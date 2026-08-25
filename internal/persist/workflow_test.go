package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureStoreRecoverInflightAudioQueuesASR(t *testing.T) {
	store := NewCaptureStore(t.TempDir())
	writer, err := store.NewCapture(16000)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	pcm := make([]byte, 320)
	pcm[0] = 0x01
	pcm[2] = 0x02
	if err := writer.WriteChunk(pcm); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}
	writer.mu.Lock()
	if err := writer.file.Close(); err != nil {
		t.Fatal(err)
	}
	writer.closed = true
	writer.file = nil
	writer.mu.Unlock()

	plan, err := store.Recover(2)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(plan.ASR) != 1 || plan.ASR[0].Stage != StageAudioFinalized {
		t.Fatalf("ASR plan = %+v, want one AudioFinalized capture", plan.ASR)
	}
	meta := plan.ASR[0]
	if meta.PCMBytes != int64(len(pcm)) || meta.AudioMs == 0 {
		t.Fatalf("recovered bytes=%d audio_ms=%d, want nonzero finalized audio", meta.PCMBytes, meta.AudioMs)
	}
	for _, path := range []string{meta.PCMPath, meta.AudioPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected recovered artifact %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.Root, meta.CaptureID, CaptureInflightPCM)); !os.IsNotExist(err) {
		t.Fatalf("inflight PCM should be renamed away, stat err=%v", err)
	}
}

func TestCaptureStoreRecoverShortInflightAudioFails(t *testing.T) {
	store := NewCaptureStore(t.TempDir())
	writer, err := store.NewCapture(16000)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	if err := writer.WriteChunk([]byte{0x01}); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}
	writer.mu.Lock()
	if err := writer.file.Close(); err != nil {
		t.Fatal(err)
	}
	writer.closed = true
	writer.file = nil
	writer.mu.Unlock()

	plan, err := store.Recover(2)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(plan.Failed) != 1 || plan.Failed[0].Stage != StageFailed || plan.Failed[0].FailedStage != StageRecording {
		t.Fatalf("failed plan = %+v, want one failed recording", plan.Failed)
	}
}

func TestFixMovesOwnershipWithAppendOnlyRecord(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base, MessageID: "a1", TargetStream: "notes", Transcript: "one", Success: true, Mode: "pty"},
		{Timestamp: base.Add(time.Second), MessageID: "a2", TargetStream: "notes", Transcript: "two", Success: true, Mode: "pty"},
		{Timestamp: base.Add(2 * time.Second), MessageID: "b1", TargetStream: "codex-main", Transcript: "existing", Success: true, Mode: "pty"},
	}
	for _, rec := range records {
		if err := writeRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
	}

	selection, err := SelectFixMessages(dir, "notes", 1, "\n")
	if err != nil {
		t.Fatalf("SelectFixMessages() error = %v", err)
	}
	if selection.Text != "two" || len(selection.Messages) != 1 || selection.Messages[0].MessageID != "a2" {
		t.Fatalf("selection = %+v, want message a2 text two", selection)
	}
	if _, err := SaveFix(dir, "notes", "codex-main", selection.Messages, selection.Text); err != nil {
		t.Fatalf("SaveFix() error = %v", err)
	}

	notes, err := QueryOwnedHistory(dir, HistoryQuery{Stream: "notes"})
	if err != nil {
		t.Fatalf("QueryOwnedHistory(notes) error = %v", err)
	}
	if len(notes) != 1 || notes[0].MessageID != "a1" {
		t.Fatalf("notes owned records = %+v, want only a1", notes)
	}
	codex, err := QueryOwnedHistory(dir, HistoryQuery{Stream: "codex-main"})
	if err != nil {
		t.Fatalf("QueryOwnedHistory(codex-main) error = %v", err)
	}
	if len(codex) != 2 || codex[0].MessageID != "a2" || codex[0].FixedFrom != "notes" || codex[0].FixedTo != "codex-main" || codex[1].MessageID != "b1" {
		t.Fatalf("codex owned records = %+v, want fixed a2 plus existing", codex)
	}
}

func TestCaptureStoreRecoverIgnoresTerminalFailedCaptures(t *testing.T) {
	store := NewCaptureStore(t.TempDir())
	writer, err := store.NewCapture(16000)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	meta, err := writer.FinalizeWithPCM(make([]byte, 320))
	if err != nil {
		t.Fatalf("FinalizeWithPCM() error = %v", err)
	}
	if _, err := store.Fail(meta.CaptureID, StageTranscribing, fmt.Errorf("empty text"), false); err != nil {
		t.Fatal(err)
	}

	plan, err := store.Recover(2)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(plan.Failed) != 0 {
		t.Fatalf("failed recovery = %+v, want none", plan.Failed)
	}
	if len(plan.ASR) != 0 {
		t.Fatalf("ASR recovery = %+v, want none", plan.ASR)
	}
	if len(plan.Delivery) != 0 {
		t.Fatalf("delivery recovery = %+v, want none", plan.Delivery)
	}
}

func TestCaptureStoreRecoverRetriesRetryableFailedCaptures(t *testing.T) {
	store := NewCaptureStore(t.TempDir())
	writer, err := store.NewCapture(16000)
	if err != nil {
		t.Fatalf("NewCapture() error = %v", err)
	}
	meta, err := writer.FinalizeWithPCM(make([]byte, 320))
	if err != nil {
		t.Fatalf("FinalizeWithPCM() error = %v", err)
	}
	if _, err := store.Fail(meta.CaptureID, StageTranscribing, os.ErrDeadlineExceeded, true); err != nil {
		t.Fatal(err)
	}

	plan, err := store.Recover(2)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(plan.ASR) != 1 {
		t.Fatalf("ASR recovery count = %d, want 1", len(plan.ASR))
	}
	if plan.ASR[0].Stage != StageQueuedForASR {
		t.Fatalf("recovered stage = %s, want QueuedForASR", plan.ASR[0].Stage)
	}
}
