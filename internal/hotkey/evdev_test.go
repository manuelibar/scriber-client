package hotkey

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/holoplot/go-evdev"
)

func TestListenAcceptsDeviceWithOnlyWatchedHotkey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dev := newFakeInputDevice("/dev/input/event-test", "macro pad", evdev.KEY_RIGHTCTRL)
	defer dev.Close()
	backend := newFakeInputBackend(dev)

	out, err := listenWithBackend(ctx, backend, map[evdev.EvCode]bool{evdev.KEY_RIGHTCTRL: true}, time.Hour)
	if err != nil {
		t.Fatalf("listenWithBackend() error = %v", err)
	}

	dev.events <- &evdev.InputEvent{Type: evdev.EV_KEY, Code: evdev.KEY_RIGHTCTRL, Value: 1}
	got := readEvent(t, out)
	if got.Kind != KeyDown || got.Code != evdev.KEY_RIGHTCTRL {
		t.Fatalf("event = %+v, want right-ctrl down", got)
	}
}

func TestListenReopensDeviceAfterReadError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newFakeInputDevice("/dev/input/event-test", "keyboard", evdev.KEY_RIGHTCTRL)
	second := newFakeInputDevice("/dev/input/event-test", "keyboard", evdev.KEY_RIGHTCTRL)
	defer first.Close()
	defer second.Close()
	backend := newFakeInputBackend(first, second)

	out, err := listenWithBackend(ctx, backend, map[evdev.EvCode]bool{evdev.KEY_RIGHTCTRL: true}, time.Hour)
	if err != nil {
		t.Fatalf("listenWithBackend() error = %v", err)
	}

	first.errs <- errors.New("device disappeared")
	waitUntil(t, func() bool { return backend.openCount(first.path) >= 2 })

	second.events <- &evdev.InputEvent{Type: evdev.EV_KEY, Code: evdev.KEY_RIGHTCTRL, Value: 1}
	got := readEvent(t, out)
	if got.Kind != KeyDown || got.Code != evdev.KEY_RIGHTCTRL {
		t.Fatalf("event after reopen = %+v, want right-ctrl down", got)
	}
}

func readEvent(t *testing.T, out <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-out:
		return ev
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for event")
	}
	return Event{}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition was not met before timeout")
}

type fakeInputBackend struct {
	mu      sync.Mutex
	paths   []evdev.InputPath
	devices map[string][]*fakeInputDevice
	opens   map[string]int
}

func newFakeInputBackend(devices ...*fakeInputDevice) *fakeInputBackend {
	b := &fakeInputBackend{
		devices: map[string][]*fakeInputDevice{},
		opens:   map[string]int{},
	}
	seen := map[string]bool{}
	for _, d := range devices {
		if !seen[d.path] {
			b.paths = append(b.paths, evdev.InputPath{Name: d.name, Path: d.path})
			seen[d.path] = true
		}
		b.devices[d.path] = append(b.devices[d.path], d)
	}
	return b
}

func (b *fakeInputBackend) ListDevicePaths() ([]evdev.InputPath, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]evdev.InputPath(nil), b.paths...), nil
}

func (b *fakeInputBackend) Open(path string) (inputDevice, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	devices := b.devices[path]
	if len(devices) == 0 {
		return nil, errors.New("no fake device")
	}
	dev := devices[0]
	b.devices[path] = devices[1:]
	b.opens[path]++
	return dev, nil
}

func (b *fakeInputBackend) openCount(path string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opens[path]
}

type fakeInputDevice struct {
	path      string
	name      string
	codes     []evdev.EvCode
	events    chan *evdev.InputEvent
	errs      chan error
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakeInputDevice(path, name string, codes ...evdev.EvCode) *fakeInputDevice {
	return &fakeInputDevice{
		path:   path,
		name:   name,
		codes:  append([]evdev.EvCode(nil), codes...),
		events: make(chan *evdev.InputEvent, 8),
		errs:   make(chan error, 1),
		closed: make(chan struct{}),
	}
}

func (d *fakeInputDevice) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func (d *fakeInputDevice) Name() (string, error) {
	return d.name, nil
}

func (d *fakeInputDevice) Path() string {
	return d.path
}

func (d *fakeInputDevice) CapableTypes() []evdev.EvType {
	return []evdev.EvType{evdev.EV_KEY}
}

func (d *fakeInputDevice) CapableEvents(t evdev.EvType) []evdev.EvCode {
	if t != evdev.EV_KEY {
		return nil
	}
	return append([]evdev.EvCode(nil), d.codes...)
}

func (d *fakeInputDevice) ReadOne() (*evdev.InputEvent, error) {
	select {
	case ev := <-d.events:
		return ev, nil
	case err := <-d.errs:
		return nil, err
	case <-d.closed:
		return nil, errors.New("closed")
	}
}
