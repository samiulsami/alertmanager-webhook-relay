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
	switch parsed.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("google chat webhook url must use http or https")
	}

	return &Sender{
		client:     &http.Client{Timeout: timeout},
		webhookURL: webhookURL,
	}, nil
}

func Render(payload alertmanager.Webhook, sendResolved bool) Message {
	lines := []string{"*" + renderTitle(payload) + "*"}

	bodyLines := renderAlertLines(payload, sendResolved)
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

func renderAlertLines(payload alertmanager.Webhook, sendResolved bool) []string {
	alerts := append([]alertmanager.Alert(nil), payload.Alerts...)
	statusRank := func(status string) int {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "firing":
			return 0
		case "resolved":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(alerts, func(i, j int) bool {
		return statusRank(alerts[i].Status) < statusRank(alerts[j].Status)
	})

	lines := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		if alert.Status == "resolved" && !sendResolved {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "", alertSeparator, "")
		}
		lines = append(lines, renderAlertBlock(alert)...)
	}
	return lines
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
	appendSection := func(heading, value string) {
		lines = append(lines, "", heading+":")
		lines = append(lines, renderCodeBlock(value)...)
	}
	appendSection("Labels", renderLabels(alert.Labels))
	appendSection("Summary", summary)
	appendSection("Description", description)

	return lines
}

func renderTitle(payload alertmanager.Webhook) string {
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

func renderLabels(labels map[string]string) string {
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
