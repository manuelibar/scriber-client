package hotkey

import (
	"fmt"

	"github.com/holoplot/go-evdev"
)

// KeyByName maps human-friendly names (as found in /usr/include/linux/input-event-codes.h)
// to evdev key codes. Add entries as needed for new hotkey choices.
var KeyByName = map[string]evdev.EvCode{
	"KEY_LEFTCTRL":   evdev.KEY_LEFTCTRL,
	"KEY_RIGHTCTRL":  evdev.KEY_RIGHTCTRL,
	"KEY_LEFTSHIFT":  evdev.KEY_LEFTSHIFT,
	"KEY_RIGHTSHIFT": evdev.KEY_RIGHTSHIFT,
	"KEY_LEFTALT":    evdev.KEY_LEFTALT,
	"KEY_RIGHTALT":   evdev.KEY_RIGHTALT,
	"KEY_LEFTMETA":   evdev.KEY_LEFTMETA,
	"KEY_RIGHTMETA":  evdev.KEY_RIGHTMETA,
	"KEY_CAPSLOCK":   evdev.KEY_CAPSLOCK,
	"KEY_M":          evdev.KEY_M,
	"KEY_ESC":        evdev.KEY_ESC,
	"KEY_ESCAPE":     evdev.KEY_ESC,
	"KEY_ENTER":      evdev.KEY_ENTER,
	"KEY_FN":         evdev.KEY_FN,
	"KEY_0":          evdev.KEY_0,
	"KEY_1":          evdev.KEY_1,
	"KEY_2":          evdev.KEY_2,
	"KEY_3":          evdev.KEY_3,
	"KEY_4":          evdev.KEY_4,
	"KEY_5":          evdev.KEY_5,
	"KEY_6":          evdev.KEY_6,
	"KEY_7":          evdev.KEY_7,
	"KEY_8":          evdev.KEY_8,
	"KEY_9":          evdev.KEY_9,
	"KEY_SLASH":      evdev.KEY_SLASH,
	"KEY_F1":         evdev.KEY_F1,
	"KEY_F2":         evdev.KEY_F2,
	"KEY_F3":         evdev.KEY_F3,
	"KEY_F4":         evdev.KEY_F4,
	"KEY_F5":         evdev.KEY_F5,
	"KEY_F6":         evdev.KEY_F6,
	"KEY_F7":         evdev.KEY_F7,
	"KEY_F8":         evdev.KEY_F8,
	"KEY_F9":         evdev.KEY_F9,
	"KEY_F10":        evdev.KEY_F10,
	"KEY_F11":        evdev.KEY_F11,
	"KEY_F12":        evdev.KEY_F12,
}

func ParseKey(name string) (evdev.EvCode, error) {
	code, ok := KeyByName[name]
	if !ok {
		return 0, fmt.Errorf("unsupported key %q (add to internal/hotkey/keys.go)", name)
	}
	return code, nil
}
