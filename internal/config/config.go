package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultDedupeCacheSize = 10000

const (
	defaultListenAddr     = ":8080"
	defaultRequestTimeout = 5 * time.Second
)

type Config struct {
	ListenAddr      string
	WebhookURLs     []string
	RequestTimeout  time.Duration
	SendResolved    bool
	DedupeCacheSize int
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:      defaultListenAddr,
		WebhookURLs:     parseWebhookURLs(),
		RequestTimeout:  defaultRequestTimeout,
		SendResolved:    true,
		DedupeCacheSize: DefaultDedupeCacheSize,
	}
	if raw := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); raw != "" {
		cfg.ListenAddr = raw
	}

	if len(cfg.WebhookURLs) == 0 {
		return Config{}, fmt.Errorf("WEBHOOK_URLS is required")
	}

	if raw := strings.TrimSpace(os.Getenv("REQUEST_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid REQUEST_TIMEOUT: %w", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("REQUEST_TIMEOUT must be positive")
		}
		cfg.RequestTimeout = d
	}

	if raw := strings.TrimSpace(os.Getenv("SEND_RESOLVED")); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid SEND_RESOLVED: %w", err)
		}
		cfg.SendResolved = b
	}

	if raw := strings.TrimSpace(os.Getenv("DEDUPE_CACHE_SIZE")); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid DEDUPE_CACHE_SIZE: %w", err)
		}
		if size <= 0 {
			return Config{}, fmt.Errorf("DEDUPE_CACHE_SIZE must be positive")
		}
		cfg.DedupeCacheSize = size
	}

	return cfg, nil
}

func parseWebhookURLs() []string {
	rawValues := []string{os.Getenv("WEBHOOK_URLS"), os.Getenv("WEBHOOK_URL")}

	seen := map[string]struct{}{}
	urls := make([]string, 0)
	for _, raw := range rawValues {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n'
		}) {
			url := strings.TrimSpace(part)
			if url == "" {
				continue
			}
			if _, ok := seen[url]; ok {
				continue
			}
			seen[url] = struct{}{}
			urls = append(urls, url)
		}
	}
	return urls
}
