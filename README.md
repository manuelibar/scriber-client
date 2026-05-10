# scriber-client

Go daemon + CLI for [scriber](../). Listens on a global hotkey, captures mic audio, sends it to `scriber-server`, and routes the transcript to the active tmux pane via `tmux send-keys`.

## Requirements

- Linux (Wayland or X11; only Wayland tested)
- evdev access — user must be in the `input` group: `sudo usermod -aG input $USER` then relogin
- `tmux` — multiplexer providing the only output target in v1
- `pipewire-pulse` — supplies the audio backend miniaudio talks to via PulseAudio compat
- `libnotify-bin` — for `notify-send`
- Optional: GNOME AppIndicator extension (the daemon does not need it; only the deferred tray-icon feature does)

## Build

The client is CGO-bound (uses `malgo`/miniaudio for in-process audio capture). Two paths:

### A. Docker build (no host Go/gcc needed)

```bash
make build-docker          # produces ./dist/scriber
make install               # copies to ~/.local/bin/scriber
```

### B. Native build

Requires Go 1.23+ and a C compiler (gcc / clang).

```bash
make build install
```

## Run

```bash
# foreground:
scriber daemon

# or as systemd user service:
cp systemd/scriber-daemon.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now scriber-daemon
journalctl --user -u scriber-daemon -f
```

## CLI

```
scriber daemon            # run the daemon
scriber attach [--alias N]  # register current $TMUX_PANE as a target
scriber detach [ALIAS|all]  # remove a target
scriber list              # show registered panes (active marked with *)
scriber switch ALIAS      # set active target
scriber cycle             # rotate active to next target
scriber status            # daemon state, last transcript, latencies
scriber doctor            # diagnose setup
```

## Config

`~/.config/scriber/config.yml`:

```yaml
hotkey:
  device: auto                # auto-discovers keyboard via evdev
  talk_key: KEY_RIGHTCTRL
  cycle_key: KEY_RIGHTMETA
  hold_threshold_ms: 180
  double_tap_window_ms: 350

audio:
  device: default
  sample_rate: 16000

server:
  url: http://127.0.0.1:8765
  timeout_ms: 5000

ui:
  beeps: false               # not yet implemented
  notifications: true
```

## Hotkey behavior

- **Hold** the talk key (default `KEY_RIGHTCTRL`) to record; release to transcribe.
- **Double-tap** the talk key to start a locked recording; single-tap to stop.
- **Tap** the cycle key (default `KEY_RIGHTMETA`) to rotate the active target.

Tap and hold are distinguished by a 180 ms threshold. Audio capture starts on every keydown into a ring buffer, so there's no perceived lag — the first 180 ms of speech is preserved when a tap turns into a hold.
