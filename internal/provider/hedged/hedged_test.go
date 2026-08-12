package hedged

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"typezero/internal/provider"
)

type fakeSpeech struct {
	delay    time.Duration
	text     string
	err      error
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (s *fakeSpeech) Transcribe(ctx context.Context, _ provider.Audio) (string, error) {
	if s.started != nil {
		s.once.Do(func() { close(s.started) })
	}
	select {
	case <-ctx.Done():
		if s.canceled != nil {
			select {
			case <-s.canceled:
			default:
				close(s.canceled)
			}
		}
		return "", ctx.Err()
	case <-time.After(s.delay):
		return s.text, s.err
	}
}

func TestPrimaryWinsBeforeHedgeDelay(t *testing.T) {
	fallbackStarted := make(chan struct{})
	client := New(
		&fakeSpeech{delay: 5 * time.Millisecond, text: "qwen"},
		&fakeSpeech{text: "groq", started: fallbackStarted},
		30*time.Millisecond,
		80*time.Millisecond,
		nil,
	)
	text, err := client.Transcribe(context.Background(), provider.Audio{})
	if err != nil || text != "qwen" {
		t.Fatalf("Transcribe() = %q, %v", text, err)
	}
	select {
	case <-fallbackStarted:
		t.Fatal("fallback started for a fast primary")
	default:
	}
}

func TestPrimaryKeepsPriorityUntilDeadline(t *testing.T) {
	client := New(
		&fakeSpeech{delay: 45 * time.Millisecond, text: "qwen"},
		&fakeSpeech{delay: 5 * time.Millisecond, text: "groq"},
		15*time.Millisecond,
		80*time.Millisecond,
		nil,
	)
	text, err := client.Transcribe(context.Background(), provider.Audio{})
	if err != nil || text != "qwen" {
		t.Fatalf("Transcribe() = %q, %v", text, err)
	}
}

func TestFallbackWinsAndCancelsSlowPrimaryAtDeadline(t *testing.T) {
	primaryCanceled := make(chan struct{})
	client := New(
		&fakeSpeech{delay: time.Second, text: "qwen", canceled: primaryCanceled},
		&fakeSpeech{delay: 5 * time.Millisecond, text: "groq"},
		15*time.Millisecond,
		45*time.Millisecond,
		nil,
	)
	started := time.Now()
	text, err := client.Transcribe(context.Background(), provider.Audio{})
	if err != nil || text != "groq" {
		t.Fatalf("Transcribe() = %q, %v", text, err)
	}
	if elapsed := time.Since(started); elapsed < 35*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("elapsed = %s, want deadline-bounded fallback", elapsed)
	}
	select {
	case <-primaryCanceled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("slow primary was not canceled")
	}
}

func TestPrimaryErrorStartsFallbackImmediately(t *testing.T) {
	client := New(
		&fakeSpeech{delay: 2 * time.Millisecond, err: errors.New("qwen down")},
		&fakeSpeech{delay: 2 * time.Millisecond, text: "groq"},
		100*time.Millisecond,
		200*time.Millisecond,
		nil,
	)
	started := time.Now()
	text, err := client.Transcribe(context.Background(), provider.Audio{})
	if err != nil || text != "groq" {
		t.Fatalf("Transcribe() = %q, %v", text, err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("fallback waited for hedge delay after primary error: %s", elapsed)
	}
}

func TestCallerCancellationStopsBothProviders(t *testing.T) {
	primaryCanceled := make(chan struct{})
	fallbackCanceled := make(chan struct{})
	client := New(
		&fakeSpeech{delay: time.Second, canceled: primaryCanceled},
		&fakeSpeech{delay: time.Second, canceled: fallbackCanceled},
		5*time.Millisecond,
		500*time.Millisecond,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Transcribe(ctx, provider.Audio{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Transcribe() error = %v", err)
	}
	for name, canceled := range map[string]<-chan struct{}{"primary": primaryCanceled, "fallback": fallbackCanceled} {
		select {
		case <-canceled:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("%s provider was not canceled", name)
		}
	}
}

func TestFallbackErrorIsReturnedAfterDeadline(t *testing.T) {
	client := New(
		&fakeSpeech{delay: time.Second},
		&fakeSpeech{delay: 2 * time.Millisecond, err: errors.New("groq down")},
		5*time.Millisecond,
		20*time.Millisecond,
		nil,
	)
	_, err := client.Transcribe(context.Background(), provider.Audio{})
	if err == nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
}
