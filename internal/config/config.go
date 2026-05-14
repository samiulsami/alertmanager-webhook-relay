package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr    = ":8080"
	defaultRequestTimout = 5 * time.Second
)

type Config struct {
	ListenAddr     string
	GoogleChatURL  string
	RequestTimeout time.Duration
	SendResolved   bool
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:     defaultListenAddr,
		GoogleChatURL:  strings.TrimSpace(os.Getenv("GOOGLE_CHAT_WEBHOOK_URL")),
		RequestTimeout: defaultRequestTimout,
		SendResolved:   true,
	}
	if raw := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); raw != "" {
		cfg.ListenAddr = raw
	}

	if cfg.GoogleChatURL == "" {
		return Config{}, fmt.Errorf("GOOGLE_CHAT_WEBHOOK_URL is required")
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

	return cfg, nil
}
