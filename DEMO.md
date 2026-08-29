# End-to-End Demo: Simulated GPU Fault Detection and Remediation

This walks through the full detection-to-remediation loop -- the same
loop described in the paper's architecture diagram -- against a real
Kubernetes cluster, **without needing real GPU hardware, DCGM, or the
telemetry-collector DaemonSet** described (but explicitly not included)
in the README and paper.

## What this does and does not prove

This demonstrates the **reproduction path**:

```
Synthetic signal generator -> Node annotations -> GPU Guardian -> cordon/drain
```

It does **not** demonstrate the **real GPU path** (`DCGM/nvidia-smi
collector -> Node annotations`), which requires real GPU hardware and a
telemetry-collector component this repository does not include. That
collector's job is narrow and well-defined -- parse `nvidia-smi -q -x` or
DCGM output and write the same three annotations this demo writes by
hand -- but implementing and validating it against real hardware is
explicitly out of scope here, as stated in the README and the paper's
Limitations section.

What this demo *does* prove: that the operator's reconciliation loop
correctly reads GPU health signals from node annotations, evaluates them
against a declarative `GPUHealthPolicy`, and applies real Kubernetes
remediation (cordon) -- the entire `GPU Guardian` half of the pipeline,
exercised end-to-end against a real (if local) Kubernetes API server.

## Prerequisites

- `kind` (`go install sigs.k8s.io/kind@latest` or see kind's install docs)
- `kubectl`
- Go 1.22+
- This repository, cloned locally

## Steps

### 1. Create a local cluster

```bash
kind create cluster --name gpu-guardian-demo
kubectl cluster-info --context kind-gpu-guardian-demo
```

### 2. Apply the CRD and sample policy

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/sample-policy.yaml
kubectl get gpuhealthpolicy
# should show: default-gpu-nodes
```

Note: RBAC and the Deployment manifest (`deploy/rbac.yaml`,
`deploy/deployment.yaml`) are **not needed for this demo** -- those are
for running the operator in-cluster as a pod with its own
ServiceAccount. For this local demo we run the operator as a plain
process against `kubectl proxy`, which authenticates using your own
`kubectl` credentials (cluster-admin on a fresh `kind` cluster).

### 3. Start `kubectl proxy` in one terminal

```bash
kubectl proxy --port=8001
```

Leave this running.

### 4. Build and run the operator locally, in a second terminal

```bash
go build -o bin/manager ./cmd/manager
GPU_GUARDIAN_DEV_API_HOST=http://localhost:8001 ./bin/manager
```

You should see structured JSON logs, including:

```json
{"level":"INFO","msg":"gpu-guardian-operator starting","reconcileInterval":30000000000}
{"level":"INFO","msg":"metrics server listening","addr":":8080"}
```

and, every 30 seconds, a reconcile pass against the (currently healthy,
since no GPU nodes exist yet) cluster.

### 5. In a third terminal, identify your kind node and inject a synthetic fault

```bash
kubectl get nodes
# e.g. gpu-guardian-demo-control-plane

./demo/simulate-gpu-fault.sh gpu-guardian-demo-control-plane
```

This labels the node `node-role/gpu=true` (matching the sample policy's
`nodeSelector`) and annotates it with a synthetic Xid-error count of 6 --
above the sample policy's `xidErrorThreshold: 5`.

### 6. Watch the operator detect and remediate it

Within one reconcile interval (30 seconds), the second terminal (running
the operator) should log:

```json
{"level":"WARN","msg":"unhealthy GPU node detected","node":"gpu-guardian-demo-control-plane","reason":"Xid error count exceeded threshold","action":"drain"}
```

And the node itself should be cordoned:

```bash
kubectl get node gpu-guardian-demo-control-plane -w
# STATUS should change to "Ready,SchedulingDisabled"
```

### 7. Verify the full remediation state

```bash
# Confirm the node is cordoned
kubectl get node gpu-guardian-demo-control-plane -o jsonpath='{.spec.unschedulable}{"\n"}'
# -> true

# Confirm the idempotency annotation was written
kubectl get node gpu-guardian-demo-control-plane \
  -o jsonpath='{.metadata.annotations.gpu-guardian\.milindsisodiya\.dev/remediated-at}{"\n"}'
# -> an RFC3339 timestamp

# Confirm a Kubernetes Event was recorded
kubectl get events --field-selector reason=GPUUnhealthy

# Confirm the operator's own metrics reflect it
curl -s localhost:8080/metrics | grep gpu_guardian_node_healthy
# -> gpu_guardian_node_healthy{node="gpu-guardian-demo-control-plane"} 0
curl -s localhost:8080/metrics | grep gpu_guardian_remediations_total
# -> gpu_guardian_remediations_total{action="drain"} 1
```

### 8. Confirm idempotency (a second reconcile pass does not re-remediate)

Wait another reconcile interval and check the operator logs again -- you
should **not** see a second `"unhealthy GPU node detected"` log line for
the same node, because the `remediated-at` annotation from Step 7
prevents re-triggering. This exercises the idempotency property
described in the paper's Section 3.6.

### 9. Reset and try again (optional)

```bash
kubectl annotate node gpu-guardian-demo-control-plane \
  gpu-guardian.milindsisodiya.dev/xid-errors- \
  gpu-guardian.milindsisodiya.dev/remediated-at-
kubectl uncordon gpu-guardian-demo-control-plane
```

Try a different signal, e.g. an ECC error count above threshold, or a
thermal-throttle flag:

```bash
kubectl annotate node gpu-guardian-demo-control-plane \
  gpu-guardian.milindsisodiya.dev/ecc-errors=12 --overwrite
# or
kubectl annotate node gpu-guardian-demo-control-plane \
  gpu-guardian.milindsisodiya.dev/thermal-throttle=true --overwrite
```

### 10. Tear down

```bash
kind delete cluster --name gpu-guardian-demo
```

## What this demo does not cover (honest limitations, consistent with the paper)

- **No real GPU hardware or DCGM involved.** The signal is written by
  hand, not parsed from real telemetry. Validating the (not-included)
  DCGM/nvidia-smi collector against real hardware is separate future
  work.
- **Single-node cluster.** This demonstrates correctness of the
  detection-to-remediation logic, not cluster-scale behavior (see the
  paper's Section 7.1/8.1 for that open item).
- **`action: drain` in the sample policy will attempt to evict pods** on
  the node once cordoned. On a fresh single-node `kind` cluster with no
  other workloads scheduled, there's nothing to evict, so this step
  completes trivially. To see real pod eviction, deploy a test workload
  (without a GPU toleration mismatch) before Step 5.
