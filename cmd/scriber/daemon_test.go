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
	reg, path := registryWithSlotsAt(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	originalNotify := notifySend
	t.Cleanup(func() { notifySend = originalNotify })
	type notification struct {
		title string
		body  string
	}
	notifications := make(chan notification, 2)
	notifySend = func(title, body string) {
		notifications <- notification{title: title, body: body}
	}

	in := make(chan hotkey.Event, 4)
	out := make(chan hotkey.Event, 3)
	go splitEvents(ctx, in, evdev.KEY_RIGHTCTRL, evdev.KEY_RIGHTMETA, evdev.KEY_ESC, out, reg, true)

	now := time.Now()
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_2, At: now.Add(20 * time.Millisecond)}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_2, At: now.Add(25 * time.Millisecond)}
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

	select {
	case got := <-notifications:
		if got.title != "stt slot 2 → notes" || got.body != "" {
			t.Fatalf("notification = %+v, want stt slot 2 → notes / empty", got)
		}
	default:
		t.Fatalf("notification was not sent")
	}
	select {
	case got := <-notifications:
		t.Fatalf("unexpected duplicate notification: %+v", got)
	default:
	}

	_, active, activeSlot := reg.Streams()
	if active != "notes" || activeSlot != 2 {
		t.Fatalf("active stream = %q slot=%d, want notes slot=2", active, activeSlot)
	}

	var persisted struct {
		ActiveStream string `json:"active_stream,omitempty"`
		ActiveSlot   int    `json:"active_slot,omitempty"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ActiveStream != "notes" || persisted.ActiveSlot != 2 {
		t.Fatalf("persisted active stream = %q slot=%d, want notes slot=2", persisted.ActiveStream, persisted.ActiveSlot)
	}
}

func TestSplitEventsTargetChordCancelsTalkCaptureAndNotifiesTarget(t *testing.T) {
	reg := registryWithSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	originalNotify := notifySend
	t.Cleanup(func() { notifySend = originalNotify })
	type notification struct {
		title string
		body  string
	}
	notifications := make(chan notification, 2)
	notifySend = func(title, body string) {
		notifications <- notification{title: title, body: body}
	}

	in := make(chan hotkey.Event, 4)
	out := make(chan hotkey.Event, 3)
	go splitEvents(ctx, in, evdev.KEY_RIGHTCTRL, evdev.KEY_RIGHTMETA, evdev.KEY_ESC, out, reg, true)

	now := time.Now()
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_0, At: now.Add(20 * time.Millisecond)}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_0, At: now.Add(25 * time.Millisecond)}
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

	select {
	case got := <-notifications:
		if got.title != "stt target" || got.body != "slot=1 name=codex" {
			t.Fatalf("notification = %+v, want stt target / slot=1 name=codex", got)
		}
	default:
		t.Fatalf("notification was not sent")
	}
	select {
	case got := <-notifications:
		t.Fatalf("unexpected duplicate notification: %+v", got)
	default:
	}
}

func TestSplitEventsCycleDoesNotNotify(t *testing.T) {
	reg := registryWithSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	originalNotify := notifySend
	t.Cleanup(func() { notifySend = originalNotify })
	notifications := make(chan struct{}, 1)
	notifySend = func(title, body string) {
		notifications <- struct{}{}
	}

	in := make(chan hotkey.Event, 3)
	out := make(chan hotkey.Event, 3)
	go splitEvents(ctx, in, evdev.KEY_RIGHTCTRL, evdev.KEY_RIGHTMETA, evdev.KEY_ESC, out, reg, true)

	now := time.Now()
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTMETA, At: now}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTMETA, At: now.Add(5 * time.Millisecond)}
	in <- hotkey.Event{Kind: hotkey.KeyUp, Code: evdev.KEY_RIGHTMETA, At: now.Add(20 * time.Millisecond)}
	close(in)

	for range out {
	}

	select {
	case <-notifications:
		t.Fatalf("cycle should not notify")
	default:
	}
	_, active, activeSlot := reg.Streams()
	if active != "notes" || activeSlot != 2 {
		t.Fatalf("active stream = %q slot=%d, want notes slot=2", active, activeSlot)
	}
}

func TestFormatTargetNotificationWithoutActiveStream(t *testing.T) {
	title, body := formatTargetNotification(nil, "", 0)
	if title != "stt target" || body != "slot=- name=(none)" {
		t.Fatalf("target notification = %q / %q, want stt target / slot=- name=(none)", title, body)
	}
}

func TestSplitEventsEscapeCancelsTalkCapture(t *testing.T) {
	reg := registryWithSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan hotkey.Event, 3)
	out := make(chan hotkey.Event, 3)
	go splitEvents(ctx, in, evdev.KEY_RIGHTCTRL, evdev.KEY_RIGHTMETA, evdev.KEY_ESC, out, reg, false)

	now := time.Now()
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_ESC, At: now.Add(20 * time.Millisecond)}
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
}

func TestSplitEventsBackspaceDoesNotCancelOrClear(t *testing.T) {
	reg := registryWithSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan hotkey.Event, 3)
	out := make(chan hotkey.Event, 3)
	go splitEvents(ctx, in, evdev.KEY_RIGHTCTRL, evdev.KEY_RIGHTMETA, evdev.KEY_ESC, out, reg, false)

	now := time.Now()
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- hotkey.Event{Kind: hotkey.KeyDown, Code: evdev.KEY_BACKSPACE, At: now.Add(20 * time.Millisecond)}
	in <- hotkey.Event{Kind: hotkey.KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(40 * time.Millisecond)}
	close(in)

	var events []hotkey.Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("forwarded events = %+v, want talk down and talk up only", events)
	}
	if events[0].Kind != hotkey.KeyDown || events[1].Kind != hotkey.KeyUp {
		t.Fatalf("forwarded events = %+v, want keydown then keyup", events)
	}
	_, active, activeSlot := reg.Streams()
	if active != "codex" || activeSlot != 1 {
		t.Fatalf("active stream = %q slot=%d, want codex slot=1", active, activeSlot)
	}
}

func registryWithSlots(t *testing.T) *output.Registry {
	t.Helper()
	reg, _ := registryWithSlotsAt(t)
	return reg
}

func registryWithSlotsAt(t *testing.T) (*output.Registry, string) {
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
	return reg, path
}
