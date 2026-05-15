package webhook

import (
	"strings"
	"testing"
	"time"

	amwebhook "github.com/prometheus/alertmanager/notify/webhook"
	amtemplate "github.com/prometheus/alertmanager/template"
)

func testWebhook(data amtemplate.Data) amwebhook.Message {
	return amwebhook.Message{Data: &data}
}

func TestNewSenderAcceptsUppercaseScheme(t *testing.T) {
	sender, err := NewSender("HTTPS://chat.googleapis.com/v1/spaces/test/messages?key=x&token=y", time.Second)
	if err != nil {
		t.Fatalf("expected uppercase scheme to be accepted, got %v", err)
	}
	if sender == nil {
		t.Fatal("expected sender")
	}
}

func TestRenderIncludesCoreFields(t *testing.T) {
	payload := testWebhook(amtemplate.Data{Status: "firing", GroupLabels: amtemplate.KV{"alertname": "HighLatency"}, CommonLabels: amtemplate.KV{"namespace": "prod", "severity": "critical"}, Alerts: amtemplate.Alerts{{Status: "firing", Labels: amtemplate.KV{"alertname": "HighLatency", "namespace": "prod"}, Annotations: amtemplate.KV{"summary": "API latency is above threshold"}}, {Status: "firing", Labels: amtemplate.KV{"alertname": "HighLatency", "namespace": "prod"}, Annotations: amtemplate.KV{"summary": "P99 latency exceeded 1.2s for 10m"}}}})
	message := Render(payload, true)
	checks := []string{"*[FIRING:2] HighLatency*", "```\nStatus: 🔥 FIRING\n```", "Labels:", "Summary:", "Description:", "alertname: HighLatency"}
	for _, check := range checks {
		if !strings.Contains(message.Text, check) {
			t.Fatalf("expected rendered message to contain %q, got %q", check, message.Text)
		}
	}
}
