# Designing Resilient GPU Infrastructure for Kubernetes: A Systems Approach to Provisioning and Node Health

**Milind Sisodiya**
SkywardTech; formerly Amazon Web Services

## Abstract

GPU-accelerated workloads have introduced infrastructure reliability challenges that are not fully captured by Kubernetes' conventional node health and scheduling abstractions. Large language model inference, distributed training, and batch machine learning increasingly depend on GPU nodes whose failure modes include NVIDIA Xid errors, uncorrectable ECC memory errors, and thermal degradation. These failures may occur while the corresponding Kubernetes node remains in a `Ready` state, creating a gap between control-plane health and accelerator-level health.

Production-grade systems already address large parts of this problem. NVIDIA's own NVSentinel [3], in particular, provides a mature, actively-maintained, multi-module platform that detects GPU faults via DCGM, evaluates CEL-expression-based quarantine policy with cascade-prevention circuit breakers, drains workloads with per-namespace eviction strategies, and remediates via GPU reset or node replacement -- comprehensively covering the detection-to-remediation loop this paper initially set out to examine. This paper does not claim to improve on that capability. Its contribution is narrower and lies in two places NVSentinel does not address: (1) the infrastructure-provisioning layer -- NVSentinel is installed onto GPU nodes that already exist; it does not provision them -- and (2) a minimal, single-binary, dependency-free reference implementation of the runtime detection-to-remediation pattern, built and benchmarked from scratch specifically to make the pattern's core architectural trade-offs (privilege isolation, declarative policy, polling vs. event-driven reconciliation) legible and reproducible in isolation, independent of a production system's accumulated operational complexity.

This paper presents a two-layer systems architecture reflecting that scope. The first layer provides declarative provisioning of GPU node pools on Amazon Elastic Kubernetes Service (EKS), incorporating GPU-specific scheduling isolation, Instance Metadata Service Version 2 (IMDSv2) enforcement, optional Spot capacity, and separation of infrastructure ownership from runtime autoscaling -- a concern outside NVSentinel's scope entirely. The second layer is a minimal Kubernetes-native GPU health reconciliation mechanism, presented not as a production alternative to NVSentinel but as a worked example: GPU telemetry is represented as node-level health state and evaluated against a declarative `GPUHealthPolicy`, and nodes violating policy thresholds can be cordoned or drained, using an order of magnitude less machinery (no external datastore, no multi-service architecture, a single ~5.6MB static binary) than a production fault-remediation platform requires.

The paper evaluates the computational characteristics of the policy-decision and metrics-rendering components using repeated Go microbenchmarks, with the full benchmark source and raw output reported for independent reproduction. Following correction for compiler dead-code elimination, the health decision path measured **1.888 ± 0.021 ns/op with zero allocations**, while metrics rendering required **44.36 ± 0.27 μs/op for 100 simulated nodes** and **508.95 ± 11.92 μs/op for 1,000 simulated nodes**. The study does not claim these measurements establish cluster-scale performance, nor does it claim the minimal runtime implementation is preferable to NVSentinel for production use; rather, it characterizes the computational overhead of the pattern's core primitives, and separately, addresses the genuinely distinct provisioning problem NVSentinel's documentation does not cover.

**Keywords:** Kubernetes, GPUs, cloud infrastructure, GPU reliability, infrastructure as code, EKS, Kubernetes operators, node health, GPU scheduling, fault remediation, MLOps

---

# 1. Introduction

The increasing use of GPUs for large-scale machine learning has changed the reliability characteristics of cloud-native infrastructure. Kubernetes provides mature abstractions for scheduling CPU and memory resources, maintaining node readiness, and recovering from common infrastructure failures. GPU-backed workloads, however, introduce additional failure modes and economic constraints that are not completely represented by these abstractions.

A Kubernetes node can remain operational from the control plane's perspective while the accelerator attached to that node experiences a hardware or driver-level failure. NVIDIA Xid errors, uncorrectable ECC memory errors, and thermal throttling are examples of GPU-specific conditions that can affect workload correctness or performance without necessarily causing the kubelet to report the node as `NotReady`. Consequently, relying exclusively on Kubernetes node readiness can result in workloads continuing to execute on degraded accelerators.

The problem is particularly significant because GPU infrastructure is substantially more expensive than general-purpose compute. A degraded GPU node therefore represents not only a reliability concern but also a potentially significant infrastructure-cost concern. Furthermore, GPU nodes are often intentionally isolated from general-purpose workloads through node labels, taints, and workload tolerations. These scheduling controls need to be established consistently at provisioning time and maintained throughout the node lifecycle.

This paper investigates a systems architecture addressing these two related problems:

1. **How should GPU infrastructure be provisioned so that accelerator-specific scheduling, security, and lifecycle characteristics are encoded declaratively?**
2. **How can GPU health information be translated into Kubernetes-native remediation decisions without requiring the remediation controller itself to possess privileged access to GPU devices?**

The resulting architecture separates infrastructure provisioning from runtime health reconciliation. The provisioning layer establishes the desired characteristics of GPU node pools, while the runtime layer continuously evaluates accelerator health and applies policy-defined remediation.

## 1.1 Problem Statement

The system addresses a mismatch between three layers of infrastructure state:

* **Infrastructure state:** instance type, capacity type, networking, IAM configuration, and node-pool capacity.
* **Kubernetes state:** node readiness, labels, taints, scheduling state, and workload placement.
* **Accelerator state:** GPU errors, ECC conditions, thermal behavior, and driver-level health.

