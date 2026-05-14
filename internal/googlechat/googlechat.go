package googlechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	amwebhook "github.com/prometheus/alertmanager/notify/webhook"
	amtemplate "github.com/prometheus/alertmanager/template"
)

// Enforced by google chat
const maxMessageBytes = 32000

const alertSeparator = "-----------------------------------------------------------------------------------------------"

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
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("google chat webhook url must use http or https")
	}

	return &Sender{
		client:     &http.Client{Timeout: timeout},
		webhookURL: webhookURL,
	}, nil
}

func Render(payload amwebhook.Message, sendResolved bool) Message {
	lines := []string{"*" + renderTitle(payload) + "*"}

	bodyLines := renderAlertLines(payload.Alerts, sendResolved)
	if len(bodyLines) > 0 {
		lines = append(lines, "", "")
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

func renderAlertLines(alerts amtemplate.Alerts, sendResolved bool) []string {
	ordered := append([]amtemplate.Alert(nil), alerts.Firing()...)
	if sendResolved {
		ordered = append(ordered, alerts.Resolved()...)
	}
	if len(ordered) < len(alerts) {
		for _, alert := range alerts {
			if alert.Status != "firing" && alert.Status != "resolved" {
				ordered = append(ordered, alert)
			}
		}
	}

	lines := make([]string, 0, len(ordered))
	for _, alert := range ordered {
		if len(lines) > 0 {
			lines = append(lines, "", alertSeparator, "")
		}
		lines = append(lines, renderAlertBlock(alert)...)
	}
	return lines
}

func renderAlertBlock(alert amtemplate.Alert) []string {
	summary := strings.TrimSpace(alert.Annotations["summary"])
	description := strings.TrimSpace(alert.Annotations["description"])
	runbook := strings.TrimSpace(alert.Annotations["runbook_url"])
	status := strings.ToUpper(firstNonEmpty(alert.Status, "unknown"))
	switch strings.ToLower(strings.TrimSpace(alert.Status)) {
	case "firing":
		status = "🔥 " + status
	case "resolved":
		status = "✅ " + status
	}

	header := []string{"Status: " + status}
	if !alert.StartsAt.IsZero() {
		header = append(header, "Started At: "+alert.StartsAt.Format(time.RFC3339))
	}

	lines := renderCodeBlock(strings.Join(header, "\n"))
	if runbook != "" {
		lines = append(lines, "", "Runbook: <"+runbook+"|"+runbook+">")
	}
	appendSection := func(heading, value string) {
		lines = append(lines, "", heading+":")
		lines = append(lines, renderCodeBlock(value)...)
	}
	appendSection("Summary", summary)
	appendSection("Description", description)
	appendSection("Labels", renderLabels(alert.Labels))

	return lines
}

func renderTitle(payload amwebhook.Message) string {
	parts := make([]string, 0, 5)
	if cluster := firstNonEmpty(payload.CommonLabels["cluster"], payload.GroupLabels["cluster"]); cluster != "" {
		clusterUID := firstNonEmpty(payload.CommonLabels["cluster_uid"], payload.GroupLabels["cluster_uid"])
		if len(clusterUID) > 8 {
			clusterUID = clusterUID[:8]
		}
		if clusterUID != "" {
			parts = append(parts, fmt.Sprintf("[%s/%s]", cluster, clusterUID))
		} else {
			parts = append(parts, fmt.Sprintf("[%s]", cluster))
		}
	}

	counts := make([]string, 0, 2)
	if firing := len(payload.Alerts.Firing()); firing > 0 {
		counts = append(counts, fmt.Sprintf("FIRING:%d", firing))
	}
	if resolved := len(payload.Alerts.Resolved()); resolved > 0 {
		counts = append(counts, fmt.Sprintf("RESOLVED:%d", resolved))
	}
	if len(counts) > 0 {
		parts = append(parts, "["+strings.Join(counts, " | ")+"]")
	}

	suffix := alertName(payload)
	if kind := firstNonEmpty(payload.GroupLabels["k8s_kind"], payload.CommonLabels["k8s_kind"]); kind != "" {
		suffix += " - " + kind
	}
	resourceName := firstNonEmpty(payload.GroupLabels["k8s_resource"], payload.CommonLabels["k8s_resource"])
	if resourceName != "" {
		namespace := firstNonEmpty(payload.GroupLabels["namespace"], payload.CommonLabels["namespace"], payload.CommonLabels["app_namespace"])
		if namespace != "" {
			resourceName = namespace + "/" + resourceName
		}
		suffix += " - " + resourceName
	}
	if job := firstNonEmpty(payload.GroupLabels["job"], payload.CommonLabels["job"]); job != "" {
		suffix += " [Via " + job + "]"
	}
	parts = append(parts, suffix)

	return strings.Join(parts, " ")
}

func alertName(payload amwebhook.Message) string {
	if name := strings.TrimSpace(payload.GroupLabels["alertname"]); name != "" {
		return name
	}
	if name := strings.TrimSpace(payload.CommonLabels["alertname"]); name != "" {
		return name
	}
	for _, alert := range payload.Alerts {
		if name := strings.TrimSpace(alert.Labels["alertname"]); name != "" {
			return name
		}
	}
	return "Alertmanager Notification"
}

func renderLabels(labels amtemplate.KV) string {
	pairs := labels.SortedPairs()
	lines := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		lines = append(lines, pair.Name+": "+pair.Value)
	}
	return strings.Join(lines, "\n")
}

func renderCodeBlock(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		trimmed = "-"
	}
	parts := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(parts)+2)
	lines = append(lines, "```")
	lines = append(lines, parts...)
	lines = append(lines, "```")
	return lines
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
	if len(text) <= limit {
		return text
	}
	marker := "\n... truncated ..."
	trimmed := text
	for len(trimmed)+len(marker) > limit && trimmed != "" {
		_, size := utf8.DecodeLastRuneInString(trimmed)
		trimmed = trimmed[:len(trimmed)-size]
	}
	return strings.TrimRight(trimmed, "\n") + marker
}
