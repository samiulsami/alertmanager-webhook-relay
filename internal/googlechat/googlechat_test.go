package googlechat

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
	payload := testWebhook(amtemplate.Data{
		Status:       "firing",
		GroupLabels:  amtemplate.KV{"alertname": "HighLatency"},
		CommonLabels: amtemplate.KV{"namespace": "prod", "severity": "critical"},
		Alerts: amtemplate.Alerts{
			{Status: "firing", Labels: amtemplate.KV{"alertname": "HighLatency", "namespace": "prod"}, Annotations: amtemplate.KV{"summary": "API latency is above threshold"}},
			{Status: "firing", Labels: amtemplate.KV{"alertname": "HighLatency", "namespace": "prod"}, Annotations: amtemplate.KV{"summary": "P99 latency exceeded 1.2s for 10m"}},
		},
	})

	message := Render(payload, true)

	checks := []string{
		"*[FIRING:2] HighLatency*",
		"```\nStatus: FIRING\n```",
		"Labels:",
		"Summary:",
		"Description:",
		"alertname: HighLatency",
	}
	for _, check := range checks {
		if !strings.Contains(message.Text, check) {
			t.Fatalf("expected rendered message to contain %q, got %q", check, message.Text)
		}
	}
}

func TestRenderIncludesResolvedBulletLines(t *testing.T) {
	payload := testWebhook(amtemplate.Data{
		Status:      "firing",
		GroupLabels: amtemplate.KV{"alertname": "DiskFull"},
		Alerts: amtemplate.Alerts{
			{Status: "firing", Annotations: amtemplate.KV{"summary": "Node disk usage is high"}},
			{Status: "resolved", Annotations: amtemplate.KV{"summary": "Old alert should be hidden"}},
		},
	})

	message := Render(payload, true)

	if !strings.Contains(message.Text, "Old alert should be hidden") {
		t.Fatalf("expected resolved alert bullet to be included, got %q", message.Text)
	}
	if !strings.Contains(message.Text, "[FIRING:1 | RESOLVED:1]") {
		t.Fatalf("expected counts to remain visible, got %q", message.Text)
	}
	if !strings.Contains(message.Text, alertSeparator) {
		t.Fatalf("expected separator between alerts, got %q", message.Text)
	}
}

func TestRenderUsesRequestedCodeBlockSections(t *testing.T) {
	payload := testWebhook(amtemplate.Data{
		Status:      "firing",
		GroupLabels: amtemplate.KV{"alertname": "DiskFull"},
		Alerts: amtemplate.Alerts{{
			Status: "firing",
			Annotations: amtemplate.KV{
				"summary":     "Node disk usage is high",
				"description": "Disk usage exceeded threshold for 15m",
			},
		}},
	})

	message := Render(payload, true)

	if !strings.Contains(message.Text, "```\nDisk usage exceeded threshold for 15m\n```") {
		t.Fatalf("expected description to be rendered as code block, got %q", message.Text)
	}
	if !strings.Contains(message.Text, "Description:") {
		t.Fatalf("expected description heading, got %q", message.Text)
	}
	if !strings.Contains(message.Text, "Summary:") {
		t.Fatalf("expected summary heading, got %q", message.Text)
	}
}

func TestRenderOrdersFiringBeforeResolved(t *testing.T) {
	payload := testWebhook(amtemplate.Data{
		Status:      "firing",
		GroupLabels: amtemplate.KV{"alertname": "DiskFull"},
		Alerts: amtemplate.Alerts{
			{Status: "resolved", Labels: amtemplate.KV{"alertname": "ResolvedAlert"}},
			{Status: "firing", Labels: amtemplate.KV{"alertname": "FiringAlert"}},
		},
	})

	message := Render(payload, true)

	if strings.Index(message.Text, "Status: FIRING") > strings.Index(message.Text, "Status: RESOLVED") {
		t.Fatalf("expected firing alerts before resolved alerts, got %q", message.Text)
	}
}

func TestRenderCanOmitResolvedBulletLines(t *testing.T) {
	payload := testWebhook(amtemplate.Data{
		Status:      "firing",
		GroupLabels: amtemplate.KV{"alertname": "DiskFull"},
		Alerts: amtemplate.Alerts{
			{Status: "firing", Annotations: amtemplate.KV{"summary": "Node disk usage is high"}},
			{Status: "resolved", Annotations: amtemplate.KV{"summary": "Old alert should be hidden"}},
		},
	})

	message := Render(payload, false)

	if strings.Contains(message.Text, "Old alert should be hidden") {
		t.Fatalf("expected resolved alert bullet to be omitted, got %q", message.Text)
	}
	if !strings.Contains(message.Text, "[FIRING:1 | RESOLVED:1]") {
		t.Fatalf("expected counts to remain visible, got %q", message.Text)
	}
}

func TestRenderTruncatesLargeMessages(t *testing.T) {
	payload := testWebhook(amtemplate.Data{
		Status:      "firing",
		GroupLabels: amtemplate.KV{"alertname": "HugeAlert"},
		Alerts: amtemplate.Alerts{{
			Status:      "firing",
			Annotations: amtemplate.KV{"summary": strings.Repeat("x", 40000)},
		}},
	})

	message := Render(payload, true)

	if len([]byte(message.Text)) > maxMessageBytes {
		t.Fatalf("expected message to be truncated below limit, got %d bytes", len([]byte(message.Text)))
	}
	if !strings.Contains(message.Text, "... truncated ...") {
		t.Fatalf("expected truncation marker, got %q", message.Text)
	}
}
