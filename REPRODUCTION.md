# Reproduction Guide

This document gives the exact, complete steps to reproduce every measurement
reported in "Designing Resilient GPU Infrastructure for Kubernetes: A
Systems Approach to Provisioning and Node Health." Nothing is skipped or
summarized — every command below is exactly what was run to produce the
numbers in the article, in the order it was run, including the step where a
measurement bug was found and fixed.

If you follow these steps on your own machine, you should get numbers
within normal run-to-run variance of what's reported in the article (single-
digit-percent differences are expected and fine; anything wildly different,
e.g. 5x off, means something changed and should be investigated the same
way Step 5 below investigates it).

---

## 0. Prerequisites

- **Go 1.22 or later** installed (`go version` to check).
- **git** installed.
- A Unix-like shell (Linux or macOS; steps use `ps`, adjust for Windows/WSL
  if needed).
- No GPU, no Kubernetes cluster, and no cloud account are required for any
  step below — every measurement in the article is either a component-level
  benchmark or a static build artifact, run entirely locally. This is
  explicitly noted in the article's Section 4 introduction, which states
  what the evaluation does and does not measure.

Reference environment used to produce the article's numbers: Go 1.22.2,
linux/amd64, single-vCPU Intel Xeon @ 2.10GHz cloud container.

---

## 1. Clone the repository

```bash
git clone https://github.com/milind2/gpu-guardian-operator
cd gpu-guardian-operator
```

Confirm you're looking at the right code:

```bash
cat internal/healthcheck/checker_bench_test.go
cat internal/metrics/metrics_bench_test.go
```

Both files should match the source printed in full below. If your local
copy doesn't match (e.g. you have the original, uncorrected version with
`_, _ = c.IsUnhealthy(...)` and no package-level sink variable), your clone
is out of date relative to what this guide reproduces — pull the latest
`main` before continuing.

**`internal/healthcheck/checker_bench_test.go`** (complete, corrected version):

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

**`internal/metrics/metrics_bench_test.go`** (complete, corrected version):

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

If you'd rather not clone the repo at all, you can recreate these two files
by hand at those exact paths inside any Go module and skip straight to
Step 3.

---

## 2. Verify the project builds and its existing tests pass

This isn't a benchmark step, but it's the right first check before trusting
any performance number from a codebase — a codebase that doesn't build or
whose correctness tests fail shouldn't be trusted for performance numbers
either.

```bash
go build ./...
go vet ./...
go test ./...
```

Expected: all three commands exit with status 0 and no output beyond
`ok  	github.com/milind2/gpu-guardian-operator/internal/healthcheck	...`
and the equivalent line for `internal/metrics`.

---

## 3. Reproduce the health-check decision-logic benchmark (Article Section 4.3)

This benchmarks `ThresholdChecker.IsUnhealthy`, the function that decides
whether a single node's GPU health signal violates policy thresholds.

### 3.1 Run it once, to see raw output

```bash
go test ./internal/healthcheck/... -bench=. -benchmem -run=^$
```

- `-bench=.` runs all benchmarks in the package (there is one here).
- `-benchmem` additionally reports memory allocations per operation.
- `-run=^$` tells `go test` to skip all *regular* tests (whose names it
  would otherwise try to match against `^$`, matching nothing) and run only
  benchmarks — otherwise `go test` runs both tests and benchmarks together.

