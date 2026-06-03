package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	hc *http.Client
}

func NewClient(socketPath string) *Client {
	return &Client{
		hc: &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					dialer := net.Dialer{Timeout: 750 * time.Millisecond}
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

func (c *Client) post(path string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://unix"+path, body)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) get(path string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, "http://unix"+path, nil)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("daemon not reachable (is `stt daemon` running?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var er ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&er)
		return fmt.Errorf("%s: %s", resp.Status, er.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Attach(req *AttachRequest) (*AttachResponse, error) {
	var out AttachResponse
	if err := c.post("/attach", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Detach(req *DetachRequest) error {
	return c.post("/detach", req, nil)
}

func (c *Client) Select(name string) (*SelectResponse, error) {
	var out SelectResponse
	if err := c.post("/select", SelectRequest{Name: name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SetSlot(name string, slot int) (*SetSlotResponse, error) {
	var out SetSlotResponse
	if err := c.post("/stream/set-slot", SetSlotRequest{Name: name, Slot: slot}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ClearSlot(name string) (*SetSlotResponse, error) {
	var out SetSlotResponse
	if err := c.post("/stream/clear-slot", ClearSlotRequest{Name: name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SelectSlot(slot int) (*SelectResponse, error) {
	var out SelectResponse
	if err := c.post("/slot/select", SelectSlotRequest{Slot: slot}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Cycle() (*CycleResponse, error) {
	var out CycleResponse
	if err := c.post("/cycle", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type MonitorQuery struct {
	HistorySince  time.Time
	HistoryStream string
	HistoryLimit  int
	HistoryOffset int
}

func (c *Client) Monitor(queries ...MonitorQuery) (*MonitorResponse, error) {
	var out MonitorResponse
	path := "/monitor"
	if len(queries) > 0 {
		values := url.Values{}
		query := queries[0]
		if !query.HistorySince.IsZero() {
			values.Set("history_since", query.HistorySince.UTC().Format(time.RFC3339Nano))
		}
		if query.HistoryStream != "" {
			values.Set("history_stream", query.HistoryStream)
		}
		if query.HistoryLimit != 0 {
			values.Set("history_limit", strconv.Itoa(query.HistoryLimit))
		}
		if query.HistoryOffset != 0 {
			values.Set("history_offset", strconv.Itoa(query.HistoryOffset))
		}
		if encoded := values.Encode(); encoded != "" {
			path += "?" + encoded
		}
	}
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Paste(text string) (*PasteResponse, error) {
	var out PasteResponse
	if err := c.post("/paste", PasteRequest{Text: text}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Fix(req FixRequest) (*FixResponse, error) {
	var out FixResponse
	if err := c.post("/fix", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Shutdown() error {
	var out ShutdownResponse
	return c.post("/shutdown", nil, &out)
}
