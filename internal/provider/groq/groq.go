// Package groq implements the Speech provider backed by Groq Whisper
// (whisper-large-v3 / whisper-large-v3-turbo). Latency is roughly real-time
// or faster (seconds for minute-long audio), which removes the DashScope
// queueing that dominated Qwen ASR latency.
package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"typezero/internal/provider"
)

type Client struct {
	httpClient *http.Client
	url        string
	apiKey     string
	model      string
}

// New creates a Groq transcription client. url is the base API URL, e.g.
// https://api.groq.com/openai/v1.
func New(httpClient *http.Client, url, apiKey, model string) *Client {
	return &Client{httpClient: httpClient, url: url, apiKey: apiKey, model: model}
}

type transcriptionResponse struct {
	Text  string `json:"text"`
	XGroq struct {
		ID string `json:"id"`
	} `json:"x_groq"`
}

type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (c *Client) Transcribe(ctx context.Context, audio provider.Audio) (string, error) {
	body, contentType, err := c.buildBody(audio)
	if err != nil {
		return "", err
	}

	// Groq's free tier rate-limits transcription requests; a bounded retry
	// absorbs transient 429/5xx responses.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * time.Second
			if lastHTTPStatus(lastErr) == http.StatusTooManyRequests {
				backoff *= 3
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}
		text, err, retryable := c.transcribeOnce(ctx, body, contentType)
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

func (c *Client) buildBody(audio provider.Audio) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("model", c.model); err != nil {
		return nil, "", fmt.Errorf("write groq model field: %w", err)
	}
	part, err := writer.CreateFormFile("file", "recording.wav")
	if err != nil {
		return nil, "", fmt.Errorf("create groq file part: %w", err)
	}
	if _, err := part.Write(audio.Data); err != nil {
		return nil, "", fmt.Errorf("write groq audio: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close groq multipart: %w", err)
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

func (c *Client) transcribeOnce(ctx context.Context, body []byte, contentType string) (string, error, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/audio/transcriptions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create groq request: %w", err), false
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call groq: %w", err), false
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 4<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope errorEnvelope
		_ = json.NewDecoder(limited).Decode(&envelope)
		code := envelope.Error.Code
		if code == "" {
			code = envelope.Error.Type
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", &provider.HTTPError{Provider: "groq", StatusCode: resp.StatusCode, Code: code}, retryable
	}

	var decoded transcriptionResponse
	if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode groq response: %w", err), false
	}
	text := strings.TrimSpace(decoded.Text)
	if text == "" {
		return "", provider.ErrEmptyTranscript, false
	}
	return text, nil, false
}
