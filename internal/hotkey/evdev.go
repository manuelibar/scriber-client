package hotkey

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/holoplot/go-evdev"
)

type EventKind int

const (
	KeyDown EventKind = iota
	KeyUp
	Cancel
)

func (k EventKind) String() string {
	switch k {
	case KeyDown:
		return "down"
	case KeyUp:
		return "up"
	case Cancel:
		return "cancel"
	default:
		return "?"
	}
}

type Event struct {
	Kind EventKind
	Code evdev.EvCode
	At   time.Time
}

type inputBackend interface {
	ListDevicePaths() ([]evdev.InputPath, error)
	Open(path string) (inputDevice, error)
}

type inputDevice interface {
	Close() error
	Name() (string, error)
	Path() string
	CapableTypes() []evdev.EvType
	CapableEvents(evdev.EvType) []evdev.EvCode
	ReadOne() (*evdev.InputEvent, error)
}

type evdevBackend struct{}

func (evdevBackend) ListDevicePaths() ([]evdev.InputPath, error) {
	return evdev.ListDevicePaths()
}

func (evdevBackend) Open(path string) (inputDevice, error) {
	return evdev.OpenWithFlags(path, os.O_RDONLY)
}

const deviceScanInterval = 2 * time.Second

// Listen scans /dev/input/event*, opens every device that advertises at least
// one watched hotkey, and emits KeyDown/KeyUp events for those codes. Repeat
// events (value=2) are dropped. Devices are rescanned while the listener runs
// so USB reconnects, sleep/resume, and evdev renumbering do not require a daemon
// restart.
func Listen(ctx context.Context, watched map[evdev.EvCode]bool) (<-chan Event, error) {
	return listenWithBackend(ctx, evdevBackend{}, watched, deviceScanInterval)
}

func listenWithBackend(ctx context.Context, backend inputBackend, watched map[evdev.EvCode]bool, scanInterval time.Duration) (<-chan Event, error) {
	paths, err := backend.ListDevicePaths()
	if err != nil {
		return nil, fmt.Errorf("list device paths: %w", err)
	}

	out := make(chan Event, 64)
	done := make(chan string, 64)
	active := map[string]bool{}
	startDevice := func(p evdev.InputPath) bool {
		if active[p.Path] {
			return false
		}
		d, err := backend.Open(p.Path)
		if err != nil {
			return false
		}
		if !isHotkeyDevice(d, watched) {
			d.Close()
			return false
		}
		name, _ := d.Name()
		slog.Info("watching hotkey device", "path", p.Path, "name", name)
		active[p.Path] = true
		go func(path string) {
			readDevice(ctx, d, watched, out)
			select {
			case done <- path:
			case <-ctx.Done():
			}
		}(p.Path)
		return true
	}
	scan := func(paths []evdev.InputPath) int {
		started := 0
		for _, p := range paths {
			if startDevice(p) {
				started++
			}
		}
		return started
	}

	if scan(paths) == 0 {
		return nil, fmt.Errorf("no readable input devices advertise the configured hotkeys; check the 'input' group and hotkey config")
	}

	go func() {
		ticker := time.NewTicker(scanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case path := <-done:
				delete(active, path)
				paths, err := backend.ListDevicePaths()
				if err != nil {
					slog.Warn("evdev rescan failed", "err", err)
					continue
				}
				scan(paths)
			case <-ticker.C:
				paths, err := backend.ListDevicePaths()
				if err != nil {
					slog.Warn("evdev rescan failed", "err", err)
					continue
				}
				scan(paths)
			}
		}
	}()
	return out, nil
}

func isHotkeyDevice(d inputDevice, watched map[evdev.EvCode]bool) bool {
	for _, t := range d.CapableTypes() {
		if t == evdev.EV_KEY {
			for _, c := range d.CapableEvents(evdev.EV_KEY) {
				if watched[c] {
					return true
				}
			}
			return false
		}
	}
	return false
}

func readDevice(ctx context.Context, d inputDevice, watched map[evdev.EvCode]bool, out chan<- Event) {
	defer d.Close()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ev, err := d.ReadOne()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("evdev read error", "path", d.Path(), "err", err)
			}
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
