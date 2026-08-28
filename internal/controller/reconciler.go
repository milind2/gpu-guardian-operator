// Package controller implements the reconcile loop that gives
// gpu-guardian-operator its "operator" behavior: continuously driving GPU
// node state toward what each GPUHealthPolicy declares.
package controller

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/milind2/gpu-guardian-operator/api/v1alpha1"
	"github.com/milind2/gpu-guardian-operator/internal/healthcheck"
	"github.com/milind2/gpu-guardian-operator/internal/k8sclient"
	"github.com/milind2/gpu-guardian-operator/internal/metrics"
)

const (
	policyGroupVersion = "infra.milindsisodiya.dev/v1alpha1"
	policyPlural       = "gpuhealthpolicies"

	// Annotations written by the node-level GPU health-reporting
	// DaemonSet (out of scope for this repo, documented in the README).
	annoXidErrors     = "gpu-guardian.milindsisodiya.dev/xid-errors"
	annoECCErrors     = "gpu-guardian.milindsisodiya.dev/ecc-errors"
	annoThermalThrott = "gpu-guardian.milindsisodiya.dev/thermal-throttle"

	// Annotation the operator itself writes once it has acted on a node,
	// so re-reconciles don't repeatedly cordon/drain the same node.
	annoRemediated = "gpu-guardian.milindsisodiya.dev/remediated-at"
)

// Node is the minimal subset of a corev1.Node the reconciler needs.
type Node struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
}

type nodeList struct {
	Items []Node `json:"items"`
}

// Reconciler owns one pass of the control loop: fetch policies, fetch
// matching nodes, evaluate health, remediate, update status.
type Reconciler struct {
	Client  *k8sclient.Client
	Checker healthcheck.Checker
	Logger  *slog.Logger
	Metrics *metrics.Registry
}

// ReconcileAll runs one full pass over every GPUHealthPolicy in the
// cluster. It's called on a fixed interval by cmd/manager; a production
// version would instead use watch-based informers to react immediately to
// node/CR changes, trading a small amount of latency here for a much
// simpler, dependency-free implementation.
func (r *Reconciler) ReconcileAll() error {
	var policies v1alpha1.GPUHealthPolicyList
	path := fmt.Sprintf("/apis/%s/%s", policyGroupVersion, policyPlural)
	if err := r.Client.Get(path, &policies); err != nil {
		return fmt.Errorf("listing GPUHealthPolicies: %w", err)
	}

	for _, policy := range policies.Items {
		if err := r.reconcileOne(policy); err != nil {
			r.Logger.Error("reconcile failed", "policy", policy.Metadata.Name, "err", err)
			r.Metrics.IncReconcileErrors(policy.Metadata.Name)
		}
	}
	return nil
}

func (r *Reconciler) reconcileOne(policy v1alpha1.GPUHealthPolicy) error {
	log := r.Logger.With("policy", policy.Metadata.Name)

	var nodes nodeList
	if err := r.Client.Get("/api/v1/nodes", &nodes); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	var unhealthy []string
	matched := 0

	for _, node := range nodes.Items {
		if !matchesSelector(node.Metadata.Labels, policy.Spec.NodeSelector) {
			continue
		}
		matched++

		sig := readSignal(node)
		bad, reason := r.Checker.IsUnhealthy(sig, policy.Spec.XidErrorThreshold, policy.Spec.ECCErrorThreshold)
		r.Metrics.SetNodeHealth(node.Metadata.Name, !bad)

		if !bad {
			continue
		}
		unhealthy = append(unhealthy, node.Metadata.Name)

		if node.Metadata.Annotations[annoRemediated] != "" {
			// Already remediated this occurrence; wait for an operator
			// or autoscaler to replace/heal the node before acting again.
			continue
		}

		log.Warn("unhealthy GPU node detected", "node", node.Metadata.Name, "reason", reason, "action", policy.Spec.Action)
		if err := r.remediate(node.Metadata.Name, policy.Spec.Action, reason); err != nil {
			return fmt.Errorf("remediating node %s: %w", node.Metadata.Name, err)
		}
		r.Metrics.IncRemediations(policy.Spec.Action)
	}

	log.Info("reconcile complete", "matchedNodes", matched, "unhealthyNodes", len(unhealthy))
	return r.updateStatus(policy, matched, unhealthy)
}

