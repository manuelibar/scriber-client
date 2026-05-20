package output

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"scriber/internal/ipc"
)

// PTYAlive returns true when the attach process and injection socket are still
// reachable.
func PTYAlive(target ipc.Target) bool {
	if target.TargetType != ipc.TargetTypePTY || target.TargetRef == "" {
		return false
	}
	if target.PID > 0 {
		err := syscall.Kill(target.PID, 0)
		if err != nil && err != syscall.EPERM {
			return false
		}
	}
	conn, err := net.DialTimeout("unix", target.TargetRef, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func SendText(target ipc.Target, text string) error {
	if text == "" {
		return nil
	}
	if target.TargetType != ipc.TargetTypePTY {
		return fmt.Errorf("unsupported target type %q", target.TargetType)
	}
	return PTYSendText(target.TargetRef, text)
}

func PTYSendText(socketPath, text string) error {
	body, err := json.Marshal(ipc.InjectRequest{Text: text})
	if err != nil {
		return err
	}
	hc := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	req, err := http.NewRequest(http.MethodPost, "http://unix/inject", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("target socket missing: %w", err)
		}
		return fmt.Errorf("send to pty target: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pty target rejected inject: %s", resp.Status)
	}
	return nil
}
