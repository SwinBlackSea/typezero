// Package hedged implements delayed ASR hedging: the accuracy-first provider
// starts immediately, while a faster fallback starts only if the primary is
// still pending after a short delay. The primary keeps priority until its
// result deadline; after that the fallback wins.
package hedged

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"typezero/internal/provider"
)

type Client struct {
	primary  provider.Speech
	fallback provider.Speech
	delay    time.Duration
	deadline time.Duration
	logger   *slog.Logger
}

func New(primary, fallback provider.Speech, delay, deadline time.Duration, logger *slog.Logger) *Client {
	return &Client{primary: primary, fallback: fallback, delay: delay, deadline: deadline, logger: logger}
}

type result struct {
	text string
	err  error
}

func (c *Client) Transcribe(ctx context.Context, audio provider.Audio) (string, error) {
	text, _, err := c.TranscribeWithProvider(ctx, audio)
	return text, err
}

func (c *Client) TranscribeWithProvider(ctx context.Context, audio provider.Audio) (string, string, error) {
	started := time.Now()
	primaryCtx, cancelPrimary := context.WithCancel(ctx)
	fallbackCtx, cancelFallback := context.WithCancel(ctx)
	defer cancelPrimary()
	defer cancelFallback()

	primaryResult := make(chan result, 1)
	fallbackResult := make(chan result, 1)
	go transcribe(primaryCtx, c.primary, audio, primaryResult)

	delayTimer := time.NewTimer(c.delay)
	deadlineTimer := time.NewTimer(c.deadline)
	defer delayTimer.Stop()
	defer deadlineTimer.Stop()

	fallbackStarted := false
	primaryDone := false
	deadlineReached := false
	var primaryErr error
	var savedFallback *result

	startFallback := func(reason string) {
		if fallbackStarted {
			return
		}
		fallbackStarted = true
		if c.logger != nil {
			c.logger.Info("asr hedge fallback started", "reason", reason)
		}
		go transcribe(fallbackCtx, c.fallback, audio, fallbackResult)
	}

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()

		case got := <-primaryResult:
			if time.Since(started) >= c.deadline {
				deadlineReached = true
				cancelPrimary()
				startFallback("primary_deadline")
				if savedFallback != nil {
					text, err := fallbackOutcome(*savedFallback, primaryErr)
					return text, "groq", err
				}
				continue
			}
			primaryDone = true
			if got.err == nil || errors.Is(got.err, provider.ErrEmptyTranscript) {
				return got.text, "qwen", got.err
			}
			primaryErr = got.err
			startFallback("primary_error")
			if savedFallback != nil {
				text, err := fallbackOutcome(*savedFallback, primaryErr)
				return text, "groq", err
			}

		case <-delayTimer.C:
			startFallback("delay")

		case <-deadlineTimer.C:
			deadlineReached = true
			cancelPrimary()
			startFallback("primary_deadline")
			if savedFallback != nil {
				if c.logger != nil && savedFallback.err == nil {
					c.logger.Info("asr hedge fallback selected", "reason", "primary_deadline")
				}
				text, err := fallbackOutcome(*savedFallback, primaryErr)
				return text, "groq", err
			}

		case got := <-fallbackResult:
			if primaryDone || deadlineReached {
				if c.logger != nil && got.err == nil {
					reason := "primary_error"
					if deadlineReached {
						reason = "primary_deadline"
					}
					c.logger.Info("asr hedge fallback selected", "reason", reason)
				}
				text, err := fallbackOutcome(got, primaryErr)
				return text, "groq", err
			}
			// The fallback may finish first, but Qwen remains accuracy-first
			// until the configured deadline.
			saved := got
			savedFallback = &saved
			fallbackResult = nil
		}
	}
}

func transcribe(ctx context.Context, speech provider.Speech, audio provider.Audio, out chan<- result) {
	text, err := speech.Transcribe(ctx, audio)
	out <- result{text: text, err: err}
}

func fallbackOutcome(got result, primaryErr error) (string, error) {
	if got.err == nil || errors.Is(got.err, provider.ErrEmptyTranscript) {
		return got.text, got.err
	}
	if primaryErr != nil {
		return "", fmt.Errorf("primary ASR failed: %v; fallback ASR failed: %w", primaryErr, got.err)
	}
	return "", fmt.Errorf("fallback ASR failed after primary deadline: %w", got.err)
}