func matchesSelector(labels, selector map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func readSignal(node Node) healthcheck.Signal {
	xid, _ := strconv.Atoi(node.Metadata.Annotations[annoXidErrors])
	ecc, _ := strconv.Atoi(node.Metadata.Annotations[annoECCErrors])
	throttle := node.Metadata.Annotations[annoThermalThrott] == "true"
	return healthcheck.Signal{
		NodeName:        node.Metadata.Name,
		XidErrorCount:   xid,
		ECCErrorCount:   ecc,
		ThermalThrottle: throttle,
	}
}

// remediate cordons (and optionally drains) the node, then annotates it so
// this reconcile pass is idempotent.
func (r *Reconciler) remediate(nodeName, action, reason string) error {
	cordonPatch := map[string]any{
		"spec": map[string]any{"unschedulable": true},
		"metadata": map[string]any{
			"annotations": map[string]string{
				annoRemediated: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := r.Client.Patch("/api/v1/nodes/"+nodeName, cordonPatch, nil); err != nil {
		return err
	}
	if err := r.recordEvent(nodeName, reason); err != nil {
		// Event recording failures shouldn't fail the reconcile.
		r.Logger.Warn("failed to record event", "node", nodeName, "err", err)
	}

	if action != "drain" {
		return nil
	}
	return r.evictPodsOnNode(nodeName)
}

// evictPodsOnNode evicts non-DaemonSet pods from the node via the standard
// Eviction API, mirroring what `kubectl drain` does.
func (r *Reconciler) evictPodsOnNode(nodeName string) error {
	var pods struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				OwnerRefs []struct {
					Kind string `json:"kind"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
		} `json:"items"`
	}
	path := "/api/v1/pods?fieldSelector=spec.nodeName=" + nodeName
	if err := r.Client.Get(path, &pods); err != nil {
		return fmt.Errorf("listing pods on node: %w", err)
	}

	for _, pod := range pods.Items {
		if isDaemonSetOwned(pod.Metadata.OwnerRefs) {
			continue // DaemonSet pods stay; they'll be recreated on the cordoned node as needed for teardown/diagnostics.
		}
		eviction := map[string]any{
			"apiVersion": "policy/v1",
			"kind":       "Eviction",
			"metadata": map[string]string{
				"name":      pod.Metadata.Name,
				"namespace": pod.Metadata.Namespace,
			},
		}
		evictPath := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/eviction", pod.Metadata.Namespace, pod.Metadata.Name)
		if err := r.Client.Post(evictPath, eviction, nil); err != nil {
			r.Logger.Warn("failed to evict pod", "pod", pod.Metadata.Name, "err", err)
		}
	}
	return nil
}

func isDaemonSetOwned(refs []struct {
	Kind string `json:"kind"`
}) bool {
	for _, ref := range refs {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func (r *Reconciler) recordEvent(nodeName, reason string) error {
	event := map[string]any{
		"apiVersion": "v1",
		"kind":       "Event",
		"metadata": map[string]string{
			"generateName": "gpu-guardian-",
			"namespace":    "default",
		},
		"involvedObject": map[string]string{
			"kind": "Node",
			"name": nodeName,
		},
		"reason":  "GPUUnhealthy",
		"message": reason,
		"type":    "Warning",
		"source":  map[string]string{"component": "gpu-guardian-operator"},
	}
	return r.Client.Post("/api/v1/namespaces/default/events", event, nil)
}

func (r *Reconciler) updateStatus(policy v1alpha1.GPUHealthPolicy, matched int, unhealthy []string) error {
	statusPatch := map[string]any{
		"status": v1alpha1.GPUHealthPolicyStatus{
			ObservedNodes:   matched,
			UnhealthyNodes:  unhealthy,
			LastReconcileAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	path := fmt.Sprintf("/apis/%s/%s/%s/status", policyGroupVersion, policyPlural, policy.Metadata.Name)
	return r.Client.Patch(path, statusPatch, nil)
}
