package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type Result struct {
	Text     string `json:"text"`
	Raw      string `json:"raw"`
	Ms       int    `json:"ms"`
	AudioMs  int    `json:"audio_ms"`
	Language string `json:"language,omitempty"`
}

type Client struct {
	baseURL string
	hc      *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		hc:      &http.Client{Timeout: timeout},
	}
}

// Healthz returns nil if the server is ready.
func (c *Client) Healthz(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("server not ready: %s", resp.Status)
	}
	return nil
}

// Transcribe POSTs raw int16 LE PCM samples and returns the result.
func (c *Client) Transcribe(ctx context.Context, pcm []byte, sampleRate int, language string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transcribe", bytes.NewReader(pcm))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Sample-Rate", strconv.Itoa(sampleRate))
	if language != "" {
		req.Header.Set("X-Language", language)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("transcribe failed: %s", resp.Status)
	}

	var out Result
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}
