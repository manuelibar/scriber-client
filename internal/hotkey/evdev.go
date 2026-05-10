package hotkey

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/holoplot/go-evdev"
)

type EventKind int

const (
	KeyDown EventKind = iota
	KeyUp
)

func (k EventKind) String() string {
	switch k {
	case KeyDown:
		return "down"
	case KeyUp:
		return "up"
	default:
		return "?"
	}
}

type Event struct {
	Kind EventKind
	Code evdev.EvCode
	At   time.Time
}

// Listen scans /dev/input/event*, opens every keyboard-capable device, and emits
// KeyDown/KeyUp events for codes in `watched`. Repeat events (value=2) are dropped.
// Returns an error if no keyboard devices are accessible (typically: not in input group).
func Listen(ctx context.Context, watched map[evdev.EvCode]bool) (<-chan Event, error) {
	paths, err := evdev.ListDevicePaths()
	if err != nil {
		return nil, fmt.Errorf("list device paths: %w", err)
	}

	out := make(chan Event, 64)
	started := 0
	for _, p := range paths {
		d, err := evdev.Open(p.Path)
		if err != nil {
			continue
		}
		if !isKeyboard(d) {
			d.Close()
			continue
		}
		name, _ := d.Name()
		slog.Info("watching keyboard", "path", p.Path, "name", name)
		started++
		go readDevice(ctx, d, watched, out)
	}
	if started == 0 {
		return nil, fmt.Errorf("no keyboard devices accessible — is your user in the 'input' group? (sudo usermod -aG input $USER && relogin)")
	}
	return out, nil
}

func isKeyboard(d *evdev.InputDevice) bool {
	hasKey := false
	for _, t := range d.CapableTypes() {
		if t == evdev.EV_KEY {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return false
	}
	// Heuristic: real keyboards expose typical alphabetic key codes.
	hasA, hasSpace := false, false
	for _, c := range d.CapableEvents(evdev.EV_KEY) {
		switch c {
		case evdev.KEY_A:
			hasA = true
		case evdev.KEY_SPACE:
			hasSpace = true
		}
	}
	return hasA && hasSpace
}

func readDevice(ctx context.Context, d *evdev.InputDevice, watched map[evdev.EvCode]bool, out chan<- Event) {
	defer d.Close()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ev, err := d.ReadOne()
		if err != nil {
			slog.Warn("evdev read error", "err", err)
			return
		}
		if ev.Type != evdev.EV_KEY {
			continue
		}
		if !watched[ev.Code] {
			continue
		}
		var kind EventKind
		switch ev.Value {
		case 0:
			kind = KeyUp
		case 1:
			kind = KeyDown
		default:
			continue
		}
		select {
		case out <- Event{Kind: kind, Code: ev.Code, At: time.Now()}:
		case <-ctx.Done():
			return
		}
	}
}
