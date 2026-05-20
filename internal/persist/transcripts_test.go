package persist

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSavePCM16WAV(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 15, 12, 30, 45, 123456000, time.UTC)
	pcm := []byte{0x01, 0x00, 0xff, 0x7f}

	path, err := SavePCM16WAV(dir, ts, pcm, 16000)
	if err != nil {
		t.Fatalf("SavePCM16WAV() error = %v", err)
	}
	if path != filepath.Join(dir, "20260515T123045.123456Z.wav") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wav: %v", err)
	}
	if len(data) != 44+len(pcm) {
		t.Fatalf("len(data) = %d, want %d", len(data), 44+len(pcm))
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[36:40]) != "data" {
		t.Fatalf("invalid wav header")
	}
	if got := data[44:]; string(got) != string(pcm) {
		t.Fatalf("pcm payload = %v, want %v", got, pcm)
	}
}
