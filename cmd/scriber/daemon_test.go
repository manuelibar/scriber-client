package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	commandMode := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionSelectSlot, Slot: 2}, recorder, nil, reg, notify.NewWorker(context.Background(), false, 1), state, &locked, &commandMode)

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
	commandMode := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionCycleStream}, recorder, nil, reg, notify.NewWorker(context.Background(), false, 1), state, &locked, &commandMode)

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
	commandMode := false

	done := make(chan struct{})
	go func() {
		defer close(done)
		routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionStartMomentaryCapture}, recorder, nil, reg, notify.NewWorker(context.Background(), false, 1), state, &locked, &commandMode)
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
	commandMode := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionToggleLockedCapture}, recorder, nil, reg, nil, state, &locked, &commandMode)
	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionToggleLockedCapture}, recorder, nil, reg, nil, state, &locked, &commandMode)

	first := <-recorder
	second := <-recorder
	if first.action != hotkey.ActionStartMomentaryCapture || second.action != hotkey.ActionFinalizeCapture {
		t.Fatalf("recorder actions = %s then %s, want start then finalize", first.action, second.action)
	}
}

func TestRouteCommandModeTagsCaptureWithActiveBuffer(t *testing.T) {
	reg := registryWithSlots(t)
	recorder := make(chan recorderCommand, 1)
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	locked := false
	commandMode := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionToggleCommandMode}, recorder, nil, reg, nil, state, &locked, &commandMode)
	if !commandMode {
		t.Fatalf("command mode active = false, want true")
	}
	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionStartMomentaryCapture}, recorder, nil, reg, nil, state, &locked, &commandMode)

	got := <-recorder
	if got.action != hotkey.ActionStartMomentaryCapture {
		t.Fatalf("action = %s, want start capture", got.action)
	}
	if got.options.Kind != persist.CaptureKindCommand || got.options.TargetStream != "codex" {
		t.Fatalf("capture options = %+v, want command capture for codex", got.options)
	}
}

