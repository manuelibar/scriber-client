package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsUseSTTStateNamespace(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	wantState := filepath.Join(home, ".local", "state", "stt")
	if cfg.Storage.TranscriptsDir != filepath.Join(wantState, "transcripts") {
		t.Fatalf("TranscriptsDir = %q, want stt state transcripts", cfg.Storage.TranscriptsDir)
	}
	if cfg.Storage.RegistryPath != filepath.Join(wantState, "registry.json") {
		t.Fatalf("RegistryPath = %q, want stt state registry", cfg.Storage.RegistryPath)
	}
	if cfg.Hotkey.CancelKey != "KEY_ESC" {
		t.Fatalf("CancelKey = %q, want KEY_ESC", cfg.Hotkey.CancelKey)
	}
	if cfg.Hotkey.QueryKey != "KEY_SLASH" {
		t.Fatalf("QueryKey = %q, want KEY_SLASH", cfg.Hotkey.QueryKey)
	}
	if cfg.Hotkey.FinalizeKey != "KEY_ENTER" {
		t.Fatalf("FinalizeKey = %q, want KEY_ENTER", cfg.Hotkey.FinalizeKey)
	}
	if cfg.Hotkey.CommandKey != "KEY_M" {
		t.Fatalf("CommandKey = %q, want KEY_M", cfg.Hotkey.CommandKey)
	}
	if cfg.Hotkey.HoldThresholdMs != MinHoldThresholdMs {
		t.Fatalf("HoldThresholdMs = %d, want %d", cfg.Hotkey.HoldThresholdMs, MinHoldThresholdMs)
	}
	if cfg.Hotkey.DoubleTapWindowMs != 300 {
		t.Fatalf("DoubleTapWindowMs = %d, want 300", cfg.Hotkey.DoubleTapWindowMs)
	}
	if cfg.Command.CodexCommand != "codex" || cfg.Command.TimeoutMs != 60000 {
		t.Fatalf("Command = %+v, want codex command and 60000ms timeout", cfg.Command)
	}
}

func TestLoadClampsHoldThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("hotkey:\n  hold_threshold_ms: 180\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hotkey.HoldThresholdMs != MinHoldThresholdMs {
		t.Fatalf("HoldThresholdMs = %d, want %d", cfg.Hotkey.HoldThresholdMs, MinHoldThresholdMs)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := ExpandPath("~/Projects/scriber"); got != filepath.Join(home, "Projects", "scriber") {
		t.Fatalf("ExpandPath() = %q", got)
	}
	if got := ExpandPath("/tmp/scriber"); got != "/tmp/scriber" {
		t.Fatalf("absolute path changed to %q", got)
	}
}
