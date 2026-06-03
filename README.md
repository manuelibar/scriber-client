# scriber-client (`stt`)

Go daemon + CLI for terminal-first STT. It listens on a global hotkey, captures mic audio, sends it to `scriber-server`, streams each checkpoint into the selected PTY, and keeps a hidden persisted buffer for Codex finalization. Streams are STT-owned PTY sessions with slot IDs and optional human names, so no terminal multiplexer is required.

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
stt attach --language es notes
stt attach [NAME] -- codex # run a command inside the managed terminal
stt detach NAME|SLOT      # remove a stream by name or slot number
stt stream set-slot NAME N
stt stream clear-slot NAME
stt select NAME           # select the one stream that receives staged text
stt cycle                 # rotate selection to next live stream
stt paste [N]             # stage the last N visible owned transcripts; default 1
stt fix --from A --to B --last N
stt history ls [STREAM]   # list recent owned transcript messages
stt history prune         # preview and delete transcript history after confirmation
stt monitor               # live daemon dashboard, streams, selected target, and audio level
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
  finalize_key: KEY_RIGHTSHIFT
  command_key: KEY_M
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

command_mode:
  codex_command: codex
  timeout_ms: 60000
```

The `command_mode` Codex settings also configure buffer finalization, where
chronological checkpoints are rewritten into clean visible text before the
finalized buffer is pasted.

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

Only the selected stream receives checkpoint text when recording stops. Each checkpoint is streamed immediately with a trailing space and appended to that stream's hidden buffer. Press right-Ctrl + right-Shift to run a headless `codex exec` finalization pass and persist the cleaned buffer text to visible history. Tap right-Shift a second time while still holding right-Ctrl to paste that finalized text into the stream's PTY.

Attach a stream with a language when that destination should transcribe in a
specific language. Locale values are normalized to Whisper language codes, so
`es-ES`, `es_AR`, and `es` all become `es`.

```bash
stt attach --language es-ES messages
stt attach --language en-US codex-main
stt attach --language auto scratch
```

Press right-Ctrl + M to toggle command mode. While command mode is on, normal
speech captures are transcribed as management commands for the selected stream's
buffer instead of new dictated text. The daemon runs a bounded headless
`codex exec` call to edit that persisted buffer, so commands like "delete the
last sentence" or "fix wrd to word" apply before the next finalization.

To stage recent visible dictation into the selected stream buffer:

```bash
stt paste      # stage the latest non-empty transcript
stt paste 3    # stage the last 3, oldest-to-newest, separated by spaces
```

Raw checkpoint diagnostics are not selected by `stt paste`, `stt history ls`, or
the monitor history window.

To move the last visible owned messages from one stream's history to another
and stage that text in the destination stream buffer:

```bash
stt fix --from notes --to codex-main --last 3
stt fix --to codex-main --last 1   # --from defaults visibly to the active stream
```

Fixing updates transcript history ownership and stages text in the
destination buffer. It does not try to remove text that was already streamed into
the source PTY.

To inspect persisted message ownership without needing the daemon:

```bash
stt history ls
stt history ls codex-main --limit 10
stt history ls notes --offset 10 --limit 10
```

`stt history ls` reads the persisted owned history immediately. `--offset`
skips newest matching messages first, so `--offset 10 --limit 10` shows the
next older page. Output is chronological inside each page, with the newest
message last. Use `--porcelain` for stable quoted lines.

`stt monitor` is dashboard-only by default. Use `stt monitor --history-limit 20` to opt into a small session history window, or `--history-stream NAME` to show one stream's session history.

To prune transcript JSON/WAV history:

```bash
stt history prune --dry-run
stt history prune --empty
stt history prune --older-than 7d --force
stt history prune --keep-last 50 --force
```

Without filters, `stt history prune` targets all transcript history in the configured transcript directory. It prints exact record/file/byte stats and asks for confirmation unless `--force` is passed. Useful filters include `--empty`, `--failed`, `--successful`, `--stream NAME`, `--before DATE`, `--older-than DURATION`, `--keep-last N`, and `--orphan-audio`.

## Hotkey behavior

- Hold the talk key (default `KEY_RIGHTCTRL`) for one second to record; release to transcribe and stage text in the selected stream buffer.
- Double-stroke the talk key to start a locked recording; stroke the talk key again to stop.
- Press the cancel key (default `KEY_ESC`) to discard the current capture.
- Tap the cycle key (default `KEY_RIGHTMETA`) to rotate the selected stream.
- Press right-Ctrl + F1-F9 to select an assigned stream; new streams take the first free slot automatically, and the chord cancels any capture started by that key press.
- Press right-Ctrl + / to show the selected target as a desktop notification; the chord also cancels any capture started by that key press.
- Press right-Ctrl + right-Shift to end and finalize the selected stream's hidden buffer. Tap right-Shift a second time while still holding right-Ctrl to paste that finalized text. This chord does not submit a newline.
- Press right-Ctrl + M to toggle command mode; spoken captures then edit the selected stream buffer.

## Operational checks

```bash
stt monitor --once
stt doctor
```
