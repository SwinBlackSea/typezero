package qwen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typezero/internal/provider"
)

type Client struct {
	httpClient  *http.Client
	url         string
	apiKey      string
	model       string
	waitTimeout time.Duration
}

// New creates a DashScope compatible-mode client. waitTimeout is declared via
// X-DashScope-Wait-Timeout so burst requests queue server-side (instead of
// hanging until the client timeout); 0 disables the header.
func New(httpClient *http.Client, url, apiKey, model string, waitTimeout time.Duration) *Client {
	return &Client{httpClient: httpClient, url: url, apiKey: apiKey, model: model, waitTimeout: waitTimeout}
}

type request struct {
	Model      string     `json:"model"`
	Messages   []message  `json:"messages"`
	Stream     bool       `json:"stream"`
	ASROptions asrOptions `json:"asr_options"`
}

type message struct {
	Role    string         `json:"role"`
	Content []audioContent `json:"content"`
}

type audioContent struct {
	Type       string     `json:"type"`
	InputAudio audioInput `json:"input_audio"`
}

type audioInput struct {
	Data string `json:"data"`
}

type asrOptions struct {
	EnableITN bool `json:"enable_itn"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (c *Client) Transcribe(ctx context.Context, audio provider.Audio) (string, error) {
	dataURL := "data:" + audio.MediaType + ";base64," + base64.StdEncoding.EncodeToString(audio.Data)
	payload := request{
		Model: c.model,
		Messages: []message{{
			Role:    "user",
			Content: []audioContent{{Type: "input_audio", InputAudio: audioInput{Data: dataURL}}},
		}},
		Stream:     false,
		ASROptions: asrOptions{EnableITN: true},
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return "", fmt.Errorf("encode qwen request: %w", err)
	}
	requestBytes := body.Bytes()

	// DashScope rate-limits per account+model (RPM/RPS/Traffic Burst). A 429
	// or 5xx is transient; retry with bounded backoff instead of failing the
	// whole dictation session. Network/context errors are not retried: when
	// the server queues longer than the declared wait timeout, the request
	// already consumed its full budget and retrying would double the latency.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * time.Second
			if lastHTTPStatus(lastErr) == http.StatusTooManyRequests {
				backoff *= 3 // give the quota window time to refill
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}
		text, err, retryable := c.transcribeOnce(ctx, requestBytes)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", lastErr
}

func lastHTTPStatus(err error) int {
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

func (c *Client) transcribeOnce(ctx context.Context, requestBytes []byte) (string, error, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(requestBytes))
	if err != nil {
		return "", fmt.Errorf("create qwen request: %w", err), false
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if c.waitTimeout > 0 {
		req.Header.Set("X-DashScope-Wait-Timeout", strconv.Itoa(int(c.waitTimeout.Seconds())))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call qwen: %w", err), false
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 2<<20)
	var decoded response
	if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode qwen response: %w", err), false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", &provider.HTTPError{Provider: "qwen", StatusCode: resp.StatusCode, Code: decoded.Error.Code}, retryable
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("qwen returned no choices"), false
	}
	if reason := decoded.Choices[0].FinishReason; reason != "" && reason != "stop" {
		return "", fmt.Errorf("qwen stopped with finish reason %s", reason), false
	}
	text := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if text == "" {
		return "", provider.ErrEmptyTranscript, false
	}
	return text, nil, false
}
