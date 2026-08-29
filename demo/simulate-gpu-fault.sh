#!/usr/bin/env bash
# simulate-gpu-fault.sh
#
# Simulates a GPU node crossing its Xid-error threshold by writing the
# exact node annotation gpu-guardian-operator reads (see
# internal/controller/reconciler.go, annoXidErrors), without needing real
# GPU hardware, DCGM, or the telemetry-collector DaemonSet described (but
# not included) in the README/paper.
#
# This exercises the REPRODUCTION PATH from the paper's telemetry diagram:
#   Synthetic signal generator -> Node annotations -> GPU Guardian -> cordon/drain
# as opposed to the REAL GPU PATH (DCGM/nvidia-smi collector), which
# remains explicitly out of scope.
#
# Usage:
#   ./simulate-gpu-fault.sh <node-name> [xid-error-count]
#
# Requires: kubectl pointed at a cluster (e.g. `kind` or `minikube`) that
# already has the CRD, RBAC, and sample-policy applied, and the operator
# running (either in-cluster or locally via GPU_GUARDIAN_DEV_API_HOST --
# see DEMO.md).

set -euo pipefail

NODE="${1:?Usage: $0 <node-name> [xid-error-count]}"
XID_COUNT="${2:-6}" # sample-policy.yaml's xidErrorThreshold is 5; 6 crosses it

echo "Labeling ${NODE} as a GPU node (node-role/gpu=true) so it matches the sample policy's nodeSelector..."
kubectl label node "${NODE}" node-role/gpu=true --overwrite

echo "Injecting a synthetic Xid-error signal (count=${XID_COUNT}) via node annotation..."
kubectl annotate node "${NODE}" \
  gpu-guardian.milindsisodiya.dev/xid-errors="${XID_COUNT}" \
  --overwrite

echo ""
echo "Signal injected. Within one reconcile interval (checkIntervalSeconds in"
echo "deploy/sample-policy.yaml, default 30s), the operator should cordon this node."
echo ""
echo "Watch for it with:"
echo "  kubectl get node ${NODE} -w"
echo ""
echo "Once cordoned, verify the remediation annotation and taint were applied:"
echo "  kubectl get node ${NODE} -o jsonpath='{.spec.unschedulable}{\"\n\"}'"
echo "  kubectl get node ${NODE} -o jsonpath='{.metadata.annotations.gpu-guardian\\.milindsisodiya\\.dev/remediated-at}{\"\n\"}'"
echo ""
echo "Check the operator's own metrics reflect the unhealthy node:"
echo "  curl -s localhost:8080/metrics | grep gpu_guardian_node_healthy"
echo ""
echo "To reset and try again (e.g. after changing the threshold or action):"
echo "  kubectl annotate node ${NODE} gpu-guardian.milindsisodiya.dev/xid-errors- gpu-guardian.milindsisodiya.dev/remediated-at-"
echo "  kubectl uncordon ${NODE}"
