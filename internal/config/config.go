package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultQwenURL     = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	defaultDeepSeekURL = "https://api.deepseek.com/chat/completions"
)

type Config struct {
	ListenAddr        string
	QwenAPIKey        string
	QwenURL           string
	QwenModel         string
	DeepSeekAPIKey    string
	DeepSeekURL       string
	DeepSeekModel     string
	MaxAudioBytes     int64
	MaxAudioDuration  time.Duration
	ProviderTimeout   time.Duration
	RequestTimeout    time.Duration
	RequestsPerMinute int
	TrustedProxyCIDR  string
	SessionTTL        time.Duration
}

func FromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:        envOr("LISTEN_ADDR", ":8080"),
		QwenAPIKey:        strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")),
		QwenURL:           envOr("QWEN_API_URL", defaultQwenURL),
		QwenModel:         envOr("QWEN_ASR_MODEL", "qwen3-asr-flash"),
		DeepSeekAPIKey:    strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")),
		DeepSeekURL:       envOr("DEEPSEEK_API_URL", defaultDeepSeekURL),
		DeepSeekModel:     envOr("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		MaxAudioBytes:     10 << 20,
		MaxAudioDuration:  5 * time.Minute,
		ProviderTimeout:   90 * time.Second,
		RequestTimeout:    100 * time.Second,
		RequestsPerMinute: 10,
		TrustedProxyCIDR:  strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDR")),
		SessionTTL:        5 * time.Minute,
	}

	if cfg.QwenAPIKey == "" {
		return Config{}, errors.New("DASHSCOPE_API_KEY is required")
	}
	if cfg.DeepSeekAPIKey == "" {
		return Config{}, errors.New("DEEPSEEK_API_KEY is required")
	}

	var err error
	if cfg.ProviderTimeout, err = durationEnv("PROVIDER_TIMEOUT", cfg.ProviderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = durationEnv("REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RequestsPerMinute, err = intEnv("REQUESTS_PER_MINUTE", cfg.RequestsPerMinute); err != nil {
		return Config{}, err
	}
	if cfg.RequestsPerMinute < 1 {
		return Config{}, errors.New("REQUESTS_PER_MINUTE must be positive")
	}
	if err := validateProviderURL("QWEN_API_URL", cfg.QwenURL); err != nil {
		return Config{}, err
	}
	if err := validateProviderURL("DEEPSEEK_API_URL", cfg.DeepSeekURL); err != nil {
		return Config{}, err
	}
	if cfg.TrustedProxyCIDR != "" {
		if _, _, err := net.ParseCIDR(cfg.TrustedProxyCIDR); err != nil {
			return Config{}, errors.New("TRUSTED_PROXY_CIDR must be a valid CIDR")
		}
	}
	if cfg.SessionTTL, err = durationEnv("SESSION_TTL", cfg.SessionTTL); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateProviderURL(key, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", key)
	}
	if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return fmt.Errorf("%s must use HTTPS for remote hosts", key)
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return d, nil
}

func intEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return n, nil
}
