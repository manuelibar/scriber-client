package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Record struct {
	Timestamp  time.Time `json:"ts"`
	AudioMs    int       `json:"audio_ms"`
	Transcript string    `json:"transcript"`
	Raw        string    `json:"raw,omitempty"`
	TargetPane string    `json:"target_pane,omitempty"`
	TargetAlias string   `json:"target_alias,omitempty"`
	Mode       string    `json:"mode"` // "tmux" | "noop"
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
	InferenceMs int      `json:"inference_ms,omitempty"`
}

// Save writes the record to <dir>/<ISO8601>.json. Best-effort: if the dir cannot be
// created or the write fails, returns the error but the record is lost.
func Save(dir string, rec Record) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := rec.Timestamp.UTC().Format("20060102T150405.000000Z") + ".json"
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}
