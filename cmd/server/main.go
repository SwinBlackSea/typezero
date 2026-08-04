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
	"typezero/internal/httpapi"
	"typezero/internal/provider"
	"typezero/internal/provider/deepseek"
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
	speech := qwen.New(httpClient, cfg.QwenURL, cfg.QwenAPIKey, cfg.QwenModel)
	text := deepseek.New(httpClient, cfg.DeepSeekURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel)
	handler := httpapi.New(httpapi.Dependencies{
		Speech: speech,
		Text:   text,
		SpeechForKey: func(apiKey string) provider.Speech {
			return qwen.New(httpClient, cfg.QwenURL, apiKey, cfg.QwenModel)
		},
		TextForKey: func(apiKey string) provider.Text {
			return deepseek.New(httpClient, cfg.DeepSeekURL, apiKey, cfg.DeepSeekModel)
		},
		Logger:           logger,
		MaxAudioBytes:    cfg.MaxAudioBytes,
		MaxDuration:      cfg.MaxAudioDuration,
		RequestTimeout:   cfg.RequestTimeout,
		RequestsPerMin:   cfg.RequestsPerMinute,
		ASRConcurrency:   cfg.ASRConcurrency,
		TrustedProxyCIDR: cfg.TrustedProxyCIDR,
		SessionTTL:       cfg.SessionTTL,
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
