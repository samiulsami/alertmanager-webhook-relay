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

	"go.openviz.dev/alertmanager-relay/internal/alertmanager"
	"go.openviz.dev/alertmanager-relay/internal/config"
	"go.openviz.dev/alertmanager-relay/internal/webhook"
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
	requests        *prometheus.CounterVec
	upstreamLatency *prometheus.HistogramVec
}

var (
	registerMetricsOnce sync.Once
	relayMetrics        = &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertmanager_relay_requests_total",
			Help: "Total relay requests by endpoint outcome.",
		}, []string{"endpoint", "result"}),
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
	registerMetricsOnce.Do(func() {
		prometheus.MustRegister(relayMetrics.requests, relayMetrics.upstreamLatency)
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
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

	type deliveryResult struct {
		target       string
		result       string
		statusCode   int
		responseBody string
		duration     time.Duration
		err          error
		deduped      bool
		succeeded    bool
	}

	results := make([]deliveryResult, len(s.senders))
	var wg sync.WaitGroup
	for i, target := range s.senders {
		wg.Add(1)
		go func(i int, target sender) {
			defer wg.Done()

			dedupeKey := target.Key() + ":" + payloadHash
			if _, ok := s.dedupe.Get(dedupeKey); ok {
				results[i] = deliveryResult{target: target.Name(), result: "deduped", deduped: true, succeeded: true}
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
				result.result = "upstream_error"
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
					result.result = "upstream_timeout"
				}
				results[i] = result
				return
			}
			if statusCode < 200 || statusCode >= 300 {
				result.result = "upstream_non_2xx"
				if statusCode == http.StatusTooManyRequests {
					result.result = "upstream_rate_limited"
				}
				results[i] = result
				return
			}

			s.dedupe.Add(dedupeKey, struct{}{})
			result.result = "success"
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
		s.metrics.upstreamLatency.WithLabelValues(result.target).Observe(result.duration.Seconds())
		if result.deduped {
			skipped++
			slog.Info("skipped duplicate webhook delivery", append(targetFields, "result", result.result)...)
			continue
		}
		if result.succeeded {
			delivered++
			slog.Info("forwarded alertmanager notification to webhook", append(targetFields, "result", result.result, "status_code", result.statusCode, "duration_ms", result.duration.Milliseconds())...)
			continue
		}

		allSucceeded = false
		s.metrics.requests.WithLabelValues(endpoint, result.result).Inc()
		if result.err != nil {
			slog.Error("webhook delivery failed", append(targetFields, "result", result.result, "status_code", result.statusCode, "response_body", result.responseBody, "duration_ms", result.duration.Milliseconds(), "error", result.err)...)
			continue
		}
		slog.Error("webhook rejected message", append(targetFields, "result", result.result, "status_code", result.statusCode, "response_body", result.responseBody, "duration_ms", result.duration.Milliseconds())...)
	}

	if !allSucceeded {
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

func mustNewDedupe(size int) *lru.Cache[string, struct{}] {
	cache, err := lru.New[string, struct{}](size)
	if err != nil {
		panic(err)
	}
	return cache
}
