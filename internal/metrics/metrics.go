// Package metrics exposes Prometheus metrics for the Eurobase gateway.
//
// Metrics are registered on a dedicated registry (not the default global one)
// and served on a separate, private HTTP listener so that they are never
// reachable from the public ingress. The expectation is that Scaleway Cockpit
// (or an in-cluster Grafana Alloy / Prometheus Agent) scrapes this endpoint
// over the cluster-internal network only.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds the Prometheus metrics for the gateway.
//
// A dedicated registry (instead of prometheus.DefaultRegisterer) keeps the
// surface small and predictable: only the metrics we explicitly register are
// exposed.
type Registry struct {
	reg *prometheus.Registry

	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	requestsInFlight prometheus.Gauge
	panicsTotal     prometheus.Counter
	buildInfo       *prometheus.GaugeVec
}

// New creates and registers all gateway metrics.
//
// The buildVersion label is attached to the eurobase_build_info gauge so
// dashboards can correlate latency spikes / error rates with specific
// rollouts.
func New(buildVersion string) *Registry {
	reg := prometheus.NewRegistry()

	// Standard Go runtime + process collectors (goroutines, GC, memory, FDs).
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	r := &Registry{
		reg: reg,
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "eurobase_http_requests_total",
				Help: "Total number of HTTP requests handled by the gateway, labelled by method, matched route pattern, and status code class.",
			},
			[]string{"method", "route", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "eurobase_http_request_duration_seconds",
				Help:    "HTTP request latency in seconds, labelled by method and matched route pattern.",
				Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"method", "route"},
		),
		requestsInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "eurobase_http_requests_in_flight",
				Help: "Number of HTTP requests currently being served by the gateway.",
			},
		),
		panicsTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "eurobase_http_panics_total",
				Help: "Total number of panics recovered by the gateway middleware.",
			},
		),
		buildInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "eurobase_build_info",
				Help: "Gateway build metadata. Value is always 1; the version label carries the actual information.",
			},
			[]string{"version"},
		),
	}

	reg.MustRegister(
		r.requestsTotal,
		r.requestDuration,
		r.requestsInFlight,
		r.panicsTotal,
		r.buildInfo,
	)

	if buildVersion == "" {
		buildVersion = "unknown"
	}
	r.buildInfo.WithLabelValues(buildVersion).Set(1)

	return r
}

// Handler returns the HTTP handler that serves the Prometheus exposition
// format for this registry.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		Registry:          r.reg,
		EnableOpenMetrics: true,
	})
}

// IncPanic increments the panic counter. Call this from your recover middleware.
func (r *Registry) IncPanic() {
	r.panicsTotal.Inc()
}

// Middleware records request count, latency, and in-flight count for every
// request that passes through the chi router.
//
// The "route" label uses chi's matched route pattern (e.g.
// "/platform/projects/{id}/functions/{name}") rather than the raw URL path.
// This keeps cardinality bounded — without it, per-project IDs in the path
// would create one time series per project, which can explode Prometheus
// memory usage on a shared tenant platform.
//
// Requests that do not match any route fall back to "unmatched" so 404s
// don't bloat cardinality either.
func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requestsInFlight.Inc()
		defer r.requestsInFlight.Dec()

		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rw, req)

		route := "unmatched"
		if rctx := chi.RouteContext(req.Context()); rctx != nil {
			if p := rctx.RoutePattern(); p != "" {
				route = p
			}
		}

		status := statusClass(rw.status)
		r.requestsTotal.WithLabelValues(req.Method, route, status).Inc()
		r.requestDuration.WithLabelValues(req.Method, route).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder captures the status code written to the response.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// statusClass reduces status codes to the bucket "2xx"/"3xx"/"4xx"/"5xx".
// Keeping cardinality low is the whole point; exact codes should live in logs
// and in the eurobase_http_requests_total metric's path-level aggregation.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return strconv.Itoa(code)
	}
}

// Serve starts a private HTTP server that exposes the /metrics endpoint on
// the given address. It blocks until ctx is cancelled or the listener fails.
//
// This MUST be bound to a cluster-internal address only — it is not
// authenticated and exposes memory usage, goroutine counts, per-route
// latency, and panic counts that an attacker could use for reconnaissance.
func (r *Registry) Serve(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", r.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("metrics server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
