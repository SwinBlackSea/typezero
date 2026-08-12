package config

import (
	"testing"
	"time"
)

func TestFromEnvDefaults(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.DeepSeekModel != "deepseek-v4-flash" {
		t.Fatalf("DeepSeekModel = %q", cfg.DeepSeekModel)
	}
	if cfg.ChunkSeconds != 0 {
		t.Fatalf("ChunkSeconds = %d, want default 0", cfg.ChunkSeconds)
	}
	if cfg.ASRHedgeDelay != 2*time.Second || cfg.QwenResultDeadline != 16*time.Second {
		t.Fatalf("hedge defaults = %s/%s", cfg.ASRHedgeDelay, cfg.QwenResultDeadline)
	}
}

func TestFromEnvParsesChunkSeconds(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("CHUNK_SECONDS", "10")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ChunkSeconds != 10 {
		t.Fatalf("ChunkSeconds = %d, want 10", cfg.ChunkSeconds)
	}
}

func TestFromEnvRejectsChunkSecondsOutOfRange(t *testing.T) {
	setMinimalEnv(t)
	for _, value := range []string{"-1", "121"} {
		t.Setenv("CHUNK_SECONDS", value)
		if _, err := FromEnv(); err == nil {
			t.Fatalf("FromEnv() error = nil for CHUNK_SECONDS=%q", value)
		}
	}
}

func TestFromEnvRejectsRemoteHTTPProvider(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("QWEN_API_URL", "http://example.com/chat/completions")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want insecure URL error")
	}
}

func TestFromEnvValidatesHedge(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("ASR_HEDGE", "1")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil without GROQ_API_KEY")
	}

	t.Setenv("GROQ_API_KEY", "groq-secret")
	t.Setenv("ASR_HEDGE_DELAY", "6s")
	t.Setenv("QWEN_RESULT_DEADLINE", "6s")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil when deadline is not greater than delay")
	}

	t.Setenv("ASR_HEDGE_DELAY", "2s")
	t.Setenv("ASR_COMPARE", "1")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil when hedge and comparison are both enabled")
	}
}

func setMinimalEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"DASHSCOPE_API_KEY":    "qwen-secret",
		"DEEPSEEK_API_KEY":     "deepseek-secret",
		"LISTEN_ADDR":          "",
		"QWEN_API_URL":         "",
		"QWEN_ASR_MODEL":       "",
		"DEEPSEEK_API_URL":     "",
		"DEEPSEEK_MODEL":       "",
		"PROVIDER_TIMEOUT":     "",
		"REQUEST_TIMEOUT":      "",
		"REQUESTS_PER_MINUTE":  "",
		"QWEN_WAIT_TIMEOUT":    "",
		"SPEECH_PROVIDER":      "",
		"GROQ_API_KEY":         "",
		"GROQ_MODEL":           "",
		"GROQ_API_URL":         "",
		"CHUNK_SECONDS":        "",
		"TRUSTED_PROXY_CIDR":   "",
		"ASR_HEDGE":            "",
		"ASR_HEDGE_DELAY":      "",
		"QWEN_RESULT_DEADLINE": "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
