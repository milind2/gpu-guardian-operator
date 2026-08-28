// Command manager runs the gpu-guardian-operator control loop: it watches
// GPUHealthPolicy custom resources, evaluates GPU node health against
// them, and cordons/drains nodes that fall outside acceptable thresholds.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/milind2/gpu-guardian-operator/internal/controller"
	"github.com/milind2/gpu-guardian-operator/internal/healthcheck"
	"github.com/milind2/gpu-guardian-operator/internal/k8sclient"
	"github.com/milind2/gpu-guardian-operator/internal/metrics"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	client, err := buildClient(logger)
	if err != nil {
		logger.Error("failed to build Kubernetes client", "err", err)
		os.Exit(1)
	}

	reg := metrics.NewRegistry()
	reconciler := &controller.Reconciler{
		Client:  client,
		Checker: healthcheck.ThresholdChecker{},
		Logger:  logger,
		Metrics: reg,
	}

	go serveMetrics(reg, logger)

	interval := 30 * time.Second
	logger.Info("gpu-guardian-operator starting", "reconcileInterval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately on startup, then on every tick.
	runOnce(reconciler, logger)
	for range ticker.C {
		runOnce(reconciler, logger)
	}
}

func runOnce(r *controller.Reconciler, logger *slog.Logger) {
	if err := r.ReconcileAll(); err != nil {
		logger.Error("reconcile pass failed", "err", err)
	}
}

func buildClient(logger *slog.Logger) (*k8sclient.Client, error) {
	if devHost := os.Getenv("GPU_GUARDIAN_DEV_API_HOST"); devHost != "" {
		logger.Warn("using dev client (unauthenticated) -- do not use in production", "host", devHost)
		return k8sclient.NewDevClient(devHost), nil
	}
	return k8sclient.NewInClusterClient()
}

func serveMetrics(reg *metrics.Registry, logger *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(reg.Render()))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := ":8080"
	logger.Info("metrics server listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("metrics server stopped", "err", err)
	}
}
