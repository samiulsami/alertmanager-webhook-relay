package googlechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.openviz.dev/alertmanager-relay/internal/alertmanager"
)

const maxMessageBytes = 32000

type Message struct {
	Text string `json:"text"`
}

type Sender struct {
	client     *http.Client
	webhookURL string
}

func NewSender(webhookURL string, timeout time.Duration) (*Sender, error) {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		return nil, fmt.Errorf("parse google chat webhook url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("google chat webhook url must be an absolute url")
	}

	return &Sender{
		client:     &http.Client{Timeout: timeout},
		webhookURL: webhookURL,
	}, nil
}

func Render(payload alertmanager.Webhook, sendResolved bool) Message {
	var lines []string
	lines = append(lines, "*"+renderTitle(payload)+"*")
	lines = append(lines, renderMetaLines(payload)...)

	bodyLines := renderAlertLines(payload, sendResolved)
	if len(bodyLines) > 0 {
		lines = append(lines, bodyLines...)
	}

	text := strings.Join(lines, "\n")
	return Message{Text: truncate(text, maxMessageBytes)}
}

func (s *Sender) Send(ctx context.Context, message Message) (int, string, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return 0, "", fmt.Errorf("marshal google chat message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("post to google chat: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read upstream response: %w", err)
	}

	return resp.StatusCode, strings.TrimSpace(string(respBody)), nil
}

func renderAlertLines(payload alertmanager.Webhook, sendResolved bool) []string {
	lines := make([]string, 0, len(payload.Alerts))
	for _, alert := range payload.Alerts {
		if alert.Status == "resolved" && !sendResolved {
			continue
		}
		summary := strings.TrimSpace(alert.Annotations["summary"])
		description := strings.TrimSpace(alert.Annotations["description"])
		title := firstNonEmpty(summary, alert.Labels["alertname"], alert.Fingerprint, "alert")
		lines = append(lines, fmt.Sprintf("- *%s*", title))

		lines = append(lines, renderAlertMetaLines(alert)...)

		if summary != "" {
			lines = append(lines, "  - `Summary:`")
			lines = append(lines, renderTextLines(summary, "    ")...)
		}

		if description != "" && description != summary {
			lines = append(lines, "  - `Description:`")
			lines = append(lines, renderTextLines(description, "    ")...)
		}

		if labelLines := renderLabelLines(alert.Labels); len(labelLines) > 0 {
			lines = append(lines, "  - `Labels:`")
			lines = append(lines, labelLines...)
		}

		if runbook := strings.TrimSpace(alert.Annotations["runbook_url"]); runbook != "" {
			lines = append(lines, "  - `Runbook:` "+runbook)
		}
		if source := strings.TrimSpace(alert.GeneratorURL); source != "" {
			lines = append(lines, "  - `Source:` "+source)
		}
	}
	return lines
}

func renderTitle(payload alertmanager.Webhook) string {
	parts := make([]string, 0, 5)
	if cluster := firstNonEmpty(payload.CommonLabels["cluster"], payload.GroupLabels["cluster"]); cluster != "" {
		if clusterUID := shortValue(firstNonEmpty(payload.CommonLabels["cluster_uid"], payload.GroupLabels["cluster_uid"]), 8); clusterUID != "" {
			parts = append(parts, fmt.Sprintf("[%s/%s]", cluster, clusterUID))
		} else {
			parts = append(parts, fmt.Sprintf("[%s]", cluster))
		}
	}

	counts := make([]string, 0, 2)
	if firing := payload.CountByStatus("firing"); firing > 0 {
		counts = append(counts, fmt.Sprintf("FIRING:%d", firing))
	}
	if resolved := payload.CountByStatus("resolved"); resolved > 0 {
		counts = append(counts, fmt.Sprintf("RESOLVED:%d", resolved))
	}
	if len(counts) > 0 {
		parts = append(parts, "["+strings.Join(counts, " | ")+"]")
	}

	suffix := payload.AlertName()
	if kind := firstNonEmpty(payload.GroupLabels["k8s_kind"], payload.CommonLabels["k8s_kind"]); kind != "" {
		suffix += " - " + kind
	}
	if resource := renderResource(payload); resource != "" {
		suffix += " - " + resource
	}
	if job := firstNonEmpty(payload.GroupLabels["job"], payload.CommonLabels["job"]); job != "" {
		suffix += " [Via " + job + "]"
	}
	parts = append(parts, suffix)

	return strings.Join(parts, " ")
}

func renderMetaLines(payload alertmanager.Webhook) []string {
	lines := make([]string, 0, 4)
	lines = append(lines, "- `Status:` "+strings.ToUpper(payload.Status))
	if severity := firstNonEmpty(payload.CommonLabels["severity"], firstAlertLabel(payload, "severity")); severity != "" {
		lines = append(lines, "- `Severity:` "+severity)
	}
	if namespace := firstNonEmpty(payload.CommonLabels["namespace"], payload.GroupLabels["namespace"], payload.CommonLabels["app_namespace"], firstAlertLabel(payload, "namespace")); namespace != "" {
		lines = append(lines, "- `Namespace:` "+namespace)
	}
	if job := firstNonEmpty(payload.CommonLabels["job"], firstAlertLabel(payload, "job")); job != "" {
		lines = append(lines, "- `Job:` "+job)
	}
	return lines
}

func renderAlertMetaLines(alert alertmanager.Alert) []string {
	lines := make([]string, 0, 6)
	lines = append(lines, "  - `Status:` "+strings.ToUpper(firstNonEmpty(alert.Status, "unknown")))
	if !alert.StartsAt.IsZero() {
		lines = append(lines, "  - `Started At:` "+alert.StartsAt.Format(time.RFC3339))
	}
	for _, key := range []string{"pod", "instance", "service", "container", "job"} {
		if value := strings.TrimSpace(alert.Labels[key]); value != "" {
			lines = append(lines, "  - `"+displayKey(key)+":` "+value)
		}
	}
	return lines
}

func renderResource(payload alertmanager.Webhook) string {
	resourceName := firstNonEmpty(payload.GroupLabels["k8s_resource"], payload.CommonLabels["k8s_resource"])
	namespace := firstNonEmpty(payload.GroupLabels["namespace"], payload.CommonLabels["namespace"], payload.CommonLabels["app_namespace"])
	if resourceName == "" {
		return ""
	}
	if namespace != "" {
		return namespace + "/" + resourceName
	}
	return resourceName
}

func renderLabelLines(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, "    - `"+key+":` "+labels[key])
	}
	return lines
}

func renderTextLines(value, indent string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimRight(part, " \t")
		if strings.TrimSpace(part) == "" {
			lines = append(lines, indent)
			continue
		}
		lines = append(lines, indent+part)
	}
	return lines
}

func displayKey(key string) string {
	parts := strings.Split(key, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func shortValue(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func firstAlertLabel(payload alertmanager.Webhook, key string) string {
	for _, alert := range payload.Alerts {
		if value := strings.TrimSpace(alert.Labels[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncate(text string, limit int) string {
	if len([]byte(text)) <= limit {
		return text
	}
	marker := "\n... truncated ..."
	trimmed := text
	for len([]byte(trimmed+marker)) > limit && trimmed != "" {
		_, size := utf8.DecodeLastRuneInString(trimmed)
		trimmed = trimmed[:len(trimmed)-size]
	}
	return strings.TrimRight(trimmed, "\n") + marker
}
