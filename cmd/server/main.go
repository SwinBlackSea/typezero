package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"typezero/internal/config"
	"typezero/internal/hotwords"
	"typezero/internal/httpapi"
	"typezero/internal/provider"
	"typezero/internal/provider/deepseek"
	"typezero/internal/provider/groq"
	"typezero/internal/provider/qwen"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: cfg.ProviderTimeout}
	speech := provider.Speech(qwen.New(httpClient, cfg.QwenURL, cfg.QwenAPIKey, cfg.QwenModel, cfg.QwenWaitTimeout))
	hotwordStore := hotwords.New(cfg.HotwordsFile)
	if _, err := os.Stat(cfg.HotwordsFile); errors.Is(err, os.ErrNotExist) {
		logger.Warn("hotwords file not found; hotword table is empty", "path", cfg.HotwordsFile)
	}
	if cfg.SpeechProvider == "groq" {
		// Groq Whisper runs at roughly real-time or faster and is not subject
		// to DashScope's queueing; it is the preferred ASR when configured.
		// Clients that pass their own DashScope key still use Qwen.
		speech = groq.New(httpClient, cfg.GroqURL, cfg.GroqAPIKey, cfg.GroqModel, cfg.GroqPrompt)
	}
	text := deepseek.New(httpClient, cfg.DeepSeekURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel, hotwordStore)
	text.SetHotwordGuidance(cfg.HotwordGuidance)
	primaryLabel := cfg.SpeechProvider
	var compareSpeech provider.Speech
	compareLabel := ""
	if cfg.ASRCompare {
		if cfg.SpeechProvider == "groq" {
			compareSpeech = qwen.New(httpClient, cfg.QwenURL, cfg.QwenAPIKey, cfg.QwenModel, cfg.QwenWaitTimeout)
			compareLabel = "qwen"
		} else if cfg.GroqAPIKey != "" {
			compareSpeech = groq.New(httpClient, cfg.GroqURL, cfg.GroqAPIKey, cfg.GroqModel, cfg.GroqPrompt)
			compareLabel = "groq"
		}
	}
	handler := httpapi.New(httpapi.Dependencies{
		Speech: speech,
		Text:   text,
		SpeechForKey: func(apiKey string) provider.Speech {
			return qwen.New(httpClient, cfg.QwenURL, apiKey, cfg.QwenModel, cfg.QwenWaitTimeout)
		},
		TextForKey: func(apiKey string) provider.Text {
			return deepseek.New(httpClient, cfg.DeepSeekURL, apiKey, cfg.DeepSeekModel, hotwordStore).SetHotwordGuidance(cfg.HotwordGuidance)
		},
		PrimaryLabel:      primaryLabel,
		CompareSpeech:     compareSpeech,
		CompareLabel:      compareLabel,
		CompareFile:       cfg.ASRCompareFile,
		PolishCompare:     cfg.PolishCompare,
		PolishCompareFile: cfg.PolishCompareFile,
		TestAudioDir:      cfg.TestAudioDir,
		Logger:            logger,
		MaxAudioBytes:     cfg.MaxAudioBytes,
		MaxDuration:       cfg.MaxAudioDuration,
		RequestTimeout:    cfg.RequestTimeout,
		RequestsPerMin:    cfg.RequestsPerMinute,
		ASRConcurrency:    cfg.ASRConcurrency,
		TrustedProxyCIDR:  cfg.TrustedProxyCIDR,
		SessionTTL:        cfg.SessionTTL,
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout + 5*time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("server started", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
