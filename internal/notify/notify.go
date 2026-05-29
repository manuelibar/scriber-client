package notify

import (
	"log/slog"
	"os/exec"
	"strings"
	"sync"
)

var replaceMu sync.Mutex
var replaceID string

// Send emits a desktop notification. Best-effort; logs and ignores errors.
func Send(title, body string) {
	replaceMu.Lock()
	defer replaceMu.Unlock()

	args := notifyArgs(title, body, true)
	out, err := exec.Command("notify-send", args...).Output()
	if err != nil && replaceID != "" {
		replaceID = ""
		out, err = exec.Command("notify-send", notifyArgs(title, body, true)...).Output()
	}
	if err == nil {
		if id := strings.TrimSpace(string(out)); id != "" {
			replaceID = id
		}
		return
	}

	cmd := exec.Command("notify-send", notifyArgs(title, body, false)...)
	if err := cmd.Run(); err != nil {
		slog.Debug("notify-send failed", "err", err)
	}
}

func notifyArgs(title, body string, printID bool) []string {
	args := []string{
		"-a", "scriber",
		"-t", "2500",
		"-e",
		"-h", "string:x-canonical-private-synchronous:scriber",
	}
	if printID {
		args = append(args, "-p")
		if replaceID != "" {
			args = append(args, "-r", replaceID)
		}
	}
	args = append(args, title)
	if body != "" {
		args = append(args, body)
	}
	return args
}
