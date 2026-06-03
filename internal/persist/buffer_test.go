package persist

import (
	"testing"
	"time"
)

func TestBufferStoreAppendReadAndClear(t *testing.T) {
	store := NewBufferStore(t.TempDir())
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	first, err := store.Append(BufferEntry{
		CaptureID:    "cap-1",
		Stream:       "codex-main",
		Text:         "one",
		TranscriptAt: at,
	})
	if err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if first.ID != "cap-1" {
		t.Fatalf("entry ID = %q, want cap-1", first.ID)
	}
	if _, err := store.Append(BufferEntry{CaptureID: "cap-1", Stream: "codex-main", Text: "one duplicate"}); err != nil {
		t.Fatalf("Append(duplicate) error = %v", err)
	}
	if _, err := store.Append(BufferEntry{CaptureID: "cap-2", Stream: "codex-main", Text: "two"}); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}

	snapshot, err := store.Read("codex-main")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entries = %+v, want two unique entries", snapshot.Entries)
	}
	if got := BufferText(snapshot.Entries, " "); got != "one two" {
		t.Fatalf("BufferText() = %q, want one two", got)
	}

	if err := store.Clear("codex-main"); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	snapshot, err = store.Read("codex-main")
	if err != nil {
		t.Fatalf("Read(after clear) error = %v", err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("entries after clear = %+v, want none", snapshot.Entries)
	}
}

func TestBufferStoreReplaceEditsAndClearsEntries(t *testing.T) {
	store := NewBufferStore(t.TempDir())
	first, err := store.Append(BufferEntry{CaptureID: "cap-1", Stream: "codex-main", Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(BufferEntry{CaptureID: "cap-2", Stream: "codex-main", Text: "two"})
	if err != nil {
		t.Fatal(err)
	}

	first.Text = "one edited"
	if err := store.Replace("codex-main", []BufferEntry{first, second}); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	snapshot, err := store.Read("codex-main")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Text != "one edited" || snapshot.Entries[1].Text != "two" {
		t.Fatalf("entries after replace = %+v, want edited first entry", snapshot.Entries)
	}

	if err := store.Replace("codex-main", nil); err != nil {
		t.Fatalf("Replace(clear) error = %v", err)
	}
	snapshot, err = store.Read("codex-main")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("entries after clear = %+v, want none", snapshot.Entries)
	}
}

func TestBufferStoreRejectsEmptyInputs(t *testing.T) {
	store := NewBufferStore(t.TempDir())
	if _, err := store.Append(BufferEntry{Stream: "", Text: "x"}); err == nil {
		t.Fatalf("Append(empty stream) error = nil, want error")
	}
	if _, err := store.Append(BufferEntry{Stream: "codex", Text: "   "}); err == nil {
		t.Fatalf("Append(empty text) error = nil, want error")
	}
}
