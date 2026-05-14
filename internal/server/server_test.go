package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.openviz.dev/alertmanager-relay/internal/config"
	"go.openviz.dev/alertmanager-relay/internal/googlechat"
)

type fakeSender struct {
	statusCode int
	response   string
	err        error
	message    googlechat.Message
}

func (f *fakeSender) Send(_ context.Context, message googlechat.Message) (int, string, error) {
	f.message = message
	return f.statusCode, f.response, f.err
}

func TestGoogleChatHandlerSuccess(t *testing.T) {
	sender := &fakeSender{statusCode: http.StatusOK}
	handler := newHandler(config.Config{SendResolved: true}, sender)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/google-chat", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if !strings.Contains(sender.message.Text, "HighLatency") {
		t.Fatalf("expected alert content to be forwarded, got %q", sender.message.Text)
	}
}

func TestGoogleChatHandlerRejectsInvalidPayload(t *testing.T) {
	sender := &fakeSender{}
	handler := newHandler(config.Config{SendResolved: true}, sender)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/google-chat", strings.NewReader(`{"foo":"bar"}`))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
}

func TestGoogleChatHandlerPropagatesUpstreamFailure(t *testing.T) {
	sender := &fakeSender{statusCode: http.StatusTooManyRequests, response: `rate limited`}
	handler := newHandler(config.Config{SendResolved: true}, sender)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/google-chat", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

func TestGoogleChatHandlerMapsTimeoutToBadGateway(t *testing.T) {
	sender := &fakeSender{err: context.DeadlineExceeded}
	handler := newHandler(config.Config{SendResolved: true}, sender)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/google-chat", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

func TestFakeSenderErrorClassificationDoesNotPanic(t *testing.T) {
	sender := &fakeSender{err: errors.New("boom")}
	handler := newHandler(config.Config{SendResolved: true}, sender)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/google-chat", strings.NewReader(validPayload))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

var validPayload = `{
	"version":"4",
	"groupKey":"{}:{alertname=\"HighLatency\"}",
	"status":"firing",
	"receiver":"google-chat",
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
