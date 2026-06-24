package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scriber/internal/asr"
	"scriber/internal/config"
	"scriber/internal/hotkey"
	"scriber/internal/ipc"
	"scriber/internal/notify"
	"scriber/internal/output"
	"scriber/internal/persist"
)

func TestRouteCommandSelectSlotUpdatesRegistry(t *testing.T) {
	reg, path := registryWithSlotsAt(t)
	recorder := make(chan recorderCommand, 1)
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	locked := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionSelectSlot, Slot: 2}, recorder, reg, notify.NewWorker(context.Background(), false, 1), state, &locked)

	_, active, activeSlot := reg.Streams()
	if active != "notes" || activeSlot != 2 {
		t.Fatalf("active stream = %q slot=%d, want notes slot=2", active, activeSlot)
	}
	var persisted struct {
		ActiveStream string `json:"active_stream,omitempty"`
		ActiveSlot   int    `json:"active_slot,omitempty"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ActiveStream != "notes" || persisted.ActiveSlot != 2 {
		t.Fatalf("persisted active stream = %q slot=%d, want notes slot=2", persisted.ActiveStream, persisted.ActiveSlot)
	}
}

func TestRouteCommandCycleDoesNotUseRecorder(t *testing.T) {
	reg := registryWithSlots(t)
	recorder := make(chan recorderCommand, 1)
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	locked := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionCycleStream}, recorder, reg, notify.NewWorker(context.Background(), false, 1), state, &locked)

	if len(recorder) != 0 {
		t.Fatalf("cycle should not send recorder commands")
	}
	_, active, activeSlot := reg.Streams()
	if active != "notes" || activeSlot != 2 {
		t.Fatalf("active stream = %q slot=%d, want notes slot=2", active, activeSlot)
	}
}

func TestRouteCommandCaptureDoesNotBlockWhenRecorderQueueFull(t *testing.T) {
	reg := registryWithSlots(t)
	recorder := make(chan recorderCommand, 1)
	recorder <- recorderCommand{action: hotkey.ActionStartMomentaryCapture}
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	locked := false

	done := make(chan struct{})
	go func() {
		defer close(done)
		routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionStartMomentaryCapture}, recorder, reg, notify.NewWorker(context.Background(), false, 1), state, &locked)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("routeCommand blocked on a full recorder queue")
	}
}

func TestRouteCommandLockedToggleMapsToStartThenFinalize(t *testing.T) {
	reg := registryWithSlots(t)
	recorder := make(chan recorderCommand, 2)
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	locked := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionToggleLockedCapture}, recorder, reg, nil, state, &locked)
	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionToggleLockedCapture}, recorder, reg, nil, state, &locked)

	first := <-recorder
	second := <-recorder
	if first.action != hotkey.ActionStartMomentaryCapture || second.action != hotkey.ActionFinalizeCapture {
		t.Fatalf("recorder actions = %s then %s, want start then finalize", first.action, second.action)
	}
}

func TestFormatTargetNotificationWithoutActiveStream(t *testing.T) {
	title, body := formatTargetNotification(nil, "", 0)
	if title != "stt target" || body != "slot=- name=(none)" {
		t.Fatalf("target notification = %q / %q, want stt target / slot=- name=(none)", title, body)
	}
}

func TestASRTimeoutLeavesFailedRecordWithAudioPath(t *testing.T) {
	dir := t.TempDir()
	store := persist.NewCaptureStore(dir)
	writer, err := store.NewCapture(16000)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	meta, err := writer.FinalizeWithPCM(make([]byte, 320))
	if err != nil {
		t.Fatalf("FinalizeWithPCM() error = %v", err)
	}
	meta, err = store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
		next.Stage = persist.StageQueuedForASR
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Storage.TranscriptsDir = dir
	cfg.Server.URL = server.URL
	cfg.Server.TimeoutMs = 10
	state := &daemonState{fsmState: hotkey.StateIdle.String(), jobs: map[string]ipc.JobSnapshot{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan string, 1)
	delivery := make(chan string, 1)
	go asrWorkerLoop(ctx, store, asr.New(server.URL, 10*time.Millisecond), cfg, state, jobs, delivery)
	jobs <- meta.CaptureID

	waitFor(t, 300*time.Millisecond, func() bool {
		got, err := store.Read(meta.CaptureID)
		return err == nil && got.Stage == persist.StageFailed
	})
	failed, err := store.Read(meta.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.FailedStage != persist.StageTranscribing || !failed.Retryable || failed.AudioPath == "" {
		t.Fatalf("failed meta = %+v, want retryable Transcribing failure with audio path", failed)
	}
	records, err := persist.QueryHistory(dir, persist.HistoryQuery{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].AudioPath == "" || records[0].Error == "" {
		t.Fatalf("history records = %+v, want failed ASR record with audio path and error", records)
	}
}

func TestDeliveryWorkerInjectsTranscriptAndSavesVisibleHistory(t *testing.T) {
	dir := t.TempDir()
	store := persist.NewCaptureStore(dir)
	writer, err := store.NewCapture(16000)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	meta, err := writer.FinalizeWithPCM(make([]byte, 320))
	if err != nil {
		t.Fatalf("FinalizeWithPCM() error = %v", err)
	}
	meta, err = store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
		next.Stage = persist.StageQueuedForDelivery
		next.Transcript = "deliver this"
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Storage.TranscriptsDir = dir
	state := &daemonState{fsmState: hotkey.StateIdle.String(), jobs: map[string]ipc.JobSnapshot{}}
	var injected bytes.Buffer
	reg := registryWithInjectTarget(t, "codex", &injected)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan string, 1)
	go deliveryWorkerLoop(ctx, store, reg, cfg, state, jobs)
	jobs <- meta.CaptureID

	waitFor(t, 300*time.Millisecond, func() bool {
		got, err := store.Read(meta.CaptureID)
		return err == nil && got.Stage == persist.StageDelivered
	})
	if got := injected.String(); got != "deliver this " {
		t.Fatalf("injected text = %q, want delivered transcript with trailing space", got)
	}
	records, err := persist.QueryHistory(dir, persist.HistoryQuery{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Transcript != "deliver this" || records[0].Stage != persist.StageDelivered || records[0].Type != "transcript" || records[0].Mode != "pty" {
		t.Fatalf("history records = %+v, want visible delivered transcript history", records)
	}
	visible, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].Transcript != "deliver this" {
		t.Fatalf("visible history = %+v, want delivered transcript visible", visible)
	}
	last, _ := state.LastTranscript()
	if last != "deliver this" {
		t.Fatalf("last transcript = %q, want delivered transcript", last)
	}
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition was not met within %s", timeout)
}

func registryWithSlots(t *testing.T) *output.Registry {
	t.Helper()
	reg, _ := registryWithSlotsAt(t)
	return reg
}

func registryWithInjectTarget(t *testing.T, stream string, dst *bytes.Buffer) *output.Registry {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "target.sock")
	injectSrv, err := startInjectServer(socketPath, dst)
	if err != nil {
		t.Fatalf("startInjectServer() error = %v", err)
	}
	t.Cleanup(func() {
		_ = injectSrv.Shutdown(context.Background())
		_ = os.Remove(socketPath)
	})

	reg, err := output.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if _, _, err := reg.Attach(&ipc.AttachRequest{
		PID:        os.Getpid(),
		StreamName: stream,
		TargetType: ipc.TargetTypePTY,
		TargetRef:  socketPath,
	}); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	return reg
}

func registryWithSlotsAt(t *testing.T) (*output.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	state := struct {
		ActiveStream string       `json:"active_stream,omitempty"`
		Streams      []ipc.Stream `json:"streams,omitempty"`
	}{
		ActiveStream: "codex",
		Streams: []ipc.Stream{
			{
				ID:     "stream_codex",
				Name:   "codex",
				Slot:   1,
				Status: ipc.StreamStatusActive,
				Target: ipc.Target{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/codex.sock"},
			},
			{
				ID:     "stream_notes",
				Name:   "notes",
				Slot:   2,
				Status: ipc.StreamStatusActive,
				Target: ipc.Target{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/notes.sock"},
			},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := output.NewRegistry(path)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return reg, path
}
