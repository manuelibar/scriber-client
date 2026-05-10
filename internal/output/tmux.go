package output

import (
	"fmt"
	"os/exec"
	"strings"
)

// TmuxSession returns the session name owning the given pane, or "" if the pane is dead.
func TmuxSession(paneID string) (string, error) {
	out, err := exec.Command("tmux", "display", "-p", "-t", paneID, "#{session_name}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TmuxAlive returns true if the pane still exists.
func TmuxAlive(paneID string) bool {
	err := exec.Command("tmux", "display", "-p", "-t", paneID, "#{pane_id}").Run()
	return err == nil
}

// TmuxSendKeys sends literal text to the pane (no command interpretation, no Enter).
func TmuxSendKeys(paneID, text string) error {
	if text == "" {
		return nil
	}
	cmd := exec.Command("tmux", "send-keys", "-t", paneID, "-l", text)
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w (%s)", err, stderr.String())
	}
	return nil
}
