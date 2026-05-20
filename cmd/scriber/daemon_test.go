package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holoplot/go-evdev"

	"scriber/internal/hotkey"
	"scriber/internal/ipc"
	"scriber/internal/output"
)

func TestSplitEventsSlotChordCancelsTalkCaptureAndSelectsSlot(t *testing.T) {
	reg := registryWithSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan hotkey.Event, 3)
	out := make(chan hotkey.Event, 3)
	go splitEvents(ctx, in, evdev.KEY_RIGHTCTRL, evdev.KEY_RIGHTMETA, out, reg, false)

	now := time.Now()
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_2, At: now.Add(20 * time.Millisecond)}
	in <- hotkey.Event{Kind: hotkey.KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(40 * time.Millisecond)}
	close(in)

	var events []hotkey.Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("forwarded events = %+v, want talk down and cancel", events)
	}
	if events[0].Kind != hotkey.KeyDown || events[1].Kind != hotkey.Cancel {
		t.Fatalf("forwarded events = %+v, want keydown then cancel", events)
	}

	_, active := reg.Streams()
	if active != "notes" {
		t.Fatalf("active stream = %q, want notes", active)
	}
}

func registryWithSlots(t *testing.T) *output.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	state := struct {
		ActiveStream string       `json:"active_stream,omitempty"`
		Streams      []ipc.Stream `json:"streams,omitempty"`
	}{
		ActiveStream: "codex",
		Streams: []ipc.Stream{
			{
				ID:     "stream_codex",
				Name:   "codex",
				Slot:   1,
				Status: ipc.StreamStatusActive,
				Target: ipc.Target{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/codex.sock"},
			},
			{
				ID:     "stream_notes",
				Name:   "notes",
				Slot:   2,
				Status: ipc.StreamStatusActive,
				Target: ipc.Target{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/notes.sock"},
			},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := output.NewRegistry(path)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return reg
}
