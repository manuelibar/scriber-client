package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"scriber/internal/persist"
)

// Registry is what the IPC server needs from the daemon's stream registry.
type Registry interface {
	Attach(req *AttachRequest) (*Stream, string, error)
	Detach(name, targetRef string, slot int) error
	DetachAll() error
	Streams() ([]Stream, string, int)
	Select(name string) (string, error)
	SetSlot(name string, slot int) (*Stream, error)
	ClearSlot(name string) (*Stream, error)
	SelectSlot(slot int) (string, error)
	Cycle() (string, error)
	SendTextToActive(text string) (string, error)
	SendTextToStream(name, text string) (string, error)
}

// DaemonState reflects what the IPC server reports to `stt monitor`.
type DaemonState interface {
	State() string
	RecordingStartedAt() time.Time
	AudioLevel() float64
	LastTranscript() (string, time.Time)
	ServerOK() bool
	Jobs() []JobSnapshot
}

type Server struct {
	socketPath     string
	transcriptsDir string
	reg            Registry
	dmn            DaemonState
	shutdown       func()
}

func NewServer(socketPath string, reg Registry, dmn DaemonState, shutdown func(), transcriptsDir ...string) *Server {
	dir := ""
	if len(transcriptsDir) > 0 {
		dir = transcriptsDir[0]
	}
	return &Server{socketPath: socketPath, transcriptsDir: dir, reg: reg, dmn: dmn, shutdown: shutdown}
}

func (s *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(s.socketPath)
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/attach", s.handleAttach)
	mux.HandleFunc("/detach", s.handleDetach)
	mux.HandleFunc("/select", s.handleSelect)
	mux.HandleFunc("/stream/set-slot", s.handleSetSlot)
	mux.HandleFunc("/stream/clear-slot", s.handleClearSlot)
	mux.HandleFunc("/slot/select", s.handleSelectSlot)
	mux.HandleFunc("/cycle", s.handleCycle)
	mux.HandleFunc("/monitor", s.handleMonitor)
	mux.HandleFunc("/paste", s.handlePaste)
	mux.HandleFunc("/redeem", s.handleRedeem)
	mux.HandleFunc("/shutdown", s.handleShutdown)

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
		_ = os.Remove(s.socketPath)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, ErrorResponse{Error: err.Error()})
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	var req AttachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	stream, msg, err := s.reg.Attach(&req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, AttachResponse{Stream: *stream, Message: msg})
}

func (s *Server) handleDetach(w http.ResponseWriter, r *http.Request) {
	var req DetachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "all" {
		if err := s.reg.DetachAll(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := s.reg.Detach(req.Name, req.TargetRef, req.Slot); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSelect(w http.ResponseWriter, r *http.Request) {
	var req SelectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	active, err := s.reg.Select(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, SelectResponse{Active: active})
}

func (s *Server) handleSetSlot(w http.ResponseWriter, r *http.Request) {
	var req SetSlotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	stream, err := s.reg.SetSlot(req.Name, req.Slot)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, SetSlotResponse{Stream: *stream})
}

func (s *Server) handleClearSlot(w http.ResponseWriter, r *http.Request) {
	var req ClearSlotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	stream, err := s.reg.ClearSlot(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, SetSlotResponse{Stream: *stream})
}

func (s *Server) handleSelectSlot(w http.ResponseWriter, r *http.Request) {
	var req SelectSlotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	active, err := s.reg.SelectSlot(req.Slot)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, SelectResponse{Active: active})
}

func (s *Server) handleCycle(w http.ResponseWriter, r *http.Request) {
	active, err := s.reg.Cycle()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, CycleResponse{Active: active})
}

