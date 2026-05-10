package notify

import (
	"log/slog"
	"os/exec"
)

// Send emits a desktop notification. Best-effort; logs and ignores errors.
func Send(title, body string) {
	cmd := exec.Command("notify-send", "-a", "scriber", "-t", "2500", title, body)
	if err := cmd.Run(); err != nil {
		slog.Debug("notify-send failed", "err", err)
	}
}