Traditional Kubernetes node health mechanisms primarily represent the second category. GPU reliability requires information from the third category to influence Kubernetes scheduling and lifecycle decisions.

The central systems problem is therefore:

> How can accelerator-level health state be incorporated into Kubernetes node lifecycle management while maintaining declarative infrastructure management, privilege isolation, and operational simplicity?

## 1.2 Research Questions

This paper considers four research questions.

**RQ1.** What GPU-specific infrastructure controls should be encoded declaratively at provisioning time to provide safe and predictable GPU node pools?

**RQ2.** Can GPU health signals be separated from remediation policy such that privileged GPU host access is isolated from the Kubernetes health controller?

**RQ3.** What computational overhead is introduced by the proposed health-decision and metrics-rendering mechanisms as the number of managed GPU nodes increases?

**RQ4.** What are the architectural trade-offs between a dependency-minimal polling reconciler and a conventional Kubernetes watch/informer-based implementation?

## 1.3 Contributions

Given NVSentinel's existing capability (Section 2.4), this paper's contributions are deliberately scoped to what it does not cover, plus a reproducible pedagogical treatment of the pattern it embodies:

1. **A GPU-specific EKS provisioning model** -- scheduling isolation, IMDSv2 enforcement, Spot capacity, and separation of declarative infrastructure ownership from runtime scaling. This addresses a layer NVSentinel's documentation explicitly does not cover: NVSentinel is installed onto GPU nodes that already exist and does not provision them.
2. **A minimal, from-scratch, single-binary reference implementation** of the detection-to-remediation pattern NVSentinel implements at production scale, built specifically to make the pattern's core mechanics -- privilege separation, declarative policy, idempotent remediation -- inspectable in a few hundred lines rather than a multi-module, MongoDB-backed platform.
3. **An empirical characterization of computational overhead** for that minimal implementation's health-decision and metrics-rendering primitives, using repeated, fully reproducible microbenchmarks (source, raw output, and exact commands given in Section 4) -- offered as a worked example of rigorous, reproducible microbenchmarking methodology in this domain, not as a performance claim against NVSentinel, which this paper does not benchmark.
4. **An explicit, corrected account of a real measurement error** (compiler dead-code elimination invalidating an initial benchmark result) encountered while producing (3), documented as methodology rather than silently fixed.
5. **An explicit architectural trade-off analysis** between a dependency-minimal polling reconciler (this implementation) and a conventional Kubernetes watch/informer-based design, stated as an argued hypothesis rather than a validated result (Section 7.3).
6. **A detailed, corrected comparison** with existing GPU-management and fault-remediation systems -- NVIDIA GPU Operator, NVSentinel, and GPU-aware Node Problem Detector -- intended to give an accurate picture of what a minimal implementation trades away relative to a production platform, not to claim parity or superiority.

This paper does not claim to advance the state of the art in GPU fault detection or remediation. NVSentinel already solves that problem comprehensively, with capabilities (CEL-based policy, cascade-prevention circuit breakers, sub-node GPU reset, arbitrary-CRD monitoring) this work's minimal implementation does not attempt to match. Section 2.5 answers directly what is and is not new here, for a reader asking exactly that question.

---

# 2. Background and Related Work

## 2.1 Kubernetes and GPU Scheduling

Kubernetes represents compute resources through node capacity, resource requests, labels, taints, tolerations, and scheduling constraints. GPUs can be exposed to workloads through extended resources such as `nvidia.com/gpu`.

GPU nodes introduce an additional scheduling consideration because their cost and specialization make unrestricted workload placement undesirable. A general-purpose workload accidentally scheduled onto a GPU-capable node can consume expensive infrastructure without providing corresponding value.

A common mechanism is therefore to apply a GPU-specific taint:

```text
nvidia.com/gpu=true:NoSchedule
```

and require GPU workloads to explicitly tolerate that taint. This approach establishes a negative scheduling boundary: workloads that do not explicitly indicate GPU affinity are prevented from using GPU nodes.

## 2.2 NVIDIA GPU Operator and DCGM

The NVIDIA GPU Operator [1] automates several aspects of GPU enablement within Kubernetes, including driver and device-plugin management. NVIDIA Data Center GPU Manager (DCGM) provides telemetry and diagnostics for NVIDIA GPUs. These systems address important portions of the GPU infrastructure lifecycle, but GPU telemetry and infrastructure provisioning do not automatically imply a complete node-remediation policy. An additional control mechanism is required if accelerator health should influence Kubernetes node lifecycle operations such as cordoning or draining.

## 2.3 Node Problem Detector

Node Problem Detector (NPD) provides a Kubernetes mechanism for detecting node-level problems and exposing them through Kubernetes conditions and events. GPU-aware configurations can use NVIDIA-related signals such as XID and ECC conditions [4]. This approach is useful for integrating hardware health information into Kubernetes' existing node-health model. However, detection and remediation are separate concerns: publishing a node condition does not itself establish what should happen to workloads on the affected node. Cluster operators must provide additional automation for actions such as cordoning, draining, or replacement.

## 2.4 NVIDIA NVSentinel

NVIDIA NVSentinel [3] is an actively-maintained, open-source (Apache 2.0), production-oriented fault-remediation platform, and is the most directly comparable existing system to the runtime component examined in this paper. Based on its published documentation, NVSentinel's capability is substantially broader than a first reading of "GPU health monitoring and remediation" suggests:

