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
		"HOTWORDS_FILE":       "",
		"TRUSTED_PROXY_CIDR":  "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
