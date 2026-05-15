package server

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.openviz.dev/alertmanager-webhook-relay/internal/alertmanager"
	"go.openviz.dev/alertmanager-webhook-relay/internal/config"
	"go.openviz.dev/alertmanager-webhook-relay/internal/webhook"
)

type relayServer struct {
	config  config.Config
	senders []sender
	dedupe  *lru.Cache[string, struct{}]
	metrics *metrics
}

type sender interface {
	Name() string
	Key() string
	Send(ctx context.Context, message webhook.Message) (int, string, error)
}

type metrics struct {
	requests          *prometheus.CounterVec
	downstreamResults *prometheus.CounterVec
	upstreamLatency   *prometheus.HistogramVec
}

type deliveryResult struct {
	target       string
	statusCode   int
	responseBody string
	duration     time.Duration
	err          error
	deduped      bool
	succeeded    bool
}

var (
	registerMetricsOnce sync.Once
	relayMetrics        = &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertmanager_relay_requests_total",
			Help: "Total inbound relay requests by endpoint outcome.",
		}, []string{"endpoint", "result"}),
		downstreamResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertmanager_relay_downstream_results_total",
			Help: "Total downstream delivery results by endpoint, target, and result.",
		}, []string{"endpoint", "target", "result"}),
		upstreamLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "alertmanager_relay_upstream_request_duration_seconds",
			Help:    "Latency of upstream webhook requests.",
			Buckets: prometheus.DefBuckets,
		}, []string{"target"}),
	}
)

func New(cfg config.Config) (http.Handler, error) {
	senders := make([]sender, 0, len(cfg.WebhookURLs))
	var errs []error
	for _, webhookURL := range cfg.WebhookURLs {
		sender, err := webhook.NewSender(webhookURL, cfg.RequestTimeout)
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", webhookURL, err))
		} else {
			senders = append(senders, sender)
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("error initializing senders: %w", errors.Join(errs...))
	}
	return newHandler(cfg, senders), nil
}

