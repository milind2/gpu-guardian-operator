// Package healthcheck decides whether a GPU node is healthy enough to keep
// serving workloads. It is intentionally an interface so the signal source
// can change (node annotations today, a DCGM/nvidia-smi exporter query
// tomorrow) without touching the reconciler.
package healthcheck

// Signal is the raw GPU health telemetry the operator reasons about for a
// single node. In production this is populated by a small DaemonSet that
// runs `nvidia-smi -q -x` on each GPU node and republishes the parsed
// counters as node annotations, which this checker then reads -- avoiding
// giving the operator itself privileged host access to GPU devices.
type Signal struct {
	NodeName        string
	XidErrorCount   int
	ECCErrorCount   int
	ThermalThrottle bool
}

// Checker evaluates a Signal against policy thresholds and returns whether
// the node should be considered unhealthy, plus a human-readable reason
// used in the Kubernetes Event the reconciler records.
type Checker interface {
	IsUnhealthy(sig Signal, xidThreshold, eccThreshold int) (bool, string)
}

// ThresholdChecker is the default Checker: a node is unhealthy if it
// crosses either the Xid or ECC error threshold, or is thermal throttling.
type ThresholdChecker struct{}

func (ThresholdChecker) IsUnhealthy(sig Signal, xidThreshold, eccThreshold int) (bool, string) {
	switch {
	case sig.ThermalThrottle:
		return true, "node is thermal throttling"
	case xidThreshold > 0 && sig.XidErrorCount >= xidThreshold:
		return true, "Xid error count exceeded threshold"
	case eccThreshold > 0 && sig.ECCErrorCount >= eccThreshold:
		return true, "uncorrectable ECC error count exceeded threshold"
	default:
		return false, ""
	}
}