func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	historyQuery, loadHistory, err := parseMonitorHistoryQuery(r.URL.Query())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	streams, active, activeSlot := s.reg.Streams()
	recordingMs := 0
	if started := s.dmn.RecordingStartedAt(); !started.IsZero() {
		recordingMs = int(time.Since(started) / time.Millisecond)
	}
	last, lastAt := s.dmn.LastTranscript()
	transcripts, historyLoaded, historyErr := s.monitorTranscripts(historyQuery, loadHistory)
	writeJSON(w, http.StatusOK, MonitorResponse{
		State:                   s.dmn.State(),
		PID:                     os.Getpid(),
		Active:                  active,
		ActiveSlot:              activeSlot,
		RecordingMs:             recordingMs,
		AudioLevel:              s.dmn.AudioLevel(),
		Jobs:                    s.dmn.Jobs(),
		Streams:                 streams,
		Transcripts:             transcripts,
		TranscriptHistoryLoaded: historyLoaded,
		TranscriptHistoryError:  historyErr,
		LastTranscript:          last,
		LastTranscriptAt:        lastAt,
		ServerOK:                s.dmn.ServerOK(),
	})
}

func parseMonitorHistoryQuery(values url.Values) (persist.HistoryQuery, bool, error) {
	rawLimit := values.Get("history_limit")
	rawSince := values.Get("history_since")
	rawStream := values.Get("history_stream")
	if rawLimit == "" && rawSince == "" && rawStream == "" {
		return persist.HistoryQuery{}, false, nil
	}

	query := persist.HistoryQuery{Limit: 200}
	if rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return persist.HistoryQuery{}, false, fmt.Errorf("history_limit must be zero or greater")
		}
		if limit == 0 {
			return persist.HistoryQuery{}, false, nil
		}
		query.Limit = limit
	}
	if rawSince != "" {
		since, err := time.Parse(time.RFC3339Nano, rawSince)
		if err != nil {
			return persist.HistoryQuery{}, false, fmt.Errorf("history_since must be RFC3339Nano: %w", err)
		}
		query.From = since
	}
	query.Stream = rawStream
	return query, true, nil
}

func (s *Server) monitorTranscripts(query persist.HistoryQuery, load bool) ([]TranscriptEntry, bool, string) {
	if !load || s.transcriptsDir == "" {
		return nil, false, ""
	}

	records, err := persist.QueryOwnedHistory(s.transcriptsDir, query)
	if err != nil {
		return nil, false, err.Error()
	}

	out := make([]TranscriptEntry, 0, len(records))
	for _, rec := range records {
		out = append(out, TranscriptEntry{
			Timestamp:    rec.Timestamp,
			MessageID:    persist.RecordMessageID(rec),
			AudioMs:      rec.AudioMs,
			Stream:       rec.TargetStream,
			OwnedStream:  rec.OwnedStream,
			RedeemedFrom: rec.RedeemedFrom,
			RedeemedTo:   rec.RedeemedTo,
			CaptureID:    rec.CaptureID,
			Stage:        rec.Stage,
			TargetType:   rec.TargetType,
			TargetRef:    rec.TargetRef,
			Mode:         rec.Mode,
			Success:      rec.Success,
			Error:        rec.Error,
			InferenceMs:  rec.InferenceMs,
			Transcript:   rec.Transcript,
		})
	}
	return out, true, ""
}

func (s *Server) handlePaste(w http.ResponseWriter, r *http.Request) {
	var req PasteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	stream, err := s.reg.SendTextToActive(req.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, PasteResponse{Stream: stream, Chars: len(req.Text)})
}

func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	if s.transcriptsDir == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("transcript history unavailable"))
		return
	}
	var req RedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Last <= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("last must be positive"))
		return
	}
	if req.From == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("from stream is required"))
		return
	}
	if req.To == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("to stream is required"))
		return
	}
	separator := req.Separator
	if separator == "" {
		separator = " "
	}
	selection, err := persist.SelectRedemptionMessages(s.transcriptsDir, req.From, req.Last, separator)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.reg.SendTextToStream(req.To, selection.Text); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	redemption, err := persist.SaveRedemption(s.transcriptsDir, req.From, req.To, selection.Messages, selection.Text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, RedeemResponse{
		From:       req.From,
		To:         req.To,
		MessageIDs: redemption.MessageIDs,
		Chars:      len(selection.Text),
		Text:       selection.Text,
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if s.shutdown == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("shutdown unavailable"))
		return
	}
	writeJSON(w, http.StatusOK, ShutdownResponse{OK: true})
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.shutdown()
	}()
}