- **Detection.** A GPU Health Monitor consumes DCGM telemetry for thermal issues, ECC errors, and Xid events; a Syslog Health Monitor analyzes system logs; a CSP Health Monitor integrates cloud-provider maintenance-event APIs; a Kubernetes Object Monitor watches arbitrary Custom Resources and evaluates CEL (Common Expression Language) expressions against them to generate health events from operator status or application-level signals, without requiring new code for each new resource type monitored.
- **Policy.** Fault-quarantine cordons nodes based on configurable CEL rules, with a circuit breaker specifically designed to prevent mass quarantines during cluster-wide events -- a production concern this paper's minimal implementation does not address at all.
- **Remediation.** Node-drainer supports per-namespace eviction strategies (immediate, allow-completion, delete-after-timeout). Fault-remediation creates maintenance CRDs and, for recoverable GPU faults, can trigger a GPU reset (via a dedicated `GPUReset` CRD) rather than a full node reboot, reducing remediation time from minutes to seconds. A Janitor module executes the resulting maintenance action through cloud-provider APIs or direct node commands.
- **Architecture.** These modules communicate via MongoDB change streams (an explicit, required external datastore), and health events can additionally be streamed externally in CloudEvents format for integration with existing observability pipelines.

Detection, quarantine, drain, and remediation happen automatically end-to-end in this system today, per NVSentinel's own documentation, typically completing in minutes. This is, concretely, the same detection-to-remediation loop this paper's runtime component implements -- at production scale, with substantially more sophistication (policy expressiveness, cascade protection, sub-node-granularity remediation), already shipping, already NVIDIA-maintained.

What NVSentinel's published documentation does not appear to cover is infrastructure provisioning: its installation instructions (`helm install nvsentinel ...`) assume a GPU-enabled Kubernetes cluster and NVIDIA GPU Operator already exist. It is a runtime layer installed onto infrastructure, not a mechanism for creating that infrastructure.

## 2.5 What Is Actually New Here

A reader familiar with NVSentinel is entitled to ask directly: given the above, what does this paper contribute? The honest answer has two parts.

**Nothing, with respect to GPU fault detection and remediation as a capability.** NVSentinel already solves this problem, more completely, at production scale, and this paper's minimal runtime implementation makes no claim to match it -- not on fault-source breadth (no syslog or CSP maintenance-event monitoring here), not on policy expressiveness (fixed numeric thresholds, not CEL), not on remediation sophistication (binary cordon/drain, no GPU-reset fast path, no cascade circuit breaker), and not on operational maturity (a from-scratch implementation with no production deployment history, versus NVSentinel's active release cadence and NVIDIA backing).

**Something, in two specific places NVSentinel's own documentation does not address.** First, GPU node-pool *provisioning* -- the Terraform layer in Section 3.2 -- is a genuinely separate concern from what NVSentinel does; the two are complementary rather than competing, and a deployment could reasonably use this paper's provisioning layer together with NVSentinel's runtime layer rather than this paper's minimal one. Second, this paper offers a from-scratch, fully benchmarked, fully reproducible **worked example** of the detection-to-remediation pattern's core mechanics, stripped to the primitives (privilege-separated telemetry ingestion, a declarative policy CRD, idempotent remediation, a poll-based reconcile loop) without a production system's accumulated operational surface. That has pedagogical and architectural-analysis value independent of whether anyone runs this specific implementation instead of NVSentinel -- in the same sense that a minimal from-scratch HTTP server has value for understanding HTTP even though production web servers already exist and are better -- but it is a different kind of contribution than "a new capability," and this paper does not claim otherwise.

## 2.6 Research Position

Existing systems demonstrate that GPU health monitoring and fault remediation are feasible within Kubernetes environments. The problem investigated here is therefore not whether GPU faults can be detected or remediated, but how a smaller system can structure the boundary between GPU telemetry acquisition, Kubernetes policy evaluation, infrastructure provisioning, and node remediation. The proposed architecture emphasizes composability, declarative policy, privilege separation, and a small operational footprint.

---

# 3. System Architecture

The system consists of two independently deployable layers: a **GPU infrastructure provisioning layer**, and a **GPU health reconciliation layer**. The layers communicate indirectly through Kubernetes node metadata and resource state rather than through direct application-level coupling.

## 3.1 Architectural Goals

**G1. GPU-aware provisioning.** GPU node pools should encode accelerator-specific scheduling and security requirements by default.

**G2. Declarative health policy.** GPU remediation decisions should be represented as Kubernetes configuration rather than embedded entirely within application logic.

**G3. Privilege isolation.** The component that accesses GPU-specific host information should be separated from the component responsible for Kubernetes remediation.

**G4. Idempotent remediation.** Repeated health evaluations should not repeatedly perform the same remediation operation.

**G5. Operational minimalism.** For small and moderately sized GPU clusters, the health-management mechanism should avoid unnecessary infrastructure dependencies.

## 3.2 Provisioning Layer

The provisioning layer consists of composable Terraform modules targeting Amazon EKS, separating networking, EKS control-plane configuration, and GPU node-pool configuration. This separation reflects different infrastructure lifecycles: networking and control-plane resources tend to change relatively infrequently, whereas GPU node pools may be added, resized, or retired in response to workload demand. A monolithic module would unnecessarily couple these lifecycles.

**GPU scheduling isolation.** The GPU node-pool configuration applies `nvidia.com/gpu=true:NoSchedule` by default, preventing workloads that do not explicitly require GPU capacity from being scheduled onto accelerator nodes. GPU nodes also carry the label `node-role/gpu=true`, providing a positive scheduling selector to complement the taint's negative boundary. Together they establish a consistent convention used across provisioning, health monitoring, and workload placement.