Expected output shape (your exact `ns/op` will vary slightly run to run;
your iteration count, the large integer before `ns/op`, will also differ
depending on your machine's speed since Go auto-scales it):

```
goos: linux
goarch: amd64
pkg: github.com/milind2/gpu-guardian-operator/internal/healthcheck
cpu: <your CPU model>
BenchmarkThresholdChecker_IsUnhealthy-N   	<iterations>	         1.8XX ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/milind2/gpu-guardian-operator/internal/healthcheck	<duration>
```

The `-N` suffix on the benchmark name is `GOMAXPROCS` (number of logical
CPUs Go believes it has available) — this is normal and not something you
need to control for separately unless you're specifically testing
parallelism, which this benchmark does not.

### 3.2 Run it 5 times, as the article does, for a real mean/stdev

```bash
go test ./internal/healthcheck/... -bench=. -benchmem -run=^$ -count=5
```

This prints 5 separate `BenchmarkThresholdChecker_IsUnhealthy` lines, one
per run. Record the 5 `ns/op` values.

### 3.3 Compute mean and standard deviation

Copy the 5 numbers you got into this one-liner (replace the example values
with yours):

```bash
python3 -c "
import statistics
data = [1.924, 1.887, 1.876, 1.873, 1.882]  # <- replace with your 5 values
print(f'mean={statistics.mean(data):.4f} ns/op')
print(f'stdev={statistics.stdev(data):.4f} ns/op')
"
```

The article reports **1.888 ± 0.021 ns/op** from this exact process. Your
numbers should be close (typically within 10-20%, since this is measuring
real hardware/OS-level timing that varies by machine), not identical.

---

## 4. Reproduce the metrics-rendering benchmarks (Article Section 4.4)

This benchmarks `Registry.Render`, which produces the full Prometheus
text-exposition output for a simulated set of GPU nodes, at two simulated
fleet sizes (100 and 1,000 nodes).

### 4.1 Run once for raw output

```bash
go test ./internal/metrics/... -bench=. -benchmem -run=^$
```

Expected output shape:

```
goos: linux
goarch: amd64
pkg: github.com/milind2/gpu-guardian-operator/internal/metrics
cpu: <your CPU model>
BenchmarkRegistry_Render_100Nodes-N     	<iterations>	     44XXX ns/op	   20080 B/op	     111 allocs/op
BenchmarkRegistry_Render_1000Nodes-N    	<iterations>	    5XXXXX ns/op	  242875 B/op	    1019 allocs/op
PASS
ok  	github.com/milind2/gpu-guardian-operator/internal/metrics	<duration>
```

Note that the `B/op` and `allocs/op` columns should match the article
**exactly** (20080 / 111 and 242875 / 1019) — these are deterministic byte
counts and allocation counts, not timing-sensitive, so they should not vary
across machines at all. If yours differ, the code you're running does not
match the article's benchmark source, and you should re-check Step 1.

### 4.2 Run 5 times for mean/stdev

```bash
go test ./internal/metrics/... -bench=. -benchmem -run=^$ -count=5
```

### 4.3 Compute mean and standard deviation

```bash
python3 -c "
import statistics
r100 = [44734, 44420, 44036, 44146, 44449]     # <- replace with your 5 values (ns/op)
r1000 = [517424, 513841, 521085, 495778, 496640]  # <- replace with your 5 values (ns/op)
print(f'100 nodes:  mean={statistics.mean(r100)/1000:.2f} us/op, stdev={statistics.stdev(r100)/1000:.2f} us/op')
print(f'1000 nodes: mean={statistics.mean(r1000)/1000:.2f} us/op, stdev={statistics.stdev(r1000)/1000:.2f} us/op')
"
```

The article reports **44.36 ± 0.27 µs/op** (100 nodes) and
**508.95 ± 11.92 µs/op** (1,000 nodes) from this exact process.

---

## 5. Reproduce the dead-code-elimination check (Article Section 4.2)

This is the most important step to actually reproduce, not just take on
faith, because it's the one place the article corrects a real measurement
error. Here's how to reproduce the *original bug*, confirm *why* it
happened, and confirm the *fix*.

### 5.1 Recreate the flawed benchmark

Save this as a temporary file, e.g. `internal/healthcheck/flawed_bench_test.go`:

```go
package healthcheck

import "testing"

func BenchmarkFlawed_IsUnhealthy(b *testing.B) {
	c := ThresholdChecker{}
	sig := Signal{XidErrorCount: 3, ECCErrorCount: 2, ThermalThrottle: false}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.IsUnhealthy(sig, 5, 5) // <- result discarded, this is the bug
	}
}
```

### 5.2 Confirm the function gets inlined

```bash
go build -gcflags="-m -m" ./internal/healthcheck/... 2>&1 | grep -i "IsUnhealthy\|inlin"
```

You should see a line containing `can inline ThresholdChecker.IsUnhealthy`
and a line containing `inlining call to ThresholdChecker.IsUnhealthy`. This
confirms the compiler is small enough to fold this function's body directly
into its caller — the precondition for the dead-code-elimination risk.

### 5.3 Run the flawed benchmark and observe the implausible number

```bash
go test ./internal/healthcheck/... -bench=BenchmarkFlawed -benchmem -run=^$ -count=5
```

You should see a result in the sub-nanosecond range (the article measured
0.368 ns/op) — implausibly fast for a function call plus a branching
switch statement, which is the tell that something is being optimized away.

### 5.4 Compare against the corrected benchmark

Run the real (corrected) benchmark from Step 3 again, side by side:

```bash
go test ./internal/healthcheck/... -bench=. -benchmem -run=^$ -count=5
```

The corrected `BenchmarkThresholdChecker_IsUnhealthy` should report a
number roughly **5x higher** than the flawed one from Step 5.3 — the
article measured 1.888 ns/op corrected vs. 0.368 ns/op flawed. This
confirms the discard-the-result pattern was indeed causing partial
dead-code elimination.

### 5.5 Clean up

Delete the temporary flawed benchmark file so it doesn't linger in your
working copy:

```bash
rm internal/healthcheck/flawed_bench_test.go
```

---

## 6. Reproduce the compiled binary footprint (Article Section 4.5)

```bash
CGO_ENABLED=0 go build -o /tmp/manager-unstripped ./cmd/manager
ls -la /tmp/manager-unstripped
```

Note the file size in bytes; the article reports 8.22 MB (8,224,138 bytes,
specifically, from the original build).

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o /tmp/manager-stripped ./cmd/manager
ls -la /tmp/manager-stripped
```

The article reports 5.62 MB (5,615,768 bytes) for the stripped build. The
`-ldflags="-s -w"` flags strip the symbol table and DWARF debugging
information respectively, which is why this build is smaller — this is a
standard technique for production Go binaries where interactive debugging
symbols aren't needed at runtime.

These sizes should be very close to exact matches on your machine (small
differences of a few KB are possible across different Go patch versions,
but the numbers should not differ by more than a rounding error).

---

## 7. Reproduce the idle memory footprint (Article Section 4.6)

This measures resident set size (RSS) shortly after the operator process
starts, without a real Kubernetes cluster available.

```bash
cd /tmp
GPU_GUARDIAN_DEV_API_HOST=http://localhost:1 timeout 2 ./manager-stripped 2>/tmp/manager.log &
sleep 0.5
PID=$!
ps -o rss= -p $PID
wait $PID 2>/dev/null
```

Explanation of each part:

- `GPU_GUARDIAN_DEV_API_HOST=http://localhost:1` points the operator at an
  intentionally-unreachable local address, so it starts up (initializes
  its dev-mode REST client, starts its metrics HTTP server) without
  needing a real Kubernetes API server. Port 1 is used specifically
  because it's a privileged port nothing will be listening on, guaranteeing
  a fast, deterministic connection-refused failure on its first
  reconcile attempt rather than a slow timeout.
- `timeout 2` ensures the process is killed after 2 seconds regardless, so
  this step can't hang.
- `sleep 0.5` gives the process time to fully start (Go runtime init,
  goroutine startup) before we measure it — measuring RSS at the exact
  instant of process launch would capture an artificially low,
  not-yet-initialized value.
- `ps -o rss= -p $PID` reads resident set size, in kilobytes, directly from
  the OS process table for that specific PID — this is a real OS-level
  measurement, not something the Go runtime self-reports (which could be
  argued to undercount by excluding, e.g., the Go runtime's own reserved
  address space).

The article reports **1.82 MB RSS** (1,820 KB) from this exact process,
noted explicitly as a single-sample, non-repeated measurement rather than a
statistically characterized one — see the article's Section 4.6 for why
that limitation is disclosed rather than presented as more rigorous than it
is.

---

## 8. Summary checklist

Use this to confirm you've reproduced every number in the article:

- [ ] Repo cloned, builds clean, `go vet` clean, existing tests pass (Step 2)
- [ ] Health-check benchmark: ~1.888 ns/op, 0 B/op, 0 allocs/op, from 5 runs (Step 3)
- [ ] Metrics render @ 100 nodes: ~44.36 µs/op, exactly 20080 B/op, exactly 111 allocs/op (Step 4)
- [ ] Metrics render @ 1000 nodes: ~508.95 µs/op, exactly 242875 B/op, exactly 1019 allocs/op (Step 4)
- [ ] Confirmed inlining occurs (`-gcflags="-m -m"` output) (Step 5.2)
- [ ] Reproduced the flawed 0.368 ns/op number from the discard-result pattern (Step 5.3)
- [ ] Confirmed the corrected benchmark is ~5x higher than the flawed one (Step 5.4)
- [ ] Unstripped binary: 8.22 MB (Step 6)
- [ ] Stripped binary: 5.62 MB (Step 6)
- [ ] Idle RSS: ~1.82 MB (Step 7)

If every box is checked and your numbers are within normal run-to-run
variance of the article's, you have independently reproduced every
quantitative claim in Section 4 of the article.
