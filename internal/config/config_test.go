package config

import "testing"

func TestFromEnvDefaults(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.DeepSeekModel != "deepseek-v4-flash" {
		t.Fatalf("DeepSeekModel = %q", cfg.DeepSeekModel)
	}
	if cfg.ChunkSeconds != 30 {
		t.Fatalf("ChunkSeconds = %d, want default 30", cfg.ChunkSeconds)
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

func setMinimalEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"DASHSCOPE_API_KEY":   "qwen-secret",
		"DEEPSEEK_API_KEY":    "deepseek-secret",
		"LISTEN_ADDR":         "",
		"QWEN_API_URL":        "",
		"QWEN_ASR_MODEL":      "",
		"DEEPSEEK_API_URL":    "",
		"DEEPSEEK_MODEL":      "",
		"PROVIDER_TIMEOUT":    "",
		"REQUEST_TIMEOUT":     "",
		"REQUESTS_PER_MINUTE": "",
		"QWEN_WAIT_TIMEOUT":   "",
		"SPEECH_PROVIDER":     "",
		"GROQ_API_KEY":        "",
		"GROQ_MODEL":          "",
		"GROQ_API_URL":        "",
		"CHUNK_SECONDS":       "",
		"TRUSTED_PROXY_CIDR":  "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
