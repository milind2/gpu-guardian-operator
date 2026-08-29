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
