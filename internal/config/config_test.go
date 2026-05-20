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
