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
	ASRConcurrency    int
	TrustedProxyCIDR  string
	SessionTTL        time.Duration
	QwenWaitTimeout   time.Duration
	SpeechProvider    string
	GroqAPIKey        string
	GroqModel         string
	GroqURL           string
	// ChunkSeconds is the global chunking interval applied to every
	// recording: 0 disables chunking (whole file, single ASR call), N cuts
	// every N seconds with a fixed 2s overlap (window = N+2s). The value is
	// exposed via /healthz so the client can cut accordingly.
	ChunkSeconds      int
	ASRCompare        bool
	ASRCompareFile    string
	TestAudioDir      string
}

func FromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:       envOr("LISTEN_ADDR", ":8080"),
		QwenAPIKey:       strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")),
		QwenURL:          envOr("QWEN_API_URL", defaultQwenURL),
		QwenModel:        envOr("QWEN_ASR_MODEL", "qwen3-asr-flash"),
		DeepSeekAPIKey:   strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")),
		DeepSeekURL:      envOr("DEEPSEEK_API_URL", defaultDeepSeekURL),
		DeepSeekModel:    envOr("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		MaxAudioBytes:    10 << 20,
		MaxAudioDuration: 5 * time.Minute,
		// DashScope queues burst requests server-side for up to the
		// X-DashScope-Wait-Timeout we declare; the provider timeout must
		// cover base latency plus that queueing window (official guidance).
		ProviderTimeout:   150 * time.Second,
		RequestTimeout:    170 * time.Second,
		RequestsPerMinute: 60,
		// Measured on the project account: two concurrent ASR calls queue
		// behind each other (one waited 90s and timed out). Serial is the
		// safe default; raise only after verifying the account quota.
		ASRConcurrency:   1,
		TrustedProxyCIDR: strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDR")),
		SessionTTL:       5 * time.Minute,
		QwenWaitTimeout:  30 * time.Second,
		SpeechProvider:   "qwen",
		GroqAPIKey:       strings.TrimSpace(os.Getenv("GROQ_API_KEY")),
		GroqModel:        envOr("GROQ_MODEL", "whisper-large-v3"),
		GroqURL:          envOr("GROQ_API_URL", "https://api.groq.com/openai/v1"),
		ChunkSeconds:     30,
		ASRCompare:        envBool("ASR_COMPARE"),
		ASRCompareFile:    envOr("ASR_COMPARE_FILE", "/tmp/asr_compare.jsonl"),
		TestAudioDir:      strings.TrimSpace(os.Getenv("TEST_AUDIO_DIR")),
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
	if cfg.ASRConcurrency, err = intEnv("ASR_CONCURRENCY", cfg.ASRConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.ASRConcurrency < 1 {
		return Config{}, errors.New("ASR_CONCURRENCY must be positive")
	}
	if cfg.ChunkSeconds, err = intEnv("CHUNK_SECONDS", cfg.ChunkSeconds); err != nil {
		return Config{}, err
	}
	if cfg.ChunkSeconds < 0 || cfg.ChunkSeconds > 120 {
		return Config{}, errors.New("CHUNK_SECONDS must be between 0 and 120")
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
	if cfg.QwenWaitTimeout, err = durationEnv("QWEN_WAIT_TIMEOUT", cfg.QwenWaitTimeout); err != nil {
		return Config{}, err
	}
	if cfg.QwenWaitTimeout < 0 || cfg.QwenWaitTimeout > 120*time.Second {
		return Config{}, errors.New("QWEN_WAIT_TIMEOUT must be between 0s and 120s")
	}
	cfg.SpeechProvider = strings.ToLower(strings.TrimSpace(envOr("SPEECH_PROVIDER", cfg.SpeechProvider)))
	if cfg.SpeechProvider != "qwen" && cfg.SpeechProvider != "groq" {
		return Config{}, errors.New("SPEECH_PROVIDER must be qwen or groq")
	}
	if cfg.SpeechProvider == "groq" && cfg.GroqAPIKey == "" {
		return Config{}, errors.New("GROQ_API_KEY is required when SPEECH_PROVIDER=groq")
	}
	if err := validateProviderURL("GROQ_API_URL", cfg.GroqURL); err != nil {
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

func envBool(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
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
