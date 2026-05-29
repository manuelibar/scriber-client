# scriber-client (`stt`)

Go daemon + CLI for terminal-first STT. It listens on a global hotkey, captures mic audio, sends it to `scriber-server`, and routes the final transcript to the selected stream. Streams are STT-owned PTY sessions with slot IDs and optional human names, so no terminal multiplexer is required.

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

## Run

```bash
stt start
stt attach [NAME]
```

Stop everything started by `stt start`:

```bash
stt shutdown
```

For foreground daemon debugging, run `stt daemon` directly.

## CLI

```bash
stt start                  # start Docker backend and host daemon
stt shutdown               # gracefully stop daemon and Docker backend
stt daemon [--transcripts-dir DIR]
stt attach [NAME]         # start an STT-managed terminal stream and select it
stt attach [NAME] -- codex # run a command inside the managed terminal
stt detach NAME|SLOT      # remove a stream by name or slot number
stt stream set-slot NAME N
stt stream clear-slot NAME
stt select NAME           # select the one stream that receives final text
stt cycle                 # rotate selection to next live stream
stt paste [N]             # paste the last N non-empty transcripts; default 1
stt redeem --from A --to B --last N
stt history prune         # preview and delete transcript history after confirmation
stt monitor               # live daemon state, streams, selected target, session history, and audio level
stt monitor --once        # print one combined snapshot and exit
stt doctor                # diagnose setup
```

## Config

Default config path: `~/.config/stt/config.yml`.

```yaml
hotkey:
  device: auto
  talk_key: KEY_RIGHTCTRL
  cycle_key: KEY_RIGHTMETA
  cancel_key: KEY_ESC
  query_key: KEY_SLASH
  hold_threshold_ms: 1000
  double_tap_window_ms: 300

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

This replaces the current terminal with a shell running inside an STT-owned PTY. If `NAME` is omitted, the stream remains unnamed and is identified by its assigned slot. Anything you run inside that shell, including `codex`, receives dictated text through the PTY input path.

From another terminal:

```bash
stt monitor --once
```

Create another managed stream, then select where final dictation goes:

```bash
stt attach notes
stt stream set-slot codex-main 1
stt stream set-slot notes 2
stt select codex-main
stt detach 2
```

Only the selected stream receives final text when recording stops. Scriber/STT never presses Enter; review before submitting.

To replay recent dictation into the selected stream:

```bash
stt paste      # paste the latest non-empty transcript
stt paste 3    # paste the last 3, oldest-to-newest, separated by spaces
```

To move the last delivered messages from one stream's history to another and
paste that text into the destination stream:

```bash
stt redeem --from notes --to codex-main --last 3
stt redeem --to codex-main --last 1   # --from defaults visibly to the active stream
```

Redemption updates transcript history ownership and pastes into the destination
PTY. It does not try to remove text that was already pasted into the source PTY.

`stt monitor` starts its transcript history at monitor launch, keeps the stats/stream header visible, and stacks a sliding history window under each attached terminal. Entries are separated by dashed timestamp/status lines. Use `stt monitor --history-limit 20` to keep only 20 session records, `--history-limit 0` to hide history, or `--history-stream NAME` to show one stream's session history.

To prune transcript JSON/WAV history:

```bash
stt history prune --dry-run
stt history prune --empty
stt history prune --older-than 7d --force
stt history prune --keep-last 50 --force
```

Without filters, `stt history prune` targets all transcript history in the configured transcript directory. It prints exact record/file/byte stats and asks for confirmation unless `--force` is passed. Useful filters include `--empty`, `--failed`, `--successful`, `--stream NAME`, `--before DATE`, `--older-than DURATION`, `--keep-last N`, and `--orphan-audio`.

## Hotkey behavior

- Hold the talk key (default `KEY_RIGHTCTRL`) for one second to record; release to transcribe.
- Double-stroke the talk key to start a locked recording; stroke the talk key again to stop.
- Press the cancel key (default `KEY_ESC`) to discard the current capture.
- Tap the cycle key (default `KEY_RIGHTMETA`) to rotate the selected stream.
- Press right-Ctrl + F1-F9 to select an assigned stream; new streams take the first free slot automatically, and the chord cancels any capture started by that key press.
- Press right-Ctrl + / to show the selected target as a desktop notification; the chord also cancels any capture started by that key press.

## Operational checks

```bash
stt monitor --once
stt doctor
```
