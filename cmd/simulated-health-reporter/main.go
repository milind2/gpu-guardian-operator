// Command simulated-health-reporter is a stand-in for the real GPU
// telemetry collector this project's README and paper describe but do
// not include -- a DaemonSet that would, on each GPU node, periodically
// query DCGM or `nvidia-smi -q -x`, parse Xid/ECC/thermal state, and
// publish it as node annotations for gpu-guardian-operator to consume.
//
// This binary has exactly the same *structure* and *responsibility* as
// that real collector -- poll a telemetry source on an interval, publish
// the result as the same three node annotations -- with only the
// telemetry source itself replaced: instead of querying real GPU
// hardware via DCGM, it reads a small JSON file that a human (or a test
// harness) edits to simulate a fault appearing or clearing. Everything
// downstream of that file read -- the annotation publishing, the retry
// behavior, the Kubernetes API interaction -- is real, not simulated,
// and is the same k8sclient.Client the main operator uses.
//
// This exists specifically to close the gap between "GPU Guardian can
// read node annotations" (which the unit tests and benchmarks already
// cover) and "the full signal -> annotation -> remediation pipeline
// works end-to-end against a real Kubernetes API server" (which
// previously had no runnable demonstration at all, since the
// reconciliation demo alone left a human standing in for this entire
// component via a single `kubectl annotate` command).
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/milind2/gpu-guardian-operator/internal/k8sclient"
)

// SimulatedSignal is the on-disk stand-in for what a real collector
// would parse out of `nvidia-smi -q -x` or a DCGM query -- structurally
// identical to the healthcheck.Signal fields the operator itself
// reasons about (internal/healthcheck/checker.go), so there is no
// translation layer to get wrong between "what this file says" and
// "what the operator will evaluate."
type SimulatedSignal struct {
	XidErrors       int  `json:"xidErrors"`
	EccErrors       int  `json:"eccErrors"`
	ThermalThrottle bool `json:"thermalThrottle"`
}

const (
	annoXidErrors     = "gpu-guardian.milindsisodiya.dev/xid-errors"
	annoECCErrors     = "gpu-guardian.milindsisodiya.dev/ecc-errors"
	annoThermalThrott = "gpu-guardian.milindsisodiya.dev/thermal-throttle"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// In a real DaemonSet this would come from the Kubernetes downward
	// API (fieldRef: spec.nodeName), injected as an env var by the pod
	// spec -- exactly how it's read here. This binary doesn't know or
	// care whether the value came from the downward API or was set by
	// hand for a demo; the code path is identical either way.
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		logger.Error("NODE_NAME environment variable is required (in production this comes from the Kubernetes downward API's spec.nodeName field)")
		os.Exit(1)
	}

	signalFile := os.Getenv("SIGNAL_FILE")
	if signalFile == "" {
		signalFile = "simulated-signal.json"
	}

	client, err := buildClient(logger)
	if err != nil {
		logger.Error("failed to build Kubernetes client", "err", err)
		os.Exit(1)
	}

	interval := 10 * time.Second
	logger.Info("simulated-health-reporter starting",
		"node", nodeName,
		"signalFile", signalFile,
		"pollInterval", interval,
		"note", "this is a simulated stand-in for a real DCGM/nvidia-smi collector -- see package doc comment")

	reportOnce(client, logger, nodeName, signalFile)

	// ONESHOT=true runs a single poll-and-publish cycle then exits,
	// rather than running the ticker loop a real DaemonSet would use.
	// Useful for scripted tests and for the DEMO.md walkthrough, where
	// a single deterministic publish is easier to reason about than an
	// indefinitely running process.
	if os.Getenv("ONESHOT") == "true" {
		logger.Info("ONESHOT=true, exiting after one cycle")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		reportOnce(client, logger, nodeName, signalFile)
	}
}

// reportOnce performs exactly one poll-and-publish cycle: read the
// current simulated signal, publish it as node annotations. A real
// collector's equivalent cycle would run `nvidia-smi -q -x`, parse XML,
// and publish -- the publish step (publishSignal) is unchanged either
// way.
func reportOnce(client *k8sclient.Client, logger *slog.Logger, nodeName, signalFile string) {
	sig, err := readSignal(signalFile)
	if err != nil {
		logger.Warn("could not read simulated signal file, skipping this cycle (this mirrors how a real collector would skip a cycle on a failed DCGM query rather than publish a stale or zeroed reading)", "file", signalFile, "err", err)
		return
	}

	if err := publishSignal(client, nodeName, sig); err != nil {
		logger.Error("failed to publish GPU health signal to node annotations", "node", nodeName, "err", err)
		return
	}

	logger.Info("published GPU health signal",
		"node", nodeName,
		"xidErrors", sig.XidErrors,
		"eccErrors", sig.EccErrors,
		"thermalThrottle", sig.ThermalThrottle)
}

func readSignal(path string) (SimulatedSignal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SimulatedSignal{}, fmt.Errorf("reading signal file: %w", err)
	}
	var sig SimulatedSignal
	if err := json.Unmarshal(data, &sig); err != nil {
		return SimulatedSignal{}, fmt.Errorf("parsing signal file as JSON: %w", err)
	}
	return sig, nil
}

// publishSignal writes the signal to the same three node annotations
// gpu-guardian-operator's reconciler reads (see
// internal/controller/reconciler.go's annoXidErrors/annoECCErrors/
// annoThermalThrott constants, duplicated here rather than imported
// since that package is internal/controller, not a shared library --
// keeping this binary's only compile-time dependency on the rest of the
// project scoped to the k8sclient package it genuinely needs).
func publishSignal(client *k8sclient.Client, nodeName string, sig SimulatedSignal) error {
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				annoXidErrors:     strconv.Itoa(sig.XidErrors),
				annoECCErrors:     strconv.Itoa(sig.EccErrors),
				annoThermalThrott: strconv.FormatBool(sig.ThermalThrottle),
			},
		},
	}
	return client.Patch("/api/v1/nodes/"+nodeName, patch, nil)
}

func buildClient(logger *slog.Logger) (*k8sclient.Client, error) {
	if devHost := os.Getenv("GPU_GUARDIAN_DEV_API_HOST"); devHost != "" {
		logger.Warn("using dev client (unauthenticated) -- do not use in production", "host", devHost)
		return k8sclient.NewDevClient(devHost), nil
	}
	return k8sclient.NewInClusterClient()
}
