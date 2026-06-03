package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSavePCM16WAV(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 15, 12, 30, 45, 123456000, time.UTC)
	pcm := []byte{0x01, 0x00, 0xff, 0x7f}

	path, err := SavePCM16WAV(dir, ts, pcm, 16000)
	if err != nil {
		t.Fatalf("SavePCM16WAV() error = %v", err)
	}
	if path != filepath.Join(dir, "20260515T123045.123456Z.wav") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wav: %v", err)
	}
	if len(data) != 44+len(pcm) {
		t.Fatalf("len(data) = %d, want %d", len(data), 44+len(pcm))
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[36:40]) != "data" {
		t.Fatalf("invalid wav header")
	}
	if got := data[44:]; string(got) != string(pcm) {
		t.Fatalf("pcm payload = %v, want %v", got, pcm)
	}
}

func TestQueryHistoryFiltersByTimeStreamAndLimit(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base.Add(1 * time.Minute), TargetStream: "one", Transcript: "skip", Success: true},
		{Timestamp: base.Add(2 * time.Minute), TargetStream: "two", Transcript: "first", Success: true},
		{Timestamp: base.Add(3 * time.Minute), TargetStream: "two", Transcript: "second", Success: true},
		{Timestamp: base.Add(4 * time.Minute), TargetStream: "two", Transcript: "third", Success: true},
	}
	for _, rec := range records {
		if err := writeRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
	}

	got, err := QueryHistory(dir, HistoryQuery{
		From:   base.Add(2 * time.Minute),
		Stream: "two",
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("QueryHistory() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Transcript != "second" || got[1].Transcript != "third" {
		t.Fatalf("transcripts = [%q, %q], want [second, third]", got[0].Transcript, got[1].Transcript)
	}
}

func TestQueryHistoryOffsetSkipsNewestMatchingRecords(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	for i, text := range []string{"one", "two", "three", "four"} {
		if err := writeRecord(dir, Record{
			Timestamp:  base.Add(time.Duration(i) * time.Minute),
			Transcript: text,
			Success:    true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := QueryHistory(dir, HistoryQuery{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("QueryHistory() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Transcript != "two" || got[1].Transcript != "three" {
		t.Fatalf("transcripts = [%q, %q], want [two, three]", got[0].Transcript, got[1].Transcript)
	}

	got, err = QueryHistory(dir, HistoryQuery{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("QueryHistory(offset beyond end) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestQueryOwnedHistoryOffsetUsesOwnedStreams(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base, MessageID: "m1", TargetStream: "notes", Transcript: "one", Success: true},
		{Timestamp: base.Add(time.Minute), MessageID: "m2", TargetStream: "notes", Transcript: "two", Success: true},
		{Timestamp: base.Add(2 * time.Minute), MessageID: "m3", TargetStream: "codex", Transcript: "three", Success: true},
	}
	for _, rec := range records {
		if err := writeRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SaveFix(dir, "notes", "codex", records[1:2], "two"); err != nil {
		t.Fatal(err)
	}

	got, err := QueryOwnedHistory(dir, HistoryQuery{Stream: "codex", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("QueryOwnedHistory() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Transcript != "two" || got[0].OwnedStream != "codex" || got[0].FixedFrom != "notes" {
		t.Fatalf("owned record = %+v, want fixed notes message under codex", got[0])
	}
}

func TestPlanHistoryPruneEmptyRecordsAndOrphanAudio(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	emptyAudio := filepath.Join(dir, recordName(base, ".wav"))
	okAudio := filepath.Join(dir, recordName(base.Add(time.Minute), ".wav"))
	orphanAudio := filepath.Join(dir, "orphan.wav")
	for _, path := range []string{emptyAudio, okAudio, orphanAudio} {
		if err := os.WriteFile(path, []byte("wav"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRecord(dir, Record{
		Timestamp:  base,
		AudioPath:  emptyAudio,
		Transcript: " ",
		Success:    true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(dir, Record{
		Timestamp:  base.Add(time.Minute),
		AudioPath:  okAudio,
		Transcript: "ok",
		Success:    true,
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanHistoryPrune(dir, HistoryPruneFilter{Empty: true, IncludeOrphanAudio: true})
	if err != nil {
		t.Fatalf("PlanHistoryPrune() error = %v", err)
	}
	if plan.RecordsScanned != 2 || plan.RecordsMatched != 1 || plan.EmptyRecordsMatched != 1 {
		t.Fatalf("record stats = scanned %d matched %d empty %d", plan.RecordsScanned, plan.RecordsMatched, plan.EmptyRecordsMatched)
	}
	if plan.FilesMatched() != 3 || plan.JSONFilesMatched != 1 || plan.AudioFilesMatched != 2 || plan.OrphanAudioFilesMatched != 1 {
		t.Fatalf("file stats = files %d json %d audio %d orphan %d", plan.FilesMatched(), plan.JSONFilesMatched, plan.AudioFilesMatched, plan.OrphanAudioFilesMatched)
	}

	result, err := plan.Apply()
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.FilesDeleted != 3 {
		t.Fatalf("FilesDeleted = %d, want 3", result.FilesDeleted)
	}
	for _, path := range []string{emptyAudio, orphanAudio, filepath.Join(dir, recordName(base, ".json"))} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or stat failed: %v", path, err)
		}
	}
	for _, path := range []string{okAudio, filepath.Join(dir, recordName(base.Add(time.Minute), ".json"))} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should remain: %v", path, err)
		}
	}
}

func TestPlanHistoryPruneKeepsNewestMatchingRecords(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := writeRecord(dir, Record{
			Timestamp:  base.Add(time.Duration(i) * time.Minute),
			Transcript: string(rune('a' + i)),
			Success:    true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := PlanHistoryPrune(dir, HistoryPruneFilter{KeepLast: 1})
	if err != nil {
		t.Fatalf("PlanHistoryPrune() error = %v", err)
	}
	if plan.RecordsMatched != 2 || plan.JSONFilesMatched != 2 {
		t.Fatalf("matched records=%d json=%d, want 2 and 2", plan.RecordsMatched, plan.JSONFilesMatched)
	}
}

func TestPlanHistoryPruneOrphanAudioOnlyDoesNotMatchAllRecords(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	recordAudio := filepath.Join(dir, recordName(ts, ".wav"))
	orphanAudio := filepath.Join(dir, "orphan.wav")
	for _, path := range []string{recordAudio, orphanAudio} {
		if err := os.WriteFile(path, []byte("wav"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRecord(dir, Record{
		Timestamp:  ts,
		AudioPath:  recordAudio,
		Transcript: "keep",
		Success:    true,
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanHistoryPrune(dir, HistoryPruneFilter{IncludeOrphanAudio: true})
	if err != nil {
		t.Fatalf("PlanHistoryPrune() error = %v", err)
	}
	if plan.RecordsMatched != 0 || plan.JSONFilesMatched != 0 {
		t.Fatalf("matched records=%d json=%d, want 0 and 0", plan.RecordsMatched, plan.JSONFilesMatched)
	}
	if plan.FilesMatched() != 1 || plan.OrphanAudioFilesMatched != 1 {
		t.Fatalf("matched files=%d orphan=%d, want 1 and 1", plan.FilesMatched(), plan.OrphanAudioFilesMatched)
	}
}

func writeRecord(dir string, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, recordName(rec.Timestamp, ".json")), data, 0o600)
}
