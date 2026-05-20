package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Registry is what the IPC server needs from the daemon's stream registry.
type Registry interface {
	Attach(req *AttachRequest) (*Stream, string, error)
	Detach(name, targetRef string) error
	DetachAll() error
	Streams() ([]Stream, string)
	Select(name string) (string, error)
	SetSlot(name string, slot int) (*Stream, error)
	ClearSlot(name string) (*Stream, error)
	SelectSlot(slot int) (string, error)
	Cycle() (string, error)
}

// DaemonState reflects what the IPC server reports for `stt status`.
type DaemonState interface {
	State() string
	RecordingStartedAt() time.Time
	AudioLevel() float64
	LastTranscript() (string, time.Time)
	ServerOK() bool
}

type Server struct {
	socketPath string
	reg        Registry
	dmn        DaemonState
}

func NewServer(socketPath string, reg Registry, dmn DaemonState) *Server {
	return &Server{socketPath: socketPath, reg: reg, dmn: dmn}
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
	mux.HandleFunc("/streams", s.handleStreams)
	mux.HandleFunc("/select", s.handleSelect)
	mux.HandleFunc("/stream/set-slot", s.handleSetSlot)
	mux.HandleFunc("/stream/clear-slot", s.handleClearSlot)
	mux.HandleFunc("/slot/select", s.handleSelectSlot)
	mux.HandleFunc("/cycle", s.handleCycle)
	mux.HandleFunc("/status", s.handleStatus)

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
	if err := s.reg.Detach(req.Name, req.TargetRef); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleStreams(w http.ResponseWriter, r *http.Request) {
	streams, active := s.reg.Streams()
	writeJSON(w, http.StatusOK, ListResponse{Active: active, Streams: streams})
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	streams, active := s.reg.Streams()
	activeSlot := 0
	for _, stream := range streams {
		if stream.Name == active {
			activeSlot = stream.Slot
			break
		}
	}
	recordingMs := 0
	if started := s.dmn.RecordingStartedAt(); !started.IsZero() {
		recordingMs = int(time.Since(started) / time.Millisecond)
	}
	last, lastAt := s.dmn.LastTranscript()
	writeJSON(w, http.StatusOK, StatusResponse{
		State:            s.dmn.State(),
		Active:           active,
		ActiveSlot:       activeSlot,
		RecordingMs:      recordingMs,
		AudioLevel:       s.dmn.AudioLevel(),
		StreamCount:      len(streams),
		LastTranscript:   last,
		LastTranscriptAt: lastAt,
		ServerOK:         s.dmn.ServerOK(),
	})
}
