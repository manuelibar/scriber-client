package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"time"
)

// Registry is what the IPC server needs from the daemon's pane registry.
type Registry interface {
	Attach(req *AttachRequest) (*Pane, string, error)
	Detach(alias, tmuxPane string) error
	DetachAll() error
	List() ([]Pane, string)
	Switch(alias string) (string, error)
	Cycle() (string, error)
}

// DaemonState reflects what the IPC server reports for `scriber status`.
type DaemonState interface {
	State() string
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
	mux.HandleFunc("/list", s.handleList)
	mux.HandleFunc("/switch", s.handleSwitch)
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
	pane, msg, err := s.reg.Attach(&req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, AttachResponse{Pane: *pane, Message: msg})
}

func (s *Server) handleDetach(w http.ResponseWriter, r *http.Request) {
	var req DetachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Alias == "all" {
		if err := s.reg.DetachAll(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		if err := s.reg.Detach(req.Alias, req.TMUXPane); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	panes, active := s.reg.List()
	writeJSON(w, http.StatusOK, ListResponse{Active: active, Panes: panes})
}

func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	var req SwitchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	active, err := s.reg.Switch(req.Alias)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, SwitchResponse{Active: active})
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
	panes, active := s.reg.List()
	last, lastAt := s.dmn.LastTranscript()
	writeJSON(w, http.StatusOK, StatusResponse{
		State:            s.dmn.State(),
		Active:           active,
		PaneCount:        len(panes),
		LastTranscript:   last,
		LastTranscriptAt: lastAt,
		ServerOK:         s.dmn.ServerOK(),
	})
}
