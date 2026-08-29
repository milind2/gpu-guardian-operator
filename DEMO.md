# End-to-End Demo: Signal → Reporter → Annotation → Guardian → Remediation

This walks through the **complete** pipeline described in the paper's
architecture diagram, with every stage represented by real, running
software:

```
Simulated GPU signal (a JSON file you edit)
      |
      v
simulated-health-reporter   <-- a real Go binary, polling and publishing,
      |                          structurally identical to a real DCGM
      v                          collector (see its package doc comment)
Kubernetes node annotations
      |
      v
gpu-guardian-operator        <-- the actual controller under test
      |
      v
cordon / drain
```

Earlier versions of this repository's demo had a human run a single
`kubectl annotate` command in place of the entire reporter stage. That
tested the operator's reconcile logic but left the "signal -> annotation"
stage entirely unrepresented by any software. This version closes that
gap: `simulated-health-reporter` is a real binary that reads a signal
source and publishes node annotations, exactly like a real DCGM-based
collector would -- only the signal *source* (a JSON file instead of
`nvidia-smi`/DCGM) is simulated. Everything downstream of that file read
is real.

## What this does and does not prove

**Proves:** the full pipeline works end-to-end against a real Kubernetes
API server -- a real reporter process publishing real annotations, read
and acted on by the real operator, resulting in a real cordon.

**Does not prove:** that the (not included) real DCGM/nvidia-smi
integration correctly parses actual GPU hardware telemetry into the same
signal shape. That remains explicitly out of scope (README, paper
Section 8.3) -- swapping `readSignal()`'s file read for a real
`nvidia-smi -q -x` parse is the only change a real collector would need
relative to `simulated-health-reporter`, but it hasn't been built or
validated against real hardware.

## Prerequisites

- `kind`, `kubectl`, Go 1.22+
- This repository, cloned locally

## Steps

### 1. Create a local cluster and apply the CRD/policy

```bash
kind create cluster --name gpu-guardian-demo
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/sample-policy.yaml
```

### 2. Start `kubectl proxy` (terminal 1)

```bash
kubectl proxy --port=8001
```

### 3. Build both binaries

```bash
go build -o bin/manager ./cmd/manager
go build -o bin/reporter ./cmd/simulated-health-reporter
```

### 4. Create the initial (healthy) signal file

```bash
mkdir -p /tmp/gpu-guardian-demo
cat > /tmp/gpu-guardian-demo/signal.json << 'EOF'
{"xidErrors": 0, "eccErrors": 0, "thermalThrottle": false}
EOF
```

### 5. Label your kind node as a GPU node (terminal 2)

```bash
kubectl get nodes
# e.g. gpu-guardian-demo-control-plane
kubectl label node gpu-guardian-demo-control-plane node-role/gpu=true --overwrite
```

### 6. Run the operator (terminal 2, stays running)

```bash
GPU_GUARDIAN_DEV_API_HOST=http://localhost:8001 ./bin/manager
```

### 7. Run the reporter once, healthy (terminal 3)

```bash
NODE_NAME=gpu-guardian-demo-control-plane \
  SIGNAL_FILE=/tmp/gpu-guardian-demo/signal.json \
  GPU_GUARDIAN_DEV_API_HOST=http://localhost:8001 \
  ONESHOT=true \
  ./bin/reporter
```

You should see `"published GPU health signal"` with all-zero values, and
the node stays schedulable -- the reporter is doing real work, but the
signal it's reporting is healthy.

### 8. Inject a fault by editing the signal file

```bash
cat > /tmp/gpu-guardian-demo/signal.json << 'EOF'
{"xidErrors": 6, "eccErrors": 2, "thermalThrottle": false}
EOF
```

(6 crosses `deploy/sample-policy.yaml`'s `xidErrorThreshold: 5`.)

### 9. Run the reporter again to publish the fault

```bash
NODE_NAME=gpu-guardian-demo-control-plane \
  SIGNAL_FILE=/tmp/gpu-guardian-demo/signal.json \
  GPU_GUARDIAN_DEV_API_HOST=http://localhost:8001 \
  ONESHOT=true \
  ./bin/reporter
```

In a real deployment this step doesn't exist as a separate manual
action -- the reporter runs continuously (omit `ONESHOT=true`) and picks
up the file change on its next 10-second poll, the same way a real
collector would pick up a real hardware fault on its next DCGM query.
Running it once with `ONESHOT=true` here just makes the demo's timing
deterministic and easy to follow step by step.

### 10. Watch the operator detect and remediate it (terminal 2's logs)

Within one reconcile interval (30s), you should see:

```json
{"level":"WARN","msg":"unhealthy GPU node detected","node":"gpu-guardian-demo-control-plane","reason":"Xid error count exceeded threshold","action":"drain"}
```

```bash
kubectl get node gpu-guardian-demo-control-plane -w
# STATUS -> Ready,SchedulingDisabled
```

### 11. Verify the full chain, end to end

```bash
# The annotation the reporter published:
kubectl get node gpu-guardian-demo-control-plane \
  -o jsonpath='{.metadata.annotations.gpu-guardian\.milindsisodiya\.dev/xid-errors}{"\n"}'
# -> 6

# The remediation the operator applied in response:
kubectl get node gpu-guardian-demo-control-plane -o jsonpath='{.spec.unschedulable}{"\n"}'
# -> true

kubectl get node gpu-guardian-demo-control-plane \
  -o jsonpath='{.metadata.annotations.gpu-guardian\.milindsisodiya\.dev/remediated-at}{"\n"}'
# -> an RFC3339 timestamp

kubectl get events --field-selector reason=GPUUnhealthy

curl -s localhost:8080/metrics | grep gpu_guardian_node_healthy
# -> gpu_guardian_node_healthy{node="gpu-guardian-demo-control-plane"} 0
```

Every arrow in the architecture diagram at the top of this file is now
backed by something you just watched happen, not just a claim.

### 12. Reset and tear down

```bash
kubectl annotate node gpu-guardian-demo-control-plane \
  gpu-guardian.milindsisodiya.dev/xid-errors- \
  gpu-guardian.milindsisodiya.dev/remediated-at-
kubectl uncordon gpu-guardian-demo-control-plane
kind delete cluster --name gpu-guardian-demo
```

## Running the reporter as a real DaemonSet instead of locally

`deploy/simulated-health-reporter-daemonset.yaml` and
`deploy/simulated-health-reporter-rbac.yaml` deploy the same binary as an
actual DaemonSet, reading its signal from a ConfigMap you edit with
`kubectl edit configmap simulated-gpu-signal -n gpu-guardian-system`
instead of a local file -- useful if you want to see this running
in-cluster (e.g. alongside the operator's own `deploy/deployment.yaml`)
rather than as local processes against `kubectl proxy`. Build and push
`Dockerfile.reporter` to a registry your cluster can pull from first.

## Honest limitations of this demo

- **No real GPU hardware or DCGM.** The signal source is a file, not
  real telemetry. See "What this does and does not prove" above.
- **Single-node cluster.** Correctness, not cluster-scale behavior (see
  paper Section 7.1/8.1).
- **`action: drain` on a fresh kind cluster with no other workloads has
  nothing to evict**, so Step 10's drain completes trivially. Deploy a
  test workload first if you want to see real pod eviction.
