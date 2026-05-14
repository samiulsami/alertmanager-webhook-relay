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

	return &Sender{
		client:     &http.Client{Timeout: timeout},
		webhookURL: webhookURL,
	}, nil
}

func Render(payload alertmanager.Webhook, sendResolved bool) Message {
	var lines []string
	lines = append(lines, "*"+renderTitle(payload)+"*")

	bodyLines := renderAlertLines(payload, sendResolved)
	if len(bodyLines) > 0 {
		lines = append(lines, "")
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
	alerts := orderedAlerts(payload.Alerts)
	lines := make([]string, 0, len(alerts))
	printed := 0
	for _, alert := range alerts {
		if alert.Status == "resolved" && !sendResolved {
			continue
		}
		if printed > 0 {
			lines = append(lines, "")
			lines = append(lines, alertSeparator)
			lines = append(lines, "")
		}
		lines = append(lines, renderAlertBlock(alert)...)
		printed++
	}
	return lines
}

func orderedAlerts(alerts []alertmanager.Alert) []alertmanager.Alert {
	ordered := append([]alertmanager.Alert(nil), alerts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := alertStatusOrder(ordered[i].Status)
		right := alertStatusOrder(ordered[j].Status)
		if left != right {
			return left < right
		}
		return false
	})
	return ordered
}

func alertStatusOrder(status string) int {
	if strings.EqualFold(status, "firing") {
		return 0
	}
	if strings.EqualFold(status, "resolved") {
		return 1
	}
	return 2
}

func renderAlertBlock(alert alertmanager.Alert) []string {
	summary := strings.TrimSpace(alert.Annotations["summary"])
	description := strings.TrimSpace(alert.Annotations["description"])

	header := []string{"Status: " + strings.ToUpper(firstNonEmpty(alert.Status, "unknown"))}
	if runbook := strings.TrimSpace(alert.Annotations["runbook_url"]); runbook != "" {
		header = append(header, "Runbook: "+runbook)
	}
	if !alert.StartsAt.IsZero() {
		header = append(header, "Started At: "+alert.StartsAt.Format(time.RFC3339))
	}

	lines := renderCodeBlock(strings.Join(header, "\n"))

	lines = append(lines, "")
	lines = append(lines, "Labels:")
	lines = append(lines, renderCodeBlock(renderLabels(alert.Labels))...)
	lines = append(lines, "")
	lines = append(lines, "Summary:")
	lines = append(lines, renderCodeBlock(summary)...)
	lines = append(lines, "")
	lines = append(lines, "Description:")
	lines = append(lines, renderCodeBlock(description)...)

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

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+": "+labels[key])
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

func shortValue(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
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