func newHandler(cfg config.Config, senders []sender) http.Handler {
	if cfg.MaxRequestBodyBytes <= 0 {
		cfg.MaxRequestBodyBytes = config.DefaultMaxRequestBodyBytes
	}

	registerMetricsOnce.Do(func() {
		prometheus.MustRegister(relayMetrics.requests, relayMetrics.downstreamResults, relayMetrics.upstreamLatency)
	})

	server := &relayServer{
		config:  cfg,
		senders: senders,
		dedupe:  mustNewDedupe(cfg.DedupeCacheSize),
		metrics: relayMetrics,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/v1/ingest/webhook", server.handleWebhook)
	return mux
}

func (s *relayServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	const endpoint = "webhook"
	if r.Method != http.MethodPost {
		s.metrics.requests.WithLabelValues(endpoint, "invalid_method").Inc()
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		slog.Warn("rejected webhook relay request", "endpoint", endpoint, "result", "invalid_method", "method", r.Method)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxRequestBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		result := "invalid_body"
		statusCode := http.StatusBadRequest
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			result = "request_too_large"
			statusCode = http.StatusRequestEntityTooLarge
		}
		s.metrics.requests.WithLabelValues(endpoint, result).Inc()
		http.Error(w, http.StatusText(statusCode), statusCode)
		slog.Warn("rejected webhook relay request", "endpoint", endpoint, "result", result, "max_body_bytes", s.config.MaxRequestBodyBytes, "error", err)
		return
	}
	if err := r.Body.Close(); err != nil {
		s.metrics.requests.WithLabelValues(endpoint, "invalid_body").Inc()
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		slog.Warn("rejected webhook relay request", "endpoint", endpoint, "result", "invalid_body", "error", err)
		return
	}

	payload, err := alertmanager.Decode(body)
	if err != nil {
		s.metrics.requests.WithLabelValues(endpoint, "invalid_payload").Inc()
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		slog.Warn("rejected invalid alertmanager payload", "endpoint", endpoint, "result", "invalid_payload", "body_bytes", len(body), "error", err)
		return
	}

	logFields := []any{
		"endpoint", endpoint,
		"receiver", payload.Receiver,
		"group_key", payload.GroupKey,
		"alerts_total", len(payload.Alerts),
		"firing", len(payload.Alerts.Firing()),
		"resolved", len(payload.Alerts.Resolved()),
	}
	payloadHash := hashPayload(body)

	if !s.config.SendResolved && len(payload.Alerts.Resolved()) == len(payload.Alerts) {
		s.metrics.requests.WithLabelValues(endpoint, "skipped_resolved").Inc()
		w.WriteHeader(http.StatusOK)
		slog.Info("skipped resolved-only alertmanager notification", append(logFields, "result", "skipped_resolved")...)
		return
	}

	message := webhook.Render(payload, s.config.SendResolved)

	results := make([]deliveryResult, len(s.senders))
	var wg sync.WaitGroup
	for i, target := range s.senders {
		wg.Add(1)
		go func(i int, target sender) {
			defer wg.Done()

			dedupeKey := target.Key() + ":" + payloadHash
			if _, ok := s.dedupe.Get(dedupeKey); ok {
				results[i] = deliveryResult{target: target.Name(), deduped: true, succeeded: true}
				return
			}

			start := time.Now()
			statusCode, responseBody, err := target.Send(r.Context(), message)
			duration := time.Since(start)
			result := deliveryResult{
				target:       target.Name(),
				statusCode:   statusCode,
				responseBody: responseBody,
				duration:     duration,
				err:          err,
			}
			if err != nil {
				results[i] = result
				return
			}
			if result.statusCode < 200 || result.statusCode >= 300 {
				results[i] = result
				return
			}

			s.dedupe.Add(dedupeKey, struct{}{})
			result.succeeded = true
			results[i] = result
		}(i, target)
	}
	wg.Wait()

	allSucceeded := true
	delivered := 0
	skipped := 0
	for _, result := range results {
		targetFields := append(logFields, "target", result.target)
		outcome := result.metricResult(r.Context())
		if result.deduped {
			skipped++
			s.metrics.downstreamResults.WithLabelValues(endpoint, result.target, outcome).Inc()
			slog.Info("skipped duplicate webhook delivery", append(targetFields, "result", outcome)...)
			continue
		}
		s.metrics.downstreamResults.WithLabelValues(endpoint, result.target, outcome).Inc()
		s.metrics.upstreamLatency.WithLabelValues(result.target).Observe(result.duration.Seconds())
		if result.succeeded {
			delivered++
			slog.Info("forwarded alertmanager notification to webhook", append(targetFields, "result", outcome, "status_code", result.statusCode, "duration_ms", result.duration.Milliseconds())...)
			continue
		}

		allSucceeded = false
		if result.err != nil {
			slog.Error("webhook delivery failed", append(targetFields, "result", outcome, "status_code", result.statusCode, "response_body", result.responseBody, "duration_ms", result.duration.Milliseconds(), "error", result.err)...)
			continue
		}
		slog.Error("webhook rejected message", append(targetFields, "result", outcome, "status_code", result.statusCode, "response_body", result.responseBody, "duration_ms", result.duration.Milliseconds())...)
	}

	if !allSucceeded {
		s.metrics.requests.WithLabelValues(endpoint, "downstream_failed").Inc()
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}

	s.metrics.requests.WithLabelValues(endpoint, "success").Inc()
	w.WriteHeader(http.StatusOK)
	slog.Info("completed webhook fanout", append(logFields, "result", "success", "targets_delivered", delivered, "targets_deduped", skipped)...)
}

func hashPayload(body []byte) string {
	sum := sha512.Sum512(body)
	return hex.EncodeToString(sum[:])
}

func (r deliveryResult) metricResult(requestCtx context.Context) string {
	if r.deduped {
		return "deduped"
	}
	if r.succeeded {
		return "success"
	}
	if r.err != nil {
		if errors.Is(r.err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return "upstream_timeout"
		}
		return "upstream_error"
	}
	return fmt.Sprintf("upstream_http_%d", r.statusCode)
}

func mustNewDedupe(size int) *lru.Cache[string, struct{}] {
	cache, err := lru.New[string, struct{}](size)
	if err != nil {
		panic(err)
	}
	return cache
}
