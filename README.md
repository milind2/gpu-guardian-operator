# gpu-guardian-operator

A small Kubernetes operator that watches GPU nodes for hardware-level
unhealthiness (Xid errors, uncorrectable ECC memory errors, thermal
throttling) and automatically cordons or drains them — turning "a GPU is
silently misbehaving somewhere in the cluster" into "the scheduler already
stopped placing pods on it, and here's a Prometheus metric and a
Kubernetes Event telling you why."

This started as a way to formalize a pattern I've implemented in production
several times in different shapes: GPU/ML infrastructure fails in ways
regular node problems don't (Xid errors, ECC errors, thermal throttling)
and the failure mode is usually *silent* — the node stays `Ready`, the
kubelet is happy, and a training or inference job just gets slower or wrong
until someone notices. This operator exists to make that failure loud and
automatic instead of quiet and manual.

## Why this design

- **Custom Kubernetes operator, not just a script.** `GPUHealthPolicy` is a
  CRD — cluster operators declare *what* healthy means (thresholds, which
  nodes, what remediation) and the operator continuously reconciles
  reality toward that declaration, the same control-loop pattern
  Kubernetes itself uses for Deployments/ReplicaSets.
- **Zero third-party dependencies.** The whole thing — REST client,
  reconciler, Prometheus metrics — is built on Go's standard library only.
  No `client-go`, no `controller-runtime`. That's a deliberate trade-off:
  a poll-based loop instead of watch-based informers, in exchange for a
  a 5.62MB stripped static binary (measured under Go 1.22.2, linux/amd64;
  see the paper's Section 4.5 and REPRODUCTION.md for the exact build
  commands), a trivially auditable supply chain, and a build that
  works with nothing but `go build`. For a larger operator managing many
  resource types at high churn, I'd reach for `controller-runtime`'s
  informer/cache machinery instead — the tradeoff flips once watch
  efficiency and boilerplate reduction start to matter more than binary
  size and dependency surface.
- **Signal comes from node annotations, not host access.** The operator
  itself never touches `/dev/nvidia*` or runs `nvidia-smi`. A separate
  DaemonSet does that instead -- `cmd/simulated-health-reporter` is a
  real, runnable version of that DaemonSet's publishing half (poll a
  source, patch node annotations); it reads a JSON file rather than
  real DCGM/nvidia-smi output, which is the one piece not yet built (see
  "What I'd add next"). This keeps the operator's
  RBAC and container image minimal, and keeps privileged GPU host access
  scoped to a single-purpose component that's easy to reason about
  independently.
- **Idempotent remediation.** Once a node is cordoned/drained for a given
  unhealthy occurrence it's annotated so repeated reconcile passes don't
  re-drain it; recovery requires either the annotation clearing (node
  healed and was re-onboarded) or an operator/autoscaler replacing it.

## Architecture

```
                     ┌─────────────────────────┐
                     │   GPUHealthPolicy (CRD)  │
                     │  thresholds + selector    │
                     └────────────┬─────────────┘
                                  │ watched (polled)
                                  ▼
┌──────────────┐        ┌─────────────────┐        ┌───────────────┐
│  GPU nodes    │◀──────▶│  gpu-guardian    │───────▶│ Prometheus     │
│ (annotations  │  read  │  reconcile loop  │ /metrics│  scrape        │
│  from health- │  health│                  │         └───────────────┘
│  reporting    │  cordon/drain via K8s API
│  DaemonSet)   │        └────────┬─────────┘
└──────────────┘                 │
                                  ▼
                          Kubernetes Events
                        (GPUUnhealthy, Warning)
```

## Repo layout

```
api/v1alpha1/         GPUHealthPolicy Go types (mirrors the CRD schema)
internal/k8sclient/   Dependency-free REST client for the K8s API server
internal/healthcheck/ Pluggable health evaluation (thresholds today)
internal/controller/  The reconcile loop: list → evaluate → remediate
internal/metrics/     Prometheus text-exposition metrics, stdlib only
cmd/manager/          Entrypoint wiring it all together + /metrics server
deploy/               CRD, RBAC, Deployment, and a sample policy
```

## Running it

Build and test:

```bash
make build
make test
```

Try it against a local cluster (kind/minikube) without building a
container image at all:

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/sample-policy.yaml
kubectl proxy --port=8001 &
make run
```

Deploy for real:

```bash
make docker   # builds ghcr.io/milind2/gpu-guardian-operator:latest
make deploy   # applies CRD, RBAC, and the Deployment
```

Check metrics:

```bash
curl localhost:8080/metrics
# gpu_guardian_node_healthy{node="gpu-node-1"} 1
# gpu_guardian_remediations_total{action="drain"} 1
```

## End-to-end demo without real GPU hardware

This repository includes `simulated-health-reporter`
(`cmd/simulated-health-reporter`), a real Go binary structurally
identical to what a DCGM/nvidia-smi-based collector would be -- it
polls a telemetry source and publishes the result as node annotations --
with only the telemetry source itself replaced by a JSON file you edit
by hand instead of real GPU hardware. [`DEMO.md`](./DEMO.md) walks
through the complete pipeline end-to-end against a local `kind` cluster:
edit the signal file, watch the reporter publish it, watch the operator
detect and cordon the node in response. Every stage in the paper's
architecture diagram is exercised by real running software; only the
telemetry *source* (file vs. real hardware) is simulated.

## What I'd add next

- Watch-based reconciliation (informers) instead of polling, once this
  needs to react in sub-second time or manage more resource kinds.
- Real DCGM/nvidia-smi integration for `simulated-health-reporter` --
  today it reads a JSON file a human edits; a production version would
  parse real GPU telemetry instead. The publishing half of that pipeline
  (annotations, RBAC, DaemonSet shape) already exists and is exercised
  end-to-end in `DEMO.md`; only the telemetry-source half is left.
- Multi-tenancy awareness — today remediation is all-or-nothing per node;
  a natural next step is cordoning workload classes selectively (e.g.
  draining best-effort batch jobs before evicting latency-sensitive
  inference pods).

## License

MIT