**Instance metadata protection.** The underlying EC2 launch configuration enforces IMDSv2. This control is independent of Kubernetes because the EC2 metadata service exists below the Kubernetes abstraction boundary; enforcing this posture at provisioning time avoids depending on individual workload or cluster-level configuration.

**Spot capacity.** GPU capacity is expensive, making Spot capacity attractive for interruption-tolerant workloads. The provisioning model treats capacity type as a first-class configuration dimension rather than an afterthought, particularly applicable to batch training and fault-tolerant inference services.

**Infrastructure ownership and autoscaling.** The provisioning layer uses explicit `ignore_changes` behavior for desired node-group capacity. Terraform manages relatively stable characteristics (instance type, labels, taints, IAM, security settings); a runtime autoscaler such as Cluster Autoscaler or Karpenter manages dynamic capacity. Infrastructure-as-code owns the *shape* of the node pool; runtime autoscaling owns *how many nodes exist at a given moment*. This reduces persistent Terraform plan drift caused by runtime scaling activity.

## 3.3 Runtime GPU Health Layer

The runtime component introduces a Kubernetes custom resource, `GPUHealthPolicy`, specifying the nodes a policy applies to, an XID error threshold, an ECC error threshold, and a remediation action (`cordon` or `drain`). The reconciliation loop periodically evaluates the health state associated with matching nodes:

```text
GPU telemetry -> Telemetry collector -> Kubernetes Node annotations
    -> GPUHealthPolicy evaluation
        -> healthy: no action
        -> unhealthy: cordon/drain -> Kubernetes Event + Prometheus metric
```

The important architectural property is that the telemetry source and remediation mechanism are separate.

## 3.4 Health Signal Ingestion and Privilege Isolation

The health controller does not execute `nvidia-smi` and does not directly access `/dev/nvidia*`. A separate telemetry component is responsible for obtaining accelerator-level information and representing it as Kubernetes node metadata:

```text
GPU device -> telemetry collector [privileged boundary]
    -> Kubernetes API -> Health controller -> Node remediation
```

The controller does not require direct GPU-device access. This provides two advantages: the privileged component has a narrowly defined responsibility (acquiring accelerator telemetry), and the remediation controller operates with substantially smaller host-level privileges. Components are separated according to privilege boundaries rather than solely according to functional convenience.

## 3.5 Declarative Health Policy

A conceptual policy:

```yaml
apiVersion: gpu.example.com/v1
kind: GPUHealthPolicy
spec:
  nodeSelector:
    node-role/gpu: "true"
  thresholds:
    xidErrors: 5
    eccErrors: 5
  remediation:
    action: drain
```

The exact API schema is implementation-specific, but the architectural principle is that health thresholds and remediation behavior become declarative Kubernetes state, enabling operators to modify policy without changing the controller implementation.

## 3.6 Reconciliation

The controller periodically: (1) retrieves `GPUHealthPolicy` resources, (2) identifies matching GPU nodes, (3) retrieves each node's current health signal, (4) evaluates thresholds, (5) applies remediation if violated, (6) records remediation state via annotation, and (7) emits a Kubernetes Event and Prometheus metric, before repeating after the configured interval. The remediation operation is idempotent: an annotation recording that remediation has already been applied prevents repeated reconciliation passes from reissuing the same action.

## 3.7 Polling Versus Watch-Based Reconciliation

The runtime implementation uses a polling model rather than the more conventional Kubernetes informer/watch architecture -- an explicit trade-off. A polling controller performs a complete evaluation pass every interval regardless of whether state changed, and its detection latency is bounded by that interval. A watch-based controller can react to resource changes via informer-backed caches, generally reducing unnecessary repeated list operations and reacting faster to state changes, but typically depends on Kubernetes client libraries (`client-go`, `controller-runtime`) and their associated dependency graph.

The polling architecture trades periodic list operations, potentially higher unnecessary work, and interval-bounded detection latency for fewer dependencies, a smaller implementation surface, simple API interactions, and straightforward auditability. The choice is appropriate within the bounded operating scale this implementation targets; at significantly larger cluster sizes or higher resource churn, an informer-based architecture would likely become preferable. This trade-off is stated here as an architectural expectation grounded in how the two mechanisms work, not as a claim independently validated by the measurements in Section 4 -- see Section 7.3.

---

# 4. Experimental Methodology

The evaluation characterizes the computational primitives of the health controller in isolation; it does not claim end-to-end cluster performance. No production GPU cluster or multi-node Kubernetes environment was available during the preparation of this study, so the experiments measure (1) health-policy decision overhead, (2) metrics-rendering overhead, (3) compiled binary footprint, and (4) startup RSS -- and explicitly do not measure Kubernetes API-server latency, real GPU fault injection, node draining under production workload, or cluster-scale reconciliation behavior. This scope limitation is stated once, here, and applies throughout Sections 4-6 without being restated at each subsection.

## 4.1 Environment and Tooling

