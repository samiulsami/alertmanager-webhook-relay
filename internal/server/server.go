package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.openviz.dev/alertmanager-relay/internal/alertmanager"
	"go.openviz.dev/alertmanager-relay/internal/config"
	"go.openviz.dev/alertmanager-relay/internal/googlechat"
)

type relayServer struct {
	config  config.Config
	sender  sender
	metrics *metrics
}

type sender interface {
	Send(ctx context.Context, message googlechat.Message) (int, string, error)
}

type metrics struct {
	requests        *prometheus.CounterVec
	upstreamLatency prometheus.Histogram
}

func New(cfg config.Config) (http.Handler, error) {
	sender, err := googlechat.NewSender(cfg.GoogleChatURL, cfg.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("create google chat sender: %w", err)
	}
	return newHandler(cfg, sender), nil
}

func newHandler(cfg config.Config, sender sender) http.Handler {
	registry := prometheus.NewRegistry()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "alertmanager_relay_requests_total",
		Help: "Total relay requests by endpoint outcome.",
	}, []string{"endpoint", "result"})
	upstreamLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "alertmanager_relay_google_chat_request_duration_seconds",
		Help:    "Latency of Google Chat webhook requests.",
		Buckets: prometheus.DefBuckets,
	})
	registry.MustRegister(requests, upstreamLatency)

	server := &relayServer{
		config: cfg,
		sender: sender,
		metrics: &metrics{
			requests:        requests,
			upstreamLatency: upstreamLatency,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/v1/ingest/google-chat", server.handleGoogleChat)
	return mux
}

func (s *relayServer) handleGoogleChat(w http.ResponseWriter, r *http.Request) {
	const endpoint = "google_chat"
	if r.Method != http.MethodPost {
		s.metrics.requests.WithLabelValues(endpoint, "invalid_method").Inc()
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		slog.Warn("rejected google chat relay request", "endpoint", endpoint, "result", "invalid_method", "method", r.Method)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.metrics.requests.WithLabelValues(endpoint, "invalid_body").Inc()
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		slog.Warn("rejected google chat relay request", "endpoint", endpoint, "result", "invalid_body", "error", err)
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

	if !s.config.SendResolved && len(payload.Alerts.Resolved()) == len(payload.Alerts) {
		s.metrics.requests.WithLabelValues(endpoint, "skipped_resolved").Inc()
		w.WriteHeader(http.StatusOK)
		slog.Info("skipped resolved-only alertmanager notification", append(logFields, "result", "skipped_resolved")...)
		return
	}

	message := googlechat.Render(payload, s.config.SendResolved)

	start := time.Now()
	statusCode, responseBody, err := s.sender.Send(r.Context(), message)
	duration := time.Since(start)
	s.metrics.upstreamLatency.Observe(duration.Seconds())
	if err != nil {
		result := "upstream_error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			result = "upstream_timeout"
		}
		s.metrics.requests.WithLabelValues(endpoint, result).Inc()
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		slog.Error("google chat delivery failed", append(logFields, "result", result, "status_code", statusCode, "response_body", responseBody, "duration_ms", duration.Milliseconds(), "error", err)...)
		return
	}

	if statusCode < 200 || statusCode >= 300 {
		result := "upstream_non_2xx"
		if statusCode == http.StatusTooManyRequests {
			result = "upstream_rate_limited"
		}
		s.metrics.requests.WithLabelValues(endpoint, result).Inc()
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		slog.Error("google chat rejected message", append(logFields, "result", result, "status_code", statusCode, "response_body", responseBody, "duration_ms", duration.Milliseconds())...)
		return
	}

	s.metrics.requests.WithLabelValues(endpoint, "success").Inc()
	w.WriteHeader(http.StatusOK)
	slog.Info("forwarded alertmanager notification to google chat", append(logFields, "result", "success", "status_code", statusCode, "duration_ms", duration.Milliseconds())...)
}