func TestRouteCommandEndBufferQueuesPinnedStream(t *testing.T) {
	reg := registryWithSlots(t)
	recorder := make(chan recorderCommand, 1)
	bufferJobs := make(chan bufferJob, 1)
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	locked := false
	commandMode := false

	routeCommand(context.Background(), hotkey.Command{Action: hotkey.ActionEndBuffer}, recorder, bufferJobs, reg, nil, state, &locked, &commandMode)

	if len(recorder) != 0 {
		t.Fatalf("end buffer should not send recorder commands")
	}
	got := <-bufferJobs
	if got.kind != bufferJobKindEnd || got.stream != "codex" {
		t.Fatalf("buffer job = %+v, want end-buffer for codex", got)
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
	commandJobs := make(chan string, 1)
	go asrWorkerLoop(ctx, store, asr.New(server.URL, 10*time.Millisecond), cfg, state, jobs, delivery, commandJobs)
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

func TestDeliveryWorkerStreamsCheckpointAndHidesRawHistory(t *testing.T) {
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
		next.Transcript = "buffer should persist this"
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Storage.TranscriptsDir = dir
	bufferStore := persist.NewBufferStore(dir)
	state := &daemonState{fsmState: hotkey.StateIdle.String(), jobs: map[string]ipc.JobSnapshot{}}
	var injected bytes.Buffer
	reg := registryWithInjectTarget(t, "codex", &injected)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan string, 1)
	go deliveryWorkerLoop(ctx, store, bufferStore, reg, cfg, state, jobs)
	jobs <- meta.CaptureID

	waitFor(t, 300*time.Millisecond, func() bool {
		got, err := store.Read(meta.CaptureID)
		return err == nil && got.Stage == persist.StageBuffered
	})
	snapshot, err := bufferStore.Read("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "buffer should persist this" {
		t.Fatalf("buffer snapshot = %+v, want staged transcript", snapshot)
	}
	if got := injected.String(); got != "buffer should persist this " {
		t.Fatalf("injected text = %q, want raw checkpoint with trailing space", got)
	}
	records, err := persist.QueryHistory(dir, persist.HistoryQuery{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Transcript != "buffer should persist this" || records[0].Stage != persist.StageBuffered || records[0].Type != "checkpoint" || records[0].Mode != "checkpoint" {
		t.Fatalf("history records = %+v, want raw checkpoint diagnostic history", records)
	}
	visible, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 0 {
		t.Fatalf("visible history = %+v, want raw checkpoints hidden", visible)
	}
	last, _ := state.LastTranscript()
	if last != "" {
		t.Fatalf("last transcript = %q, want raw checkpoint hidden from monitor last transcript", last)
	}
}

func TestHandleEndBufferFinalizesWithoutInjecting(t *testing.T) {
	dir := t.TempDir()
	bufferStore := persist.NewBufferStore(dir)
	if _, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-1", Stream: "codex", Text: "one", AudioMs: 100, InferenceMs: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-2", Stream: "codex", Text: "two", AudioMs: 200, InferenceMs: 20}); err != nil {
		t.Fatal(err)
	}

	var injected bytes.Buffer
	reg := registryWithInjectTarget(t, "codex", &injected)

	finalizer := &fakeBufferFinalizer{text: "One, two.", explanation: "cleaned punctuation"}
	state := &daemonState{fsmState: hotkey.StateIdle.String()}
	finalized, ok := handleEndBuffer(context.Background(), bufferStore, reg, finalizer, dir, state, "codex")

	if !ok || finalized.stream != "codex" || finalized.text != "One, two." {
		t.Fatalf("finalized = %+v ok=%t, want codex final text", finalized, ok)
	}
	if got := injected.String(); got != "" {
		t.Fatalf("injected text = %q, want no injection while ending buffer", got)
	}
	if finalizer.snapshot.Stream != "codex" || len(finalizer.snapshot.Entries) != 2 {
		t.Fatalf("finalizer snapshot = %+v, want two codex entries", finalizer.snapshot)
	}
	snapshot, err := bufferStore.Read("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("buffer after finalize = %+v, want empty", snapshot.Entries)
	}
	visible, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{Stream: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].Transcript != "One, two." || visible[0].Mode != "codex-buffer" || visible[0].Stage != persist.StageBufferFinalized || visible[0].AudioMs != 300 || visible[0].InferenceMs != 30 {
		t.Fatalf("visible history = %+v, want finalized codex buffer record with summed metadata", visible)
	}
	last, _ := state.LastTranscript()
	if last != "One, two." {
		t.Fatalf("last transcript = %q, want finalized text", last)
	}
}

func TestHandleEndBufferKeepsBufferWhenFinalizeFails(t *testing.T) {
	dir := t.TempDir()
	bufferStore := persist.NewBufferStore(dir)
	if _, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-1", Stream: "codex", Text: "keep me"}); err != nil {
		t.Fatal(err)
	}

	var injected bytes.Buffer
	socketPath := filepath.Join(t.TempDir(), "target.sock")
	injectSrv, err := startInjectServer(socketPath, &injected)
	if err != nil {
		t.Fatalf("startInjectServer() error = %v", err)
	}
	defer func() {
		_ = injectSrv.Shutdown(context.Background())
		_ = os.Remove(socketPath)
	}()

	reg, err := output.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if _, _, err := reg.Attach(&ipc.AttachRequest{
		PID:        os.Getpid(),
		StreamName: "codex",
		TargetType: ipc.TargetTypePTY,
		TargetRef:  socketPath,
	}); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	finalized, ok := handleEndBuffer(context.Background(), bufferStore, reg, &fakeBufferFinalizer{err: errors.New("codex unavailable")}, dir, nil, "codex")

	if ok || finalized.text != "" {
		t.Fatalf("finalized = %+v ok=%t, want no finalized text on finalization failure", finalized, ok)
	}
	if got := injected.String(); got != "" {
		t.Fatalf("injected text = %q, want nothing on finalization failure", got)
	}
	snapshot, err := bufferStore.Read("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "keep me" {
		t.Fatalf("buffer after failed finalize = %+v, want original entry kept", snapshot.Entries)
	}
	visible, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 0 {
		t.Fatalf("visible history = %+v, want no finalized record on finalization failure", visible)
	}
}

func TestBufferWorkerPastesFinalizedTextAfterEnd(t *testing.T) {
	dir := t.TempDir()
	bufferStore := persist.NewBufferStore(dir)
	if _, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-1", Stream: "codex", Text: "run test"}); err != nil {
		t.Fatal(err)
	}

	var injected bytes.Buffer
	socketPath := filepath.Join(t.TempDir(), "target.sock")
	injectSrv, err := startInjectServer(socketPath, &injected)
	if err != nil {
		t.Fatalf("startInjectServer() error = %v", err)
	}
	defer func() {
		_ = injectSrv.Shutdown(context.Background())
		_ = os.Remove(socketPath)
	}()

	reg, err := output.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if _, _, err := reg.Attach(&ipc.AttachRequest{
		PID:        os.Getpid(),
		StreamName: "codex",
		TargetType: ipc.TargetTypePTY,
		TargetRef:  socketPath,
	}); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	finalizer := &fakeBufferFinalizer{text: "run test", started: started, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan bufferJob, 2)
	done := make(chan struct{})
	go func() {
		bufferWorkerLoop(ctx, bufferStore, reg, finalizer, dir, nil, jobs)
		close(done)
	}()

	jobs <- bufferJob{kind: bufferJobKindEnd, stream: "codex"}
	<-started
	jobs <- bufferJob{kind: bufferJobKindPasteFinalized, stream: "codex"}
	close(release)
	close(jobs)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("buffer worker did not drain queued finalize and paste")
	}
	if got := injected.String(); got != "run test" {
		t.Fatalf("injected text = %q, want finalized text without newline", got)
	}
}

