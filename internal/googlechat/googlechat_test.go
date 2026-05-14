package googlechat

import (
	"strings"
	"testing"

	"go.openviz.dev/alertmanager-relay/internal/alertmanager"
)

func TestRenderIncludesCoreFields(t *testing.T) {
	payload := alertmanager.Webhook{
		Status:       "firing",
		GroupLabels:  map[string]string{"alertname": "HighLatency"},
		CommonLabels: map[string]string{"namespace": "prod", "severity": "critical"},
		Alerts: []alertmanager.Alert{
			{Status: "firing", Labels: map[string]string{"alertname": "HighLatency", "namespace": "prod"}, Annotations: map[string]string{"summary": "API latency is above threshold"}},
			{Status: "firing", Labels: map[string]string{"alertname": "HighLatency", "namespace": "prod"}, Annotations: map[string]string{"summary": "P99 latency exceeded 1.2s for 10m"}},
		},
	}

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
	payload := alertmanager.Webhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "DiskFull"},
		Alerts: []alertmanager.Alert{
			{Status: "firing", Annotations: map[string]string{"summary": "Node disk usage is high"}},
			{Status: "resolved", Annotations: map[string]string{"summary": "Old alert should be hidden"}},
		},
	}

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
	payload := alertmanager.Webhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "DiskFull"},
		Alerts: []alertmanager.Alert{{
			Status: "firing",
			Annotations: map[string]string{
				"summary":     "Node disk usage is high",
				"description": "Disk usage exceeded threshold for 15m",
			},
		}},
	}

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
	payload := alertmanager.Webhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "DiskFull"},
		Alerts: []alertmanager.Alert{
			{Status: "resolved", Labels: map[string]string{"alertname": "ResolvedAlert"}},
			{Status: "firing", Labels: map[string]string{"alertname": "FiringAlert"}},
		},
	}

	message := Render(payload, true)

	if strings.Index(message.Text, "Status: FIRING") > strings.Index(message.Text, "Status: RESOLVED") {
		t.Fatalf("expected firing alerts before resolved alerts, got %q", message.Text)
	}
}

func TestRenderCanOmitResolvedBulletLines(t *testing.T) {
	payload := alertmanager.Webhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "DiskFull"},
		Alerts: []alertmanager.Alert{
			{Status: "firing", Annotations: map[string]string{"summary": "Node disk usage is high"}},
			{Status: "resolved", Annotations: map[string]string{"summary": "Old alert should be hidden"}},
		},
	}

	message := Render(payload, false)

	if strings.Contains(message.Text, "Old alert should be hidden") {
		t.Fatalf("expected resolved alert bullet to be omitted, got %q", message.Text)
	}
	if !strings.Contains(message.Text, "[FIRING:1 | RESOLVED:1]") {
		t.Fatalf("expected counts to remain visible, got %q", message.Text)
	}
}

func TestRenderTruncatesLargeMessages(t *testing.T) {
	payload := alertmanager.Webhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "HugeAlert"},
		Alerts: []alertmanager.Alert{{
			Status:      "firing",
			Annotations: map[string]string{"summary": strings.Repeat("x", 40000)},
		}},
	}

	message := Render(payload, true)

	if len([]byte(message.Text)) > maxMessageBytes {
		t.Fatalf("expected message to be truncated below limit, got %d bytes", len([]byte(message.Text)))
	}
	if !strings.Contains(message.Text, "... truncated ...") {
		t.Fatalf("expected truncation marker, got %q", message.Text)
	}
}
