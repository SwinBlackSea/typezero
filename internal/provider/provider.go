package provider

import "context"

type Audio struct {
	Data      []byte
	MediaType string
	Filename  string
}

type Speech interface {
	Transcribe(ctx context.Context, audio Audio) (string, error)
}

type Text interface {
	Polish(ctx context.Context, rawText string) (string, error)
}
