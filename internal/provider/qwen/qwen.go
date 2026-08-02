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
	"strings"

	"typezero/internal/provider"
)

type Client struct {
	httpClient *http.Client
	url        string
	apiKey     string
	model      string
}

func New(httpClient *http.Client, url, apiKey, model string) *Client {
	return &Client{httpClient: httpClient, url: url, apiKey: apiKey, model: model}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, &body)
	if err != nil {
		return "", fmt.Errorf("create qwen request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call qwen: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 2<<20)
	var decoded response
	if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode qwen response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &provider.HTTPError{Provider: "qwen", StatusCode: resp.StatusCode, Code: decoded.Error.Code}
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("qwen returned no choices")
	}
	if reason := decoded.Choices[0].FinishReason; reason != "" && reason != "stop" {
		return "", fmt.Errorf("qwen stopped with finish reason %s", reason)
	}
	text := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("qwen returned empty transcript")
	}
	return text, nil
}