Go 1.22.2, linux/amd64, single-vCPU Intel Xeon @ 2.10GHz cloud development container. The environment was not a dedicated benchmarking host: CPU frequency scaling, scheduler noise, and garbage-collector pause timing were not pinned or isolated (no `taskset` CPU pinning, no `GOMAXPROCS` restriction beyond the container's single vCPU, no GC disabling). No other intentionally CPU-intensive workload was running during measurement. This is disclosed as a limitation of measurement rigor, not elided.

All benchmarks use Go's standard harness, `go test -bench=. -benchmem`, which auto-scales each benchmark's internal iteration count until wall-clock duration exceeds a stable threshold (Go's default `-benchtime`, 1 second), rather than a fixed iteration count. `-benchmem` additionally reports heap allocations per operation via the Go runtime's memory-profiling instrumentation. Each benchmark was executed as **five independent runs** (`-count=5`); results report the mean and sample standard deviation across those five runs, not a single measurement.

## 4.2 Compiler Dead-Code-Elimination Check

An initial version of the health-check benchmark discarded its result:

```go
_, _ = c.IsUnhealthy(sig, 5, 5)
```

This is a known Go benchmarking pitfall. `go build -gcflags="-m -m"` confirmed the compiler inlines `ThresholdChecker.IsUnhealthy`:

```
$ go build -gcflags="-m -m" ./internal/healthcheck/... 2>&1 | grep -i "IsUnhealthy\|inlin"
internal/healthcheck/checker.go:30:6: can inline ThresholdChecker.IsUnhealthy with cost 35 as: method(ThresholdChecker) func(Signal, int, int) (bool, string) { switch statement }
<autogenerated>:1: inlining call to ThresholdChecker.IsUnhealthy
```

With the result unused, the compiler is free to eliminate part or all of the computation as dead code. The initial (invalid) measurement was **0.368 ns/op** -- implausibly fast for even an inlined function call plus a three-branch switch. The benchmark was corrected using the standard Go pattern of assigning the loop result to a package-level variable, which the compiler cannot prove is unobservable:

```go
var benchResult bool

func BenchmarkThresholdChecker_IsUnhealthy(b *testing.B) {
	c := ThresholdChecker{}
	sig := Signal{XidErrorCount: 3, ECCErrorCount: 2, ThermalThrottle: false}
	var r bool
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, _ = c.IsUnhealthy(sig, 5, 5)
	}
	benchResult = r
}
```

Re-run, the corrected measurement was **1.888 ns/op -- roughly 5x higher**, confirming the original number was indeed partially eliminated. The metrics-rendering benchmark was checked with the same pattern (result assigned to a package-level `benchOutput` variable rather than discarded); its results did not change materially, consistent with `Render()` performing substantial non-eliminable work (`fmt.Fprintf` calls, map iteration) regardless of whether the result is retained. All numbers reported below are from the corrected benchmarks. This correction matters generically because extremely small benchmark results can reflect compiler optimization rather than the operation under study, and is reported here as methodology rather than omitted.

## 4.3 Health Decision Benchmark

Complete benchmark source (`internal/healthcheck/checker_bench_test.go` [6]):

```go
package healthcheck

import "testing"

// benchResult is a package-level sink for the benchmark result below. This
// is a standard Go benchmarking pattern: assigning the loop's result to a
// package-level variable (rather than discarding it with `_`) prevents the
// compiler from proving the computation has no observable effect and
// eliminating it entirely, which is a real risk here since
// ThresholdChecker.IsUnhealthy is small enough to be inlined.
var benchResult bool

func BenchmarkThresholdChecker_IsUnhealthy(b *testing.B) {
	c := ThresholdChecker{}
	sig := Signal{XidErrorCount: 3, ECCErrorCount: 2, ThermalThrottle: false}
	var r bool
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, _ = c.IsUnhealthy(sig, 5, 5)
	}
	benchResult = r
}
```

Reproduce with:

```bash
git clone https://github.com/milind2/gpu-guardian-operator
cd gpu-guardian-operator
go test ./internal/healthcheck/... -bench=. -benchmem -count=5
```

Raw output (one of the five runs, unedited):

```
goos: linux
goarch: amd64
pkg: github.com/milind2/gpu-guardian-operator/internal/healthcheck
cpu: Intel(R) Xeon(R) Processor @ 2.10GHz
BenchmarkThresholdChecker_IsUnhealthy-1   	612142963	         1.887 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/milind2/gpu-guardian-operator/internal/healthcheck	1.298s
```

| Metric | Result (mean ± stdev, n=5 runs) |
|---|---|
| Decision latency | **1.888 ± 0.021 ns/op** |
| Memory allocation | **0 B/op** (all 5 runs) |
| Heap allocations | **0 allocs/op** (all 5 runs) |

The zero-allocation result is deterministic, not an average: the function operates on value types with no heap allocation on its decision path. This logic runs once per matched node on every reconcile pass; at realistic single-cluster GPU node counts (tens to low hundreds), it is not a meaningful contributor to reconcile-loop latency regardless of poll interval. It does not, however, imply the complete reconciliation operation has equivalent latency, since Kubernetes API interactions, serialization, and remediation calls are excluded from this measurement.

## 4.4 Metrics Rendering Benchmark

Complete benchmark source (`internal/metrics/metrics_bench_test.go` [6]):

```go
package metrics

import (
	"fmt"
	"testing"
)

// benchOutput is a package-level sink to prevent the compiler from
// eliminating the render call as dead code, following the same reasoning
// as the healthcheck package's benchmark.
var benchOutput string

// BenchmarkRegistry_Render_100Nodes simulates a cluster of 100 GPU nodes to
// measure the cost of producing a full Prometheus text-exposition scrape at
// that scale. This is a component-level microbenchmark of the metrics
// package in isolation -- it does not exercise the Kubernetes API client,
// network I/O, or a real cluster, and should not be read as an end-to-end
// cluster-scale reconciliation benchmark.
func BenchmarkRegistry_Render_100Nodes(b *testing.B) {
	r := NewRegistry()
	for i := 0; i < 100; i++ {
		r.SetNodeHealth(fmt.Sprintf("gpu-node-%d", i), i%10 != 0)
	}
	var out string
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = r.Render()
	}
	benchOutput = out
}

func BenchmarkRegistry_Render_1000Nodes(b *testing.B) {
	r := NewRegistry()
	for i := 0; i < 1000; i++ {
		r.SetNodeHealth(fmt.Sprintf("gpu-node-%d", i), i%10 != 0)
	}
	var out string
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = r.Render()
	}
	benchOutput = out
}
```

Reproduce with:

```bash
go test ./internal/metrics/... -bench=. -benchmem -count=5
```

Raw output (one of the five runs, unedited):

```
goos: linux
goarch: amd64
pkg: github.com/milind2/gpu-guardian-operator/internal/metrics
cpu: Intel(R) Xeon(R) Processor @ 2.10GHz
BenchmarkRegistry_Render_100Nodes-1     	   27040	     44420 ns/op	   20080 B/op	     111 allocs/op
BenchmarkRegistry_Render_1000Nodes-1    	    2390	    513841 ns/op	  242875 B/op	    1019 allocs/op
PASS
ok  	github.com/milind2/gpu-guardian-operator/internal/metrics	2.910s
```

| Simulated nodes | Latency (mean ± stdev, n=5) | Memory | Allocations |
|---|---|---|---|
| 100 | **44.36 ± 0.27 µs/op** | 20,080 B/op (all 5 runs) | 111 allocs/op (all 5 runs) |
| 1,000 | **508.95 ± 11.92 µs/op** | 242,875 B/op (all 5 runs) | 1,019 allocs/op (all 5 runs) |

Latency scaled by approximately 11.5x for a 10x increase in simulated node count, consistent with the renderer's single linear pass over each metric map; memory and allocations scaled exactly 10x, reflecting fixed per-node cost with no data-dependent branching. At typical Prometheus scrape intervals (15-60s), sub-millisecond render cost at 1,000 simulated nodes is not a scaling concern for this subsystem specifically -- though, as with 4.3, this says nothing about Kubernetes API activity, concurrent scrapes, or reconciliation scheduling under real cluster conditions.

## 4.5 Binary Footprint

| Build | Size | Command |
|---|---|---|
| Unstripped | 8.22 MB | `CGO_ENABLED=0 go build -o manager ./cmd/manager` |
| Stripped | 5.62 MB | `CGO_ENABLED=0 go build -ldflags="-s -w" -o manager ./cmd/manager` |

Both are single measurements of a deterministic build output (binary size does not vary run-to-run for fixed source and toolchain version), verified directly against the built artifact with `ls -la` immediately after each build. `-ldflags="-s -w"` strips the symbol table and DWARF debugging information, standard practice for production Go binaries.

## 4.6 Idle Memory Footprint

```bash
GPU_GUARDIAN_DEV_API_HOST=http://localhost:1 timeout 2 ./manager-stripped &
sleep 0.5
ps -o rss= -p $!
```

Result: **1.82 MB RSS** (1,820 KB), read directly from the OS process table via `ps -o rss=` against the running process, shortly after startup against an intentionally unreachable API endpoint (port 1, guaranteeing a fast connection-refused rather than a slow timeout). Unlike Sections 4.3-4.4, this was a **single measurement, not repeated across 5 runs**, and is reported as a directional data point under artificial startup conditions rather than a statistically characterized steady-state figure. A rigorous version would sample RSS at fixed intervals over an extended run against a real API server (Section 8.1).

---

# 5. Results

The experiments produce three primary observations. First, the threshold decision mechanism has negligible computational cost (1.888 ns/op) relative to the likely cost of external Kubernetes API interactions. Second, metrics rendering scales approximately linearly over the tested synthetic node range (44.36 µs at 100 nodes to 508.95 µs at 1,000 nodes). Third, the implementation maintains a small executable footprint (5.62 MB stripped) and low startup memory (1.82 MB RSS). These results characterize isolated software components rather than complete Kubernetes cluster behavior, and should be interpreted narrowly on that basis.

---

# 6. Discussion

## 6.1 Health Decision Overhead

The corrected health-check benchmark measured approximately 1.9 ns per decision with zero heap allocations. Evaluating several hundred nodes therefore contributes little computational work compared with network communication and Kubernetes API operations. Optimizing the threshold function further would provide little practical benefit unless the surrounding reconciliation architecture were already optimized; the likely bottleneck in a real deployment is external state acquisition and mutation, not threshold evaluation.

## 6.2 Metrics Rendering Scalability

The metrics renderer demonstrated approximately linear behavior over the tested node range, indicating rendering cost grows with represented-node count rather than exhibiting a superlinear increase under the tested conditions. This does not demonstrate overall controller scalability: a real cluster introduces Kubernetes API-server latency, request rate, object serialization, network latency, concurrent reconciliation, Prometheus scrape concurrency, garbage collection, and node-state churn, all outside the present evaluation.

## 6.3 Polling Versus Event-Driven Reconciliation

Polling provides implementation simplicity and dependency minimization, but its computational work is approximately proportional to (number of reconciliation cycles) x (number of evaluated nodes) even when cluster state is unchanged. An informer-based architecture can instead exploit event-driven state changes and local caches; for a large or highly dynamic cluster this difference could become operationally significant. This is not evidence that polling is generally superior to informer-based controllers -- it demonstrates that polling can be viable when a system intentionally targets a bounded GPU fleet and prioritizes implementation minimalism.

## 6.4 Privilege Isolation

| Component | Responsibility | Privilege |
|---|---|---|
| Telemetry collector | Acquire GPU health signals | GPU/host access |
| Kubernetes API | Store normalized state | API access |
| Health controller | Evaluate policy | Kubernetes node mutation |
| Prometheus | Observe controller state | Read-only metrics |

The approach does not eliminate privileged operations; it localizes them. The telemetry collector remains security-sensitive, but the architectural objective is to prevent that privilege from propagating into every component responsible for health-policy evaluation.

## 6.5 Comparison with Existing Systems

| Capability | Proposed System (minimal) | GPU Operator | NPD GPU Monitoring | NVSentinel |
|---|---|---|---|---|
| GPU node-pool provisioning | Yes, via Terraform | No | No | No |
| GPU telemetry sources | DCGM only (via external collector) | Yes/ecosystem | DCGM-derived | DCGM + syslog + CSP maintenance events + arbitrary CRDs (CEL) |
| Policy expressiveness | Fixed numeric thresholds | Not primary purpose | Conditions/events | CEL expressions, per-namespace drain strategies |
| Cascade protection | No | No | No | Yes -- circuit breaker prevents mass quarantine |
| Remediation granularity | Node-level cordon/drain only | Not primary purpose | No (detection only) | Cordon, drain, GPU reset (seconds), node reboot/replace |
| External datastore required | No | No | No | Yes -- MongoDB |
| Deployment footprint | Single ~5.6MB static binary | Multi-component | Agent + backend | Multi-module platform |
| Production deployment history | None -- reference implementation | Yes | Yes | Yes -- active NVIDIA releases |

This table is not close to symmetric, and should not be read as one: NVSentinel is ahead on essentially every capability dimension that matters for production use, and materially so on policy expressiveness, cascade protection, and remediation granularity -- none of which the minimal implementation in this paper attempts to match. The only row favoring the proposed system is provisioning, which reflects a genuine gap in what NVSentinel's documentation covers rather than a design trade-off within the runtime layer itself. The remaining "smaller footprint" rows (no datastore, single binary) are real but should be read as *what a minimal implementation costs less to run*, not as an argument that it does more, or that the cost is worth paying instead of adopting NVSentinel for any deployment that can absorb its operational footprint.

## 6.6 Provisioning and Runtime Composability

The provisioning layer's node label (`node-role/gpu=true`) and taint (`nvidia.com/gpu=true:NoSchedule`) are exactly what the health policy's default selector expects, and match the node selector/toleration a corresponding GPU-inference Helm chart uses by default. This reduces configuration translation between independently-authored systems. This is stated as an architectural design property, not an empirically validated reduction in operator configuration effort -- a formal evaluation would compare configuration steps and failure opportunities between integrated and non-integrated deployments, which this study does not conduct.

---

# 7. Limitations and Threats to Validity

## 7.1 Absence of Cluster-Scale Evaluation

As stated in Section 4, the experiments were not performed against a live multi-node cluster and do not establish end-to-end reconciliation latency, API-server request overhead, behavior under concurrent node updates, large-scale draining behavior, or performance under sustained production load. This is the most consequential limitation of the study and the top priority for follow-up work (Section 8.1).

## 7.2 Synthetic GPU Health Signals

The Section 4 microbenchmarks use synthetic health signals rather than actual DCGM-generated GPU faults, so those measurements characterize the policy evaluation mechanism rather than the complete telemetry pipeline. Separately from the benchmarks, the repository includes a runnable end-to-end demonstration (`DEMO.md`, `demo/simulate-gpu-fault.sh`) that exercises the full detection-to-remediation control loop -- policy evaluation, cordoning, event emission, idempotency -- against a real Kubernetes API server (a local `kind` cluster), by writing synthetic node annotations in place of a real DCGM collector's output. This demonstrates the reconciler's correctness end-to-end without requiring GPU hardware, but it is not a substitute for validating the (not-included) DCGM/nvidia-smi collector against real hardware, which remains future work (Section 8.3).

## 7.3 Unvalidated Polling-vs-Watch Comparison

Section 3.7's claim that watch-based reconciliation would reduce unnecessary polling work and improve reaction time follows from the architectural properties of the two mechanisms, but remains an unvalidated empirical hypothesis within this study. A controlled comparison (Section 8.2) is necessary before quantitative claims can be made.

## 7.4 Benchmark Environment Rigor

The benchmark host was a cloud development container, not a dedicated performance-testing system; CPU scheduling, frequency scaling, garbage collection, and virtualization effects were not fully controlled (Section 4.1). Nanosecond-scale results should be read as characterizing the compiled function under this test environment, not as a hardware-independent guarantee.

## 7.5 Single-Sample Memory Measurement

The 1.82 MB RSS figure (Section 4.6) is a single startup observation against an unreachable API endpoint, not a steady-state production memory requirement.

## 7.6 Remediation Granularity

The current remediation model operates at the node level and does not distinguish workload classes -- a node containing both latency-sensitive inference and interruptible batch workloads is treated as a single remediation unit. More sophisticated policies could prioritize eviction by workload priority, availability requirements, or checkpointability.

---

# 8. Future Work

**8.1 Cluster-scale evaluation.** Real clusters at approximately 10, 50, and 100 nodes, measuring reconciliation latency, API request rate, CPU/memory utilization, detection latency, remediation latency, and recovery behavior. This is the single most important open item given Section 7.1.

**8.2 Polling vs. informer-based evaluation.** A controlled `client-go`/informer-based reimplementation of the same policy engine, compared on CPU consumption, API-server request volume, memory consumption, state-change reaction time, steady-state idle cost, and behavior under high node churn -- turning the Section 3.7/6.3 architectural hypothesis into an experimentally testable result.

**8.3 Fault injection with real hardware.** The repository's `DEMO.md` already exercises the detection-to-remediation path end to end using synthetic node annotations (Section 7.2); the remaining gap is validating the same path against a real DCGM/nvidia-smi collector and real, controlled GPU faults (XID events, ECC errors, thermal throttling, repeated health-state transitions) rather than hand-written signals.

**8.4 Workload-aware remediation.** Distinguishing workload classes so remediation can protect critical inference workloads, evict interruptible batch workloads, prevent new GPU scheduling, and replace nodes as appropriate, rather than treating all workloads on an unhealthy node identically.

**8.5 Multi-cloud provisioning.** A GKE-equivalent GPU node-pool module, evaluating whether the AWS design's conventions transfer across cloud-specific differences in accelerator availability, autoscaling, node replacement, networking, and identity management, rather than assuming they do.

---

# 9. Conclusion

GPU-backed Kubernetes infrastructure introduces reliability and operational challenges not fully represented by conventional Kubernetes node-health abstractions: a node may remain `Ready` while its accelerator exhibits errors capable of affecting workload correctness or performance, and GPU infrastructure's substantially higher cost makes incorrect scheduling and prolonged operation of degraded nodes particularly undesirable. Production systems, most notably NVIDIA's NVSentinel, already address the detection-to-remediation side of this problem comprehensively -- this paper does not contribute a new capability there, and says so directly in Section 2.5 rather than leaving that comparison implicit.

What this paper presents is narrower: declarative GPU infrastructure provisioning on Amazon EKS (scheduling isolation via taints/labels, IMDSv2 enforcement, first-class Spot capacity, and separation of Terraform's ownership of node-pool structure from runtime autoscaling) addressing a layer NVSentinel's own documentation does not cover, and a minimal, from-scratch, fully benchmarked reference implementation of the detection-to-remediation pattern NVSentinel implements at production scale -- offered as a reproducible worked example of the pattern's core mechanics, not as something a real deployment should run instead of NVSentinel.

The evaluation characterized the computational cost of that minimal implementation's controller primitives, fully reproducible via the benchmark source, raw output, and exact commands given in Section 4, rather than claiming complete cluster-scale performance or a performance comparison against NVSentinel, which this paper does not benchmark. After correcting an initial benchmark for compiler dead-code elimination -- itself a methodological finding worth reporting, not just the corrected number -- the health decision path measured 1.888 ± 0.021 ns/op with zero allocations, while metrics rendering measured 44.36 ± 0.27 µs/op at 100 simulated nodes and 508.95 ± 11.92 µs/op at 1,000 simulated nodes. These results indicate the decision and metrics-rendering primitives are unlikely to dominate computational cost at tested scales, while leaving Kubernetes API interactions and cluster-scale behavior as the open empirical questions Section 8 outlines.

The principal architectural trade-off examined -- polling versus informer-based reconciliation -- is presented as a deliberate, scoped design choice for a bounded GPU fleet and an unvalidated hypothesis pending the comparison proposed in Section 8.2, not as a general claim of superiority over established controller patterns. Read together with Section 2.5's direct answer to "what is actually new here," this paper's contribution is a provisioning layer addressing a genuine gap in existing tooling, plus a small, reproducible, honestly-scoped teaching example of a pattern that a production reader should, for real deployments, most likely satisfy with NVSentinel rather than with the implementation described here.

---

# Artifact Availability

The implementations, benchmark source, and this article's reproduction guide are publicly available:

- **Infrastructure provisioning artifact:** Sisodiya, M. *terraform-aws-eks-gpu-nodepool* [5].
- **GPU health-management artifact, including benchmark source and `REPRODUCTION.md`:** Sisodiya, M. *gpu-guardian-operator* [6].

Every quantitative claim in Section 4 can be independently reproduced from these repositories using the commands given inline above, or the consolidated step-by-step guide in `REPRODUCTION.md`.

---

# References

[1] NVIDIA Corporation. *GPU Operator: Automated GPU Management and Monitoring for Kubernetes*. https://github.com/NVIDIA/gpu-operator

[2] Kubernetes SIG API Machinery. *Custom Resources*. Kubernetes Documentation. https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/

[3] NVIDIA Corporation. *NVSentinel: Cross-Platform Fault Remediation Service for GPU-Accelerated Computing Environments*. https://github.com/NVIDIA/NVSentinel and https://docs.nvidia.com/nvsentinel/getting-started/overview/

[4] Microsoft. *GPU Health Monitoring in Node Problem Detector (NPD) on Azure Kubernetes Service*. Microsoft Learn. https://learn.microsoft.com/en-us/azure/aks/gpu-health-monitoring

[5] Sisodiya, M. *terraform-aws-eks-gpu-nodepool*. https://github.com/milind2/terraform-aws-eks-gpu-nodepool

[6] Sisodiya, M. *gpu-guardian-operator*. https://github.com/milind2/gpu-guardian-operator
