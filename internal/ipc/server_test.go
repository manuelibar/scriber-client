package ipc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"scriber/internal/persist"
)

func TestHandleRedeemPastesToDestinationAndMovesHistoryOwnership(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	for _, rec := range []persist.Record{
		{Timestamp: base, MessageID: "m1", TargetStream: "notes", Transcript: "one", Success: true, Mode: TargetTypePTY},
		{Timestamp: base.Add(time.Second), MessageID: "m2", TargetStream: "notes", Transcript: "two", Success: true, Mode: TargetTypePTY},
	} {
		if err := persist.Save(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	reg := &fakeRegistry{}
	server := NewServer("", reg, fakeDaemonState{}, nil, dir)
	body, err := json.Marshal(RedeemRequest{From: "notes", To: "codex-main", Last: 2, Separator: "\n"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/redeem", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	server.handleRedeem(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if reg.sentStream != "codex-main" || reg.sentText != "one\ntwo" {
		t.Fatalf("sent stream/text = %q/%q, want codex-main/one\\ntwo", reg.sentStream, reg.sentText)
	}
	notes, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{Stream: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes owned records = %+v, want none", notes)
	}
	codex, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{Stream: "codex-main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 2 || codex[0].RedeemedFrom != "notes" || codex[1].RedeemedTo != "codex-main" {
		t.Fatalf("codex owned records = %+v, want redeemed notes messages", codex)
	}
}

type fakeRegistry struct {
	sentStream string
	sentText   string
}

func (f *fakeRegistry) Attach(req *AttachRequest) (*Stream, string, error) { return nil, "", nil }
func (f *fakeRegistry) Detach(name, targetRef string, slot int) error      { return nil }
func (f *fakeRegistry) DetachAll() error                                   { return nil }
func (f *fakeRegistry) Streams() ([]Stream, string, int)                   { return nil, "", 0 }
func (f *fakeRegistry) Select(name string) (string, error)                 { return name, nil }
func (f *fakeRegistry) SetSlot(name string, slot int) (*Stream, error)     { return nil, nil }
func (f *fakeRegistry) ClearSlot(name string) (*Stream, error)             { return nil, nil }
func (f *fakeRegistry) SelectSlot(slot int) (string, error)                { return "", nil }
func (f *fakeRegistry) Cycle() (string, error)                             { return "", nil }
func (f *fakeRegistry) SendTextToActive(text string) (string, error)       { return "", nil }
func (f *fakeRegistry) SendTextToStream(name, text string) (string, error) {
	f.sentStream = name
	f.sentText = text
	return name, nil
}

type fakeDaemonState struct{}

func (fakeDaemonState) State() string                       { return "Idle" }
func (fakeDaemonState) RecordingStartedAt() time.Time       { return time.Time{} }
func (fakeDaemonState) AudioLevel() float64                 { return 0 }
func (fakeDaemonState) LastTranscript() (string, time.Time) { return "", time.Time{} }
func (fakeDaemonState) ServerOK() bool                      { return true }
func (fakeDaemonState) Jobs() []JobSnapshot                 { return nil }
