package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

type Client struct {
	hc *http.Client
}

func NewClient(socketPath string) *Client {
	return &Client{
		hc: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
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
		return fmt.Errorf("daemon not reachable (is `scriber daemon` running?): %w", err)
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

func (c *Client) List() (*ListResponse, error) {
	var out ListResponse
	if err := c.get("/list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Switch(alias string) (*SwitchResponse, error) {
	var out SwitchResponse
	if err := c.post("/switch", SwitchRequest{Alias: alias}, &out); err != nil {
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

func (c *Client) Status() (*StatusResponse, error) {
	var out StatusResponse
	if err := c.get("/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
