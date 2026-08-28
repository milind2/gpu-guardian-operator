// Package v1alpha1 defines the GPUHealthPolicy custom resource, the API
// surface the gpu-guardian-operator reconciles against.
package v1alpha1

// GPUHealthPolicy describes how a set of GPU nodes should be monitored and
// remediated when they report unhealthy GPU hardware (ECC errors, Xid
// errors, thermal throttling, etc).
type GPUHealthPolicy struct {
	APIVersion string                `json:"apiVersion"`
	Kind       string                `json:"kind"`
	Metadata   ObjectMeta            `json:"metadata"`
	Spec       GPUHealthPolicySpec   `json:"spec"`
	Status     GPUHealthPolicyStatus `json:"status,omitempty"`
}

type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
}

type GPUHealthPolicySpec struct {
	// NodeSelector restricts which nodes this policy applies to,
	// e.g. {"node-role/gpu": "true"}.
	NodeSelector map[string]string `json:"nodeSelector"`

	// CheckIntervalSeconds controls how often the operator re-evaluates
	// node health for nodes matched by this policy.
	CheckIntervalSeconds int `json:"checkIntervalSeconds"`

	// XidErrorThreshold is the number of NVIDIA Xid errors (reported via
	// node annotation by the node-level health-reporting DaemonSet)
	// tolerated in a rolling window before remediation triggers.
	XidErrorThreshold int `json:"xidErrorThreshold"`

	// ECCErrorThreshold is the number of uncorrectable ECC memory errors
	// tolerated before remediation triggers.
	ECCErrorThreshold int `json:"eccErrorThreshold"`

	// Action is the remediation action taken once a node crosses the
	// configured thresholds: "cordon" or "drain".
	Action string `json:"action"`
}

type GPUHealthPolicyStatus struct {
	ObservedNodes   int      `json:"observedNodes"`
	UnhealthyNodes  []string `json:"unhealthyNodes,omitempty"`
	LastReconcileAt string   `json:"lastReconcileAt,omitempty"`
}

// GPUHealthPolicyList is the list wrapper the Kubernetes API returns for
// LIST requests against the CRD.
type GPUHealthPolicyList struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Items      []GPUHealthPolicy `json:"items"`
}
