// Package metrics exposes gpu-guardian-operator's health signals in
// Prometheus text exposition format without pulling in the
// prometheus/client_golang dependency -- consistent with the rest of the
// project's zero-third-party-dependency approach.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu               sync.Mutex
	nodeHealthy      map[string]bool
	reconcileErrors  map[string]int
	remediationCount map[string]int
}

func NewRegistry() *Registry {
	return &Registry{
		nodeHealthy:      map[string]bool{},
		reconcileErrors:  map[string]int{},
		remediationCount: map[string]int{},
	}
}

func (r *Registry) SetNodeHealth(node string, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodeHealthy[node] = healthy
}

func (r *Registry) IncReconcileErrors(policy string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconcileErrors[policy]++
}

func (r *Registry) IncRemediations(action string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remediationCount[action]++
}

// Render produces Prometheus text exposition format for scraping.
func (r *Registry) Render() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP gpu_guardian_node_healthy 1 if the node's GPU health signal is within thresholds, 0 otherwise\n")
	b.WriteString("# TYPE gpu_guardian_node_healthy gauge\n")
	for _, node := range sortedKeys(r.nodeHealthy) {
		v := 0
		if r.nodeHealthy[node] {
			v = 1
		}
		fmt.Fprintf(&b, "gpu_guardian_node_healthy{node=%q} %d\n", node, v)
	}

	b.WriteString("# HELP gpu_guardian_reconcile_errors_total Reconcile errors per policy\n")
	b.WriteString("# TYPE gpu_guardian_reconcile_errors_total counter\n")
	for _, policy := range sortedKeys(r.reconcileErrors) {
		fmt.Fprintf(&b, "gpu_guardian_reconcile_errors_total{policy=%q} %d\n", policy, r.reconcileErrors[policy])
	}

	b.WriteString("# HELP gpu_guardian_remediations_total Remediation actions taken, by action type\n")
	b.WriteString("# TYPE gpu_guardian_remediations_total counter\n")
	for _, action := range sortedKeys(r.remediationCount) {
		fmt.Fprintf(&b, "gpu_guardian_remediations_total{action=%q} %d\n", action, r.remediationCount[action])
	}

	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
