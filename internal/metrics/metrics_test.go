package metrics

import "testing"

func TestRegistryRender(t *testing.T) {
	r := NewRegistry()
	r.SetNodeHealth("gpu-node-1", true)
	r.SetNodeHealth("gpu-node-2", false)
	r.IncRemediations("cordon")
	r.IncRemediations("cordon")
	r.IncReconcileErrors("default-policy")

	out := r.Render()

	want := []string{
		`gpu_guardian_node_healthy{node="gpu-node-1"} 1`,
		`gpu_guardian_node_healthy{node="gpu-node-2"} 0`,
		`gpu_guardian_remediations_total{action="cordon"} 2`,
		`gpu_guardian_reconcile_errors_total{policy="default-policy"} 1`,
	}
	for _, w := range want {
		if !contains(out, w) {
			t.Errorf("Render() missing expected line %q\nfull output:\n%s", w, out)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
