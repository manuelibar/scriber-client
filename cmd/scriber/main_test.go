package main

import (
	"strings"
	"testing"
	"time"

	"scriber/internal/ipc"
)

func TestRootCommandUsesSTTNaming(t *testing.T) {
	root := newRootCmd()
	if root.Use != "stt" {
		t.Fatalf("root Use = %q, want stt", root.Use)
	}
	if root.Short == "" {
		t.Fatalf("root Short should be STT/stream oriented, got %q", root.Short)
	}
}

func TestRootCommandHasStreamCommands(t *testing.T) {
	root := newRootCmd()
	commands := map[string]bool{}
	for _, cmd := range root.Commands() {
		commands[cmd.Name()] = true
	}
	for _, want := range []string{"start", "shutdown", "attach", "stream", "select", "cycle", "paste", "redeem", "history", "monitor", "doctor"} {
		if !commands[want] {
			t.Fatalf("root command missing %q; got %v", want, commands)
		}
	}
	for _, removed := range []string{"streams", "status"} {
		if commands[removed] {
			t.Fatalf("root command still exposes removed command %q", removed)
		}
	}
}

func TestAttachAndSelectUsage(t *testing.T) {
	attach := attachCmd()
	if attach.Use != "attach [NAME] [-- COMMAND...]" {
		t.Fatalf("attach Use = %q, want attach [NAME] [-- COMMAND...]", attach.Use)
	}
	detach := detachCmd()
	if detach.Use != "detach [NAME|SLOT|all]" {
		t.Fatalf("detach Use = %q, want detach [NAME|SLOT|all]", detach.Use)
	}
	selectCmd := selectCmd()
	if selectCmd.Use != "select NAME" {
		t.Fatalf("select Use = %q, want select NAME", selectCmd.Use)
	}
}

func TestParseAttachArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		dash        int
		wantName    string
		wantCommand []string
		wantErr     bool
	}{
		{name: "no args", dash: -1},
		{name: "name only", args: []string{"scriber"}, dash: -1, wantName: "scriber"},
		{name: "command only after dash", args: []string{"codex"}, dash: 0, wantCommand: []string{"codex"}},
		{name: "name and command after dash", args: []string{"scriber", "codex"}, dash: 1, wantName: "scriber", wantCommand: []string{"codex"}},
		{name: "command without dash", args: []string{"scriber", "codex"}, dash: -1, wantErr: true},
		{name: "too many names before dash", args: []string{"one", "two", "codex"}, dash: 2, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotCommand, err := parseAttachArgs(tc.args, tc.dash)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseAttachArgs() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAttachArgs() error = %v", err)
			}
			if gotName != tc.wantName {
				t.Fatalf("stream name = %q, want %q", gotName, tc.wantName)
			}
			if len(gotCommand) != len(tc.wantCommand) {
				t.Fatalf("command = %v, want %v", gotCommand, tc.wantCommand)
			}
			for i := range gotCommand {
				if gotCommand[i] != tc.wantCommand[i] {
					t.Fatalf("command = %v, want %v", gotCommand, tc.wantCommand)
				}
			}
		})
	}
}

func TestParseSlotArg(t *testing.T) {
	slot, ok, err := parseSlotArg("1")
	if err != nil || !ok || slot != 1 {
		t.Fatalf("parseSlotArg(1) = %d %v %v, want 1 true nil", slot, ok, err)
	}
	if _, ok, err := parseSlotArg("notes"); err != nil || ok {
		t.Fatalf("parseSlotArg(notes) ok=%v err=%v, want false nil", ok, err)
	}
	if _, _, err := parseSlotArg("10"); err == nil {
		t.Fatalf("parseSlotArg(10) error = nil, want range error")
	}
}

func TestPasteUsage(t *testing.T) {
	paste := pasteCmd()
	if paste.Use != "paste [N]" {
		t.Fatalf("paste Use = %q, want paste [N]", paste.Use)
	}
	if got := decodeSeparator(`a\n\tb`); got != "a\n\tb" {
		t.Fatalf("decodeSeparator() = %q", got)
	}
}

func TestRedeemUsage(t *testing.T) {
	redeem := redeemCmd()
	if redeem.Use != "redeem --to DEST --last N [--from SOURCE]" {
		t.Fatalf("redeem Use = %q, want redeem --to DEST --last N [--from SOURCE]", redeem.Use)
	}
	for _, flag := range []string{"from", "to", "last", "separator"} {
		if redeem.Flags().Lookup(flag) == nil {
			t.Fatalf("redeem command missing %q flag", flag)
		}
	}
}

func TestHistoryPruneUsage(t *testing.T) {
	history := historyCmd()
	commands := map[string]bool{}
	for _, cmd := range history.Commands() {
		commands[cmd.Name()] = true
	}
	if !commands["prune"] {
		t.Fatalf("history command missing prune subcommand; got %v", commands)
	}
	if got, err := parseHistoryDuration("2d"); err != nil || got != 48*time.Hour {
		t.Fatalf("parseHistoryDuration(2d) = %v %v, want 48h nil", got, err)
	}
}

func TestStreamSetSlotUsage(t *testing.T) {
	stream := streamCmd()
	commands := map[string]bool{}
	for _, cmd := range stream.Commands() {
		commands[cmd.Name()] = true
	}
	if !commands["set-slot"] || !commands["clear-slot"] {
		t.Fatalf("stream command missing slot subcommands; got %v", commands)
	}
}

