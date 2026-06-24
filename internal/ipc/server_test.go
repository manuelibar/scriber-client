package ipc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"scriber/internal/persist"
)

func TestHandleFixInjectsDestinationStreamAndMovesHistoryOwnership(t *testing.T) {
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
	body, err := json.Marshal(FixRequest{From: "notes", To: "codex-main", Last: 2, Separator: "\n"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/fix", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	server.handleFix(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if reg.sentStream != "codex-main" || reg.sentText != "one\ntwo" {
		t.Fatalf("sent stream=%q text=%q, want fixed text injected into codex-main", reg.sentStream, reg.sentText)
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
	if len(codex) != 2 || codex[0].FixedFrom != "notes" || codex[1].FixedTo != "codex-main" {
		t.Fatalf("codex owned records = %+v, want fixed notes messages", codex)
	}
}

func TestServerDoesNotExposeFixCompatibilityRoutes(t *testing.T) {
	server := NewServer("", &fakeRegistry{}, fakeDaemonState{}, nil, t.TempDir())
	removedLegacyFixRoute := "/" + "re" + "de" + "em"
	req := httptest.NewRequest(http.MethodPost, removedLegacyFixRoute, bytes.NewReader([]byte(`{}`)))
	recorder := httptest.NewRecorder()

	server.handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404 for removed legacy fix route", recorder.Code, recorder.Body.String())
	}
}

func TestHandlePasteInjectsActiveStream(t *testing.T) {
	dir := t.TempDir()
	reg := &fakeRegistry{}
	server := NewServer("", reg, fakeDaemonState{}, nil, dir)
	body, err := json.Marshal(PasteRequest{Text: "paste me"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/paste", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	server.handlePaste(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if reg.sentStream != "codex-main" || reg.sentText != "paste me" {
		t.Fatalf("sent stream=%q text=%q, want pasted text injected into active stream", reg.sentStream, reg.sentText)
	}
}

func TestParseMonitorHistoryQuerySupportsOffset(t *testing.T) {
	query, load, err := parseMonitorHistoryQuery(url.Values{
		"history_limit":  []string{"5"},
		"history_offset": []string{"10"},
		"history_stream": []string{"notes"},
	})
	if err != nil {
		t.Fatalf("parseMonitorHistoryQuery() error = %v", err)
	}
	if !load || query.Limit != 5 || query.Offset != 10 || query.Stream != "notes" {
		t.Fatalf("query = %+v load=%t, want limit=5 offset=10 stream=notes load=true", query, load)
	}

	if _, _, err := parseMonitorHistoryQuery(url.Values{"history_offset": []string{"-1"}}); err == nil {
		t.Fatalf("parseMonitorHistoryQuery(negative offset) error = nil, want error")
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
func (f *fakeRegistry) ActiveStream() *Stream                              { return testStream("codex-main") }
func (f *fakeRegistry) Stream(name string) *Stream                         { return testStream(name) }
func (f *fakeRegistry) SendTextToStream(name, text string) (string, error) {
	f.sentStream = name
	f.sentText = text
	return name, nil
}

func testStream(name string) *Stream {
	return &Stream{
		ID:     "stream_" + name,
		Name:   name,
		Status: StreamStatusActive,
		Target: Target{TargetType: TargetTypePTY, TargetRef: "/tmp/" + name + ".sock"},
	}
}

type fakeDaemonState struct{}

func (fakeDaemonState) State() string                       { return "Idle" }
func (fakeDaemonState) RecordingStartedAt() time.Time       { return time.Time{} }
func (fakeDaemonState) AudioLevel() float64                 { return 0 }
func (fakeDaemonState) LastTranscript() (string, time.Time) { return "", time.Time{} }
func (fakeDaemonState) ServerOK() bool                      { return true }
func (fakeDaemonState) Jobs() []JobSnapshot                 { return nil }
