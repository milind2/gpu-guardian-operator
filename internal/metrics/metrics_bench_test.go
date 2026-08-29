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
