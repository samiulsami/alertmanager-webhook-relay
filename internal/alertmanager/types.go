package alertmanager

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func Decode(data []byte) (Webhook, error) {
	var payload Webhook
	if err := json.Unmarshal(data, &payload); err != nil {
		return Webhook{}, fmt.Errorf("decode alertmanager webhook: %w", err)
	}

	payload.Status = strings.ToLower(strings.TrimSpace(payload.Status))
	if payload.Status == "" {
		return Webhook{}, fmt.Errorf("missing status")
	}
	if payload.Status != "firing" && payload.Status != "resolved" {
		return Webhook{}, fmt.Errorf("unsupported status %q", payload.Status)
	}
	if len(payload.Alerts) == 0 {
		return Webhook{}, fmt.Errorf("alerts must not be empty")
	}

	for i := range payload.Alerts {
		payload.Alerts[i].Status = strings.ToLower(strings.TrimSpace(payload.Alerts[i].Status))
		if payload.Alerts[i].Status == "" {
			payload.Alerts[i].Status = payload.Status
		}
		if payload.Alerts[i].Labels == nil {
			payload.Alerts[i].Labels = map[string]string{}
		}
		if payload.Alerts[i].Annotations == nil {
			payload.Alerts[i].Annotations = map[string]string{}
		}
	}

	if payload.GroupLabels == nil {
		payload.GroupLabels = map[string]string{}
	}
	if payload.CommonLabels == nil {
		payload.CommonLabels = map[string]string{}
	}
	if payload.CommonAnnotations == nil {
		payload.CommonAnnotations = map[string]string{}
	}

	return payload, nil
}

func (w Webhook) AlertName() string {
	if name := strings.TrimSpace(w.GroupLabels["alertname"]); name != "" {
		return name
	}
	if name := strings.TrimSpace(w.CommonLabels["alertname"]); name != "" {
		return name
	}
	for _, alert := range w.Alerts {
		if name := strings.TrimSpace(alert.Labels["alertname"]); name != "" {
			return name
		}
	}
	return "Alertmanager Notification"
}

func (w Webhook) CountByStatus(status string) int {
	count := 0
	for _, alert := range w.Alerts {
		if alert.Status == status {
			count++
		}
	}
	return count
}
