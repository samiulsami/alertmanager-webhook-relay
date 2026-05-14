package alertmanager

import (
	"encoding/json"
	"fmt"
	"strings"

	amwebhook "github.com/prometheus/alertmanager/notify/webhook"
	amtemplate "github.com/prometheus/alertmanager/template"
)

func Decode(data []byte) (amwebhook.Message, error) {
	payload := amwebhook.Message{Data: &amtemplate.Data{}}
	if err := json.Unmarshal(data, &payload); err != nil {
		return amwebhook.Message{}, fmt.Errorf("decode alertmanager webhook: %w", err)
	}
	if payload.Data == nil {
		payload.Data = &amtemplate.Data{}
	}

	payload.Status = normalizeStatus(payload.Status)
	if payload.Status == "" {
		return amwebhook.Message{}, fmt.Errorf("missing status")
	}
	if payload.Status != "firing" && payload.Status != "resolved" {
		return amwebhook.Message{}, fmt.Errorf("unsupported status %q", payload.Status)
	}
	if len(payload.Alerts) == 0 {
		return amwebhook.Message{}, fmt.Errorf("alerts must not be empty")
	}

	for i := range payload.Alerts {
		payload.Alerts[i].Status = normalizeStatus(payload.Alerts[i].Status)
		if payload.Alerts[i].Status == "" {
			payload.Alerts[i].Status = payload.Status
		}
		if payload.Alerts[i].Labels == nil {
			payload.Alerts[i].Labels = amtemplate.KV{}
		}
		if payload.Alerts[i].Annotations == nil {
			payload.Alerts[i].Annotations = amtemplate.KV{}
		}
	}

	if payload.GroupLabels == nil {
		payload.GroupLabels = amtemplate.KV{}
	}
	if payload.CommonLabels == nil {
		payload.CommonLabels = amtemplate.KV{}
	}
	if payload.CommonAnnotations == nil {
		payload.CommonAnnotations = amtemplate.KV{}
	}

	return payload, nil
}

func normalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}
