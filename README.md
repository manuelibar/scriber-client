# scriber-client (`stt`)

Go daemon + CLI for terminal-first STT. It listens on a global hotkey, captures mic audio, sends it to `scriber-server`, and routes the final transcript to the selected named stream. Streams are STT-owned PTY sessions, so no terminal multiplexer is required.

Every capture is saved as diagnostic WAV/JSON data. The default directory is `~/.local/state/stt/transcripts/`; do not point it at the repo root except for a temporary debug run.

## Requirements

- Linux
- evdev access: `sudo usermod -aG input $USER`, then relogin
- `pipewire-pulse`
- `libnotify-bin`
- Go 1.23+ and gcc for native builds, or Docker for build-only client builds

## Build and install

```bash
make build        # produces ./dist/stt
make install      # installs ~/.local/bin/stt
```

Docker build path:

```bash
make build-docker
make install
```

This intentionally installs `stt`, not `scriber`, to avoid clobbering any existing Hermes `scriber` wrapper.

## Run

```bash
stt daemon
stt daemon --transcripts-dir ~/.local/state/stt/transcripts
```

Systemd user services are installed from the repo root:

```bash
cd ..
make services-install
make services-start
journalctl --user -u stt-daemon -f
```

## CLI

```bash
stt daemon [--transcripts-dir DIR]
stt attach NAME           # start an STT-managed terminal stream and select it
stt attach NAME -- codex  # run a command inside the managed terminal
stt detach NAME           # remove a stream
stt stream set-slot NAME N
stt stream clear-slot NAME
stt streams               # list streams; selected stream marked with *
stt select NAME           # select the one stream that receives final text
stt cycle                 # rotate selection to next live stream
stt status                # daemon state, active stream, server health
stt monitor               # live selected stream, duration, and audio level
stt doctor                # diagnose setup
```

## Config

Default config path: `~/.config/stt/config.yml`.

```yaml
hotkey:
  device: auto
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
  beeps: false
  notifications: true

storage:
  transcripts_dir: ~/.local/state/stt/transcripts
  registry_path: ~/.local/state/stt/registry.json
```

## Stream workflow

Start a managed terminal stream:

```bash
stt attach codex-main
```

This replaces the current terminal with a shell running inside an STT-owned PTY. Anything you run inside that shell, including `codex`, receives dictated text through the PTY input path.

From another terminal:

```bash
stt streams
```

Create another managed stream, then select where final dictation goes:

```bash
stt attach notes
stt stream set-slot codex-main 1
stt stream set-slot notes 2
stt select codex-main
```

Only the selected stream receives final text when recording stops. Scriber/STT never presses Enter; review before submitting.

## Hotkey behavior

- Hold the talk key (default `KEY_RIGHTCTRL`) to record; release to transcribe.
- Double-tap the talk key to start a locked recording; single-tap to stop.
- Tap the cycle key (default `KEY_RIGHTMETA`) to rotate the selected stream.
- Press right-Ctrl + number 1-9 to select a stream assigned with `stt stream set-slot NAME N`; the chord cancels any capture started by that key press.

## Operational checks

```bash
stt status
stt monitor
stt doctor
journalctl --user -u stt-daemon -f
```
