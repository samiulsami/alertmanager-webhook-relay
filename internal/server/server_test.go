package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.openviz.dev/alertmanager-relay/internal/config"
	"go.openviz.dev/alertmanager-relay/internal/webhook"
)

type fakeSender struct {
	name       string
	key        string
	statusCode int
	response   string
	err        error
	message    webhook.Message
	called     int
}

func (f *fakeSender) Name() string {
	if f.name == "" {
		return "test-target"
	}
	return f.name
}

func (f *fakeSender) Key() string {
	if f.key == "" {
		return f.Name()
	}
	return f.key
}

func (f *fakeSender) Send(_ context.Context, message webhook.Message) (int, string, error) {
	f.called++
	f.message = message
	return f.statusCode, f.response, f.err
}

func TestNewRejectsInvalidWebhookURL(t *testing.T) {
	handler, err := New(config.Config{WebhookURLs: []string{"ftp://example.com/webhook"}, RequestTimeout: time.Second, DedupeCacheSize: 16})
	if err == nil {
		t.Fatal("expected invalid webhook url to return an error")
	}
	if handler != nil {
		t.Fatal("expected handler to be nil when configuration is invalid")
	}
}

func TestWebhookHandlerSuccess(t *testing.T) {
	target := &fakeSender{statusCode: http.StatusOK}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if !strings.Contains(target.message.Text, "HighLatency") {
		t.Fatalf("expected alert content to be forwarded, got %q", target.message.Text)
	}
}

func TestWebhookHandlerRejectsInvalidPayload(t *testing.T) {
	target := &fakeSender{}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(`{"foo":"bar"}`))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
}

func TestWebhookHandlerRejectsOversizedBody(t *testing.T) {
	target := &fakeSender{}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16, MaxRequestBodyBytes: 32}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.Code)
	}
	if target.called != 0 {
		t.Fatal("expected oversized payload to be rejected before delivery")
	}
}

func TestWebhookHandlerUsesDefaultBodyLimit(t *testing.T) {
	target := &fakeSender{}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{target})
	body := strings.Repeat("x", int(config.DefaultMaxRequestBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(body))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 from default limit, got %d", resp.Code)
	}
	if target.called != 0 {
		t.Fatal("expected oversized payload to be rejected before delivery")
	}
}

func TestWebhookHandlerPropagatesUpstreamFailure(t *testing.T) {
	target := &fakeSender{statusCode: http.StatusTooManyRequests, response: `rate limited`}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

func TestWebhookHandlerSkipsResolvedNotificationsWhenDisabled(t *testing.T) {
	target := &fakeSender{}
	handler := newHandler(config.Config{SendResolved: false, DedupeCacheSize: 16}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(resolvedPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if target.called != 0 {
		t.Fatal("expected resolved-only payload to be skipped")
	}
}

func TestWebhookHandlerMapsTimeoutToBadGateway(t *testing.T) {
	target := &fakeSender{err: context.DeadlineExceeded}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

func TestFakeSenderErrorClassificationDoesNotPanic(t *testing.T) {
	target := &fakeSender{err: errors.New("boom")}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

func TestWebhookHandlerDedupesSuccessfulTargetOnRetry(t *testing.T) {
	first := &fakeSender{name: "first", key: "https://example.com/first", statusCode: http.StatusOK}
	second := &fakeSender{name: "second", key: "https://example.com/second", statusCode: http.StatusBadGateway, response: "boom"}
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16}, []sender{first, second})

	resp1 := httptest.NewRecorder()
	handler.ServeHTTP(resp1, httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload)))
	if resp1.Code != http.StatusBadGateway {
		t.Fatalf("expected first attempt to fail, got %d", resp1.Code)
	}
	if first.called != 1 || second.called != 1 {
		t.Fatalf("expected both targets attempted once, got first=%d second=%d", first.called, second.called)
	}

	second.statusCode = http.StatusOK
	resp2 := httptest.NewRecorder()
	handler.ServeHTTP(resp2, httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(validPayload)))
	if resp2.Code != http.StatusOK {
		t.Fatalf("expected retry to succeed, got %d", resp2.Code)
	}
	if first.called != 1 {
		t.Fatalf("expected successful first target to be deduped, got %d calls", first.called)
	}
	if second.called != 2 {
		t.Fatalf("expected failed target to retry, got %d calls", second.called)
	}
}

func TestWebhookHandlerAcceptsBodyAtConfiguredLimit(t *testing.T) {
	target := &fakeSender{statusCode: http.StatusOK}
	body := fmt.Sprintf("{" +
		"\"version\":\"4\"," +
		"\"groupKey\":\"g\"," +
		"\"status\":\"firing\"," +
		"\"receiver\":\"webhook\"," +
		"\"alerts\":[{" +
		"\"status\":\"firing\"," +
		"\"labels\":{}," +
		"\"annotations\":{}" +
		"}]}")
	handler := newHandler(config.Config{SendResolved: true, DedupeCacheSize: 16, MaxRequestBodyBytes: int64(len(body))}, []sender{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/webhook", strings.NewReader(body))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if target.called != 1 {
		t.Fatalf("expected one delivery, got %d", target.called)
	}
}

var validPayload = `{
	"version":"4",
	"groupKey":"{}:{alertname=\"HighLatency\"}",
	"status":"firing",
	"receiver":"webhook",
	"groupLabels":{"alertname":"HighLatency"},
	"commonLabels":{"alertname":"HighLatency","namespace":"prod","severity":"critical"},
	"commonAnnotations":{"summary":"API latency is above threshold"},
	"externalURL":"https://alertmanager.example.com",
	"alerts":[
		{
			"status":"firing",
			"labels":{"alertname":"HighLatency","instance":"api-1","severity":"critical"},
			"annotations":{"summary":"API latency is above threshold","description":"P99 latency exceeded 1.2s for 10m"},
			"startsAt":"2026-05-11T12:00:00Z",
			"endsAt":"0001-01-01T00:00:00Z",
			"generatorURL":"https://prometheus.example.com/graph?g0.expr=latency",
			"fingerprint":"abc123"
		}
	]
}`

var resolvedPayload = `{
	"version":"4",
	"groupKey":"{}:{alertname=\"HighLatency\"}",
	"status":"resolved",
	"receiver":"webhook",
	"groupLabels":{"alertname":"HighLatency"},
	"commonLabels":{"alertname":"HighLatency","namespace":"prod","severity":"critical"},
	"commonAnnotations":{"summary":"API latency is back to normal"},
	"externalURL":"https://alertmanager.example.com",
	"alerts":[
		{
			"status":"resolved",
			"labels":{"alertname":"HighLatency","instance":"api-1","severity":"critical"},
			"annotations":{"summary":"API latency is back to normal","description":"P99 latency recovered for 10m"},
			"startsAt":"2026-05-11T12:00:00Z",
			"endsAt":"2026-05-11T12:15:00Z",
			"generatorURL":"https://prometheus.example.com/graph?g0.expr=latency",
			"fingerprint":"abc123"
		}
	]
}`