func TestMonitorUsageIncludesPorcelain(t *testing.T) {
	monitor := monitorCmd()
	if monitor.Flags().Lookup("porcelain") == nil {
		t.Fatalf("monitor command missing porcelain flag")
	}
}

func TestLevelMeter(t *testing.T) {
	if got := levelMeter(0, 5); got != "[-----]" {
		t.Fatalf("levelMeter(0, 5) = %q", got)
	}
	if got := levelMeter(1, 5); got != "[#####]" {
		t.Fatalf("levelMeter(1, 5) = %q", got)
	}
}

func TestRenderMonitorPorcelain(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	monitor := &ipc.MonitorResponse{
		State:            "Transcribing",
		PID:              1234,
		Active:           "kb",
		ActiveSlot:       1,
		RecordingMs:      2500,
		AudioLevel:       0.25,
		LastTranscript:   "hello\nworld",
		LastTranscriptAt: at,
		ServerOK:         true,
		Transcripts: []ipc.TranscriptEntry{
			{
				Timestamp:   at,
				Stream:      "kb",
				TargetType:  ipc.TargetTypePTY,
				TargetRef:   "pty-1",
				Mode:        "pty",
				Success:     true,
				AudioMs:     3000,
				InferenceMs: 75,
				Transcript:  "hello\nworld",
			},
		},
		Streams: []ipc.Stream{
			{
				ID:         "stream_kb",
				Name:       "kb",
				Slot:       1,
				Status:     ipc.StreamStatusActive,
				AttachedAt: at,
				LastUsedAt: at.Add(time.Second),
				Target: ipc.Target{
					TargetType: ipc.TargetTypePTY,
					TargetRef:  "pty-1",
					Label:      "kb",
					TTY:        "/dev/pts/1",
					CWD:        "/tmp/a b",
					PID:        4321,
					AttachedAt: at,
					LastSeenAt: at.Add(2 * time.Second),
				},
			},
		},
	}

	got := renderMonitorPorcelain(monitor)
	for _, want := range []string{
		`monitor version=1 state="Transcribing" pid=1234 server_ok=true active="kb" active_slot=1 recording_ms=2500 audio_level=0.250000 stream_count=1 transcript_count=1 transcript_tokens=2 transcript_audio_ms=3000`,
		`last_transcript ts="2026-01-02T03:04:05Z" text="hello\nworld"`,
		`stream index=0 active=true id="stream_kb" slot=1 name="kb" status="active" target_type="pty" target_ref="pty-1" target_label="kb" target_tty="/dev/pts/1" target_cwd="/tmp/a b" target_pid=4321`,
		`attached_at="2026-01-02T03:04:05Z" last_used_at="2026-01-02T03:04:06Z" target_attached_at="2026-01-02T03:04:05Z" target_last_seen_at="2026-01-02T03:04:07Z"`,
		`transcript index=0 ts="2026-01-02T03:04:05Z" stream="kb" target_type="pty" target_ref="pty-1" mode="pty" success=true audio_ms=3000 inference_ms=75 tokens=2 error="" text="hello\nworld"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("porcelain output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\033") {
		t.Fatalf("porcelain output should not contain ANSI escapes:\n%s", got)
	}
}

func TestRenderMonitorSnapshotIncludesDaemonAndStreams(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	monitor := &ipc.MonitorResponse{
		State:                   "Idle",
		PID:                     1234,
		Active:                  "kb",
		ActiveSlot:              1,
		AudioLevel:              0.25,
		LastTranscript:          "hello\nworld",
		LastTranscriptAt:        at,
		ServerOK:                true,
		TranscriptHistoryLoaded: true,
		Transcripts: []ipc.TranscriptEntry{
			{
				Timestamp:  at,
				Stream:     "kb",
				TargetRef:  "pty-1",
				Success:    true,
				Transcript: "hello\nworld",
			},
			{
				Timestamp:  at.Add(time.Second),
				Stream:     "old",
				TargetRef:  "gone",
				Success:    false,
				Error:      "send failed",
				Transcript: "detached",
			},
		},
		Streams: []ipc.Stream{
			{
				Name:       "kb",
				Slot:       1,
				Status:     ipc.StreamStatusActive,
				Target:     ipc.Target{TargetType: ipc.TargetTypePTY, TargetRef: "pty-1", PID: 4321},
				AttachedAt: at,
			},
			{
				Slot:       3,
				Status:     ipc.StreamStatusActive,
				Target:     ipc.Target{TargetType: ipc.TargetTypePTY, PID: 6789},
				AttachedAt: at,
			},
		},
	}

	got := renderMonitorSnapshot(monitor, false)
	for _, want := range []string{
		"state:         Idle",
		"daemon pid:    1234",
		"server:        ok",
		"active target: slot=1 name=kb",
		"audio level:",
		"session:       2 transcripts, ~3 tokens",
		"streams:",
		"* slot=1  name=kb",
		"msgs=1 tokens~2",
		"  slot=3  name=-",
		"session history:",
		"slot=1 name=kb",
		"--- 03:04:05 ok | ~2 tokens | talk=0.0s ---",
		"  hello",
		"  world",
		"unmatched stream",
		"--- 03:04:06 failed | ~1 tokens | talk=0.0s ---",
		"error: send failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("monitor snapshot missing %q:\n%s", want, got)
		}
	}
}
