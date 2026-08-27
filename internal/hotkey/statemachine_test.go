package hotkey

import (
	"context"
	"testing"
	"time"

	"github.com/holoplot/go-evdev"
)

func TestGestureRecognizerHoldStartsAndReleaseFinalizes(t *testing.T) {
	in, out, stop := startTestRecognizer(t)
	defer stop()

	now := time.Now()
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	got := readCommand(t, out)
	if got.Action != ActionStartMomentaryCapture {
		t.Fatalf("hold command = %s, want StartMomentaryCapture", got.Action)
	}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(20 * time.Millisecond)}
	got = readCommand(t, out)
	if got.Action != ActionFinalizeCapture {
		t.Fatalf("release command = %s, want FinalizeCapture", got.Action)
	}
}

func TestGestureRecognizerDoubleStrokeTogglesLockedCapture(t *testing.T) {
	in, out, stop := startTestRecognizer(t)
	defer stop()

	now := time.Now()
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(time.Millisecond)}
	assertNoCommand(t, out, 8*time.Millisecond)
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now.Add(2 * time.Millisecond)}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(3 * time.Millisecond)}
	got := readCommand(t, out)
	if got.Action != ActionToggleLockedCapture {
		t.Fatalf("double-stroke command = %s, want ToggleLockedCapture", got.Action)
	}

	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now.Add(40 * time.Millisecond)}
	got = readCommand(t, out)
	if got.Action != ActionToggleLockedCapture {
		t.Fatalf("locked stop command = %s, want ToggleLockedCapture", got.Action)
	}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(41 * time.Millisecond)}
	assertNoCommand(t, out, 20*time.Millisecond)
}

func TestGestureRecognizerSlotChordSuppressesTalkKeyRelease(t *testing.T) {
	in, out, stop := startTestRecognizer(t)
	defer stop()

	now := time.Now()
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- Event{Kind: KeyDown, Code: evdev.KEY_F2, At: now.Add(time.Millisecond)}
	got := readCommand(t, out)
	if got.Action != ActionSelectSlot || got.Slot != 2 {
		t.Fatalf("slot chord command = %+v, want SelectSlot(2)", got)
	}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(2 * time.Millisecond)}
	assertNoCommand(t, out, 20*time.Millisecond)
}

func TestGestureRecognizerTargetQueryChordSuppressesTalkKeyRelease(t *testing.T) {
	in, out, stop := startTestRecognizer(t)
	defer stop()

	now := time.Now()
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- Event{Kind: KeyDown, Code: evdev.KEY_SLASH, At: now.Add(time.Millisecond)}
	got := readCommand(t, out)
	if got.Action != ActionReportActiveStream {
		t.Fatalf("query chord command = %s, want ReportActiveStream", got.Action)
	}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(2 * time.Millisecond)}
	assertNoCommand(t, out, 20*time.Millisecond)
}

func TestGestureRecognizerCancelDiscardsActiveCapture(t *testing.T) {
	in, out, stop := startTestRecognizer(t)
	defer stop()

	now := time.Now()
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	got := readCommand(t, out)
	if got.Action != ActionStartMomentaryCapture {
		t.Fatalf("hold command = %s, want StartMomentaryCapture", got.Action)
	}
	in <- Event{Kind: KeyDown, Code: evdev.KEY_ESC, At: now.Add(20 * time.Millisecond)}
	got = readCommand(t, out)
	if got.Action != ActionDiscardCapture {
		t.Fatalf("cancel command = %s, want DiscardCapture", got.Action)
	}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(21 * time.Millisecond)}
	assertNoCommand(t, out, 20*time.Millisecond)
}

func TestEscDiscardsLockedCapture(t *testing.T) {
	in, out, stop := startTestRecognizer(t)
	defer stop()

	now := time.Now()
	// Double-tap to enter locked capture.
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(time.Millisecond)}
	in <- Event{Kind: KeyDown, Code: evdev.KEY_RIGHTCTRL, At: now.Add(2 * time.Millisecond)}
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(3 * time.Millisecond)}
	got := readCommand(t, out)
	if got.Action != ActionToggleLockedCapture {
		t.Fatalf("setup: got %s, want ToggleLockedCapture", got.Action)
	}

	// ESC discards the locked capture.
	in <- Event{Kind: KeyDown, Code: evdev.KEY_ESC, At: now.Add(100 * time.Millisecond)}
	got = readCommand(t, out)
	if got.Action != ActionDiscardCapture {
		t.Fatalf("cancel: got %s, want DiscardCapture", got.Action)
	}

	// Talk key release after cancel is suppressed.
	in <- Event{Kind: KeyUp, Code: evdev.KEY_RIGHTCTRL, At: now.Add(101 * time.Millisecond)}
	assertNoCommand(t, out, 20*time.Millisecond)
}

func startTestRecognizer(t *testing.T) (chan Event, chan Command, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan Event, 16)
	out := make(chan Command, 16)
	go RunRecognizer(ctx, FSMConfig{
		HoldThreshold:   5 * time.Millisecond,
		DoubleTapWindow: 40 * time.Millisecond,
		TalkKey:         evdev.KEY_RIGHTCTRL,
		CancelKey:       evdev.KEY_ESC,
		QueryKey:        evdev.KEY_SLASH,
		CycleKey:        evdev.KEY_RIGHTMETA,
		SlotKeys:        DefaultSlotKeys(),
	}, in, out)
	return in, out, cancel
}

func readCommand(t *testing.T, out <-chan Command) Command {
	t.Helper()
	select {
	case cmd := <-out:
		return cmd
	case <-time.After(120 * time.Millisecond):
		t.Fatalf("timed out waiting for command")
	}
	return Command{}
}

func assertNoCommand(t *testing.T, out <-chan Command, d time.Duration) {
	t.Helper()
	select {
	case cmd := <-out:
		t.Fatalf("unexpected command: %+v", cmd)
	case <-time.After(d):
	}
}