func TestBufferWorkerFailedEndBufferDoesNotPastePreviousFinal(t *testing.T) {
	dir := t.TempDir()
	bufferStore := persist.NewBufferStore(dir)
	if _, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-1", Stream: "codex", Text: "first"}); err != nil {
		t.Fatal(err)
	}

	var injected bytes.Buffer
	reg := registryWithInjectTarget(t, "codex", &injected)

	finalizer := &fakeBufferFinalizer{text: "first final"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan bufferJob, 4)
	done := make(chan struct{})
	go func() {
		bufferWorkerLoop(ctx, bufferStore, reg, finalizer, dir, nil, jobs)
		close(done)
	}()

	jobs <- bufferJob{kind: bufferJobKindEnd, stream: "codex"}
	waitFor(t, 500*time.Millisecond, func() bool {
		snapshot, err := bufferStore.Read("codex")
		return err == nil && len(snapshot.Entries) == 0
	})
	if _, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-2", Stream: "codex", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	finalizer.err = errors.New("codex unavailable")
	jobs <- bufferJob{kind: bufferJobKindEnd, stream: "codex"}
	jobs <- bufferJob{kind: bufferJobKindPasteFinalized, stream: "codex"}
	close(jobs)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("buffer worker did not drain failed finalize and paste")
	}
	if got := injected.String(); got != "" {
		t.Fatalf("injected text = %q, want no stale finalized paste", got)
	}
	snapshot, err := bufferStore.Read("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "second" {
		t.Fatalf("buffer after failed finalize = %+v, want second entry intact", snapshot.Entries)
	}
}

type fakeBufferFinalizer struct {
	snapshot    persist.BufferSnapshot
	text        string
	explanation string
	err         error
	started     chan struct{}
	release     chan struct{}
}

func (f *fakeBufferFinalizer) Finalize(ctx context.Context, snapshot persist.BufferSnapshot) (string, string, error) {
	f.snapshot = snapshot
	if f.started != nil {
		close(f.started)
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", "", f.err
	}
	return f.text, f.explanation, nil
}

func TestCommandWorkerAppliesEditorToTargetBuffer(t *testing.T) {
	dir := t.TempDir()
	bufferStore := persist.NewBufferStore(dir)
	first, err := bufferStore.Append(persist.BufferEntry{CaptureID: "cap-1", Stream: "codex", Text: "wrong wrd"})
	if err != nil {
		t.Fatal(err)
	}
	store := persist.NewCaptureStore(dir)
	writer, err := store.NewCaptureWithOptions(16000, persist.CaptureOptions{
		Kind:         persist.CaptureKindCommand,
		TargetStream: "codex",
	})
	if err != nil {
		t.Fatalf("NewCaptureWithOptions() error = %v", err)
	}
	meta, err := writer.FinalizeWithPCM(make([]byte, 320))
	if err != nil {
		t.Fatalf("FinalizeWithPCM() error = %v", err)
	}
	meta, err = store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
		next.Stage = persist.StageQueuedForCommand
		next.Transcript = "fix wrd to word"
		next.TargetStream = "codex"
	})
	if err != nil {
		t.Fatal(err)
	}

	editor := &fakeBufferEditor{
		entries:     []persist.BufferEntry{{ID: first.ID, Stream: "codex", Text: "wrong word"}},
		explanation: "fixed typo",
	}
	cfg := config.Defaults()
	cfg.Storage.TranscriptsDir = dir
	state := &daemonState{fsmState: hotkey.StateIdle.String(), jobs: map[string]ipc.JobSnapshot{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan string, 1)
	go commandWorkerLoop(ctx, store, bufferStore, editor, cfg, state, jobs)
	jobs <- meta.CaptureID

	waitFor(t, 500*time.Millisecond, func() bool {
		got, err := store.Read(meta.CaptureID)
		return err == nil && got.Stage == persist.StageCommandApplied
	})
	snapshot, err := bufferStore.Read("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "wrong word" {
		t.Fatalf("buffer entries = %+v, want typo fixed", snapshot.Entries)
	}
	if editor.command != "fix wrd to word" || editor.snapshot.Stream != "codex" {
		t.Fatalf("editor saw command=%q snapshot=%+v, want codex command", editor.command, editor.snapshot)
	}
	records, err := persist.QueryHistory(dir, persist.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("QueryHistory() records = %+v, want command capture excluded", records)
	}
	allRecords, err := persist.QueryHistory(dir, persist.HistoryQuery{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(allRecords) != 1 || allRecords[0].Type != "command" || allRecords[0].Mode != "command" {
		t.Fatalf("all history records = %+v, want command audit record", allRecords)
	}
}

type fakeBufferEditor struct {
	snapshot    persist.BufferSnapshot
	command     string
	entries     []persist.BufferEntry
	explanation string
	err         error
}

func (f *fakeBufferEditor) Edit(ctx context.Context, snapshot persist.BufferSnapshot, commandText string) ([]persist.BufferEntry, string, error) {
	f.snapshot = snapshot
	f.command = commandText
	if f.err != nil {
		return nil, "", f.err
	}
	return f.entries, f.explanation, nil
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
