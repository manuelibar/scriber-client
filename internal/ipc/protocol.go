package ipc

import "time"

type Pane struct {
	Alias      string    `json:"alias"`
	PaneID     string    `json:"pane_id"` // e.g. "%1"
	Session    string    `json:"session"`
	AttachedAt time.Time `json:"attached_at"`
	Mode       string    `json:"mode"` // "tmux"
}

type AttachRequest struct {
	PID      int    `json:"pid"`
	PPID     int    `json:"ppid"`
	TTY      string `json:"tty"`
	TMUXPane string `json:"tmux_pane"`
	STY      string `json:"sty"`
	CWD      string `json:"cwd"`
	Term     string `json:"term"`
	Alias    string `json:"alias,omitempty"`
}

type AttachResponse struct {
	Pane    Pane   `json:"pane"`
	Message string `json:"message,omitempty"`
}

type DetachRequest struct {
	// Either Alias (named pane) or TMUXPane (current pane). Use Alias="all" to clear.
	Alias    string `json:"alias,omitempty"`
	TMUXPane string `json:"tmux_pane,omitempty"`
}

type ListResponse struct {
	Active string `json:"active"`
	Panes  []Pane `json:"panes"`
}

type SwitchRequest struct {
	Alias string `json:"alias"`
}

type SwitchResponse struct {
	Active string `json:"active"`
}

type CycleResponse struct {
	Active string `json:"active"`
}

type StatusResponse struct {
	State            string    `json:"state"`
	Active           string    `json:"active"`
	PaneCount        int       `json:"pane_count"`
	LastTranscript   string    `json:"last_transcript,omitempty"`
	LastTranscriptAt time.Time `json:"last_transcript_at,omitempty"`
	ServerOK         bool      `json:"server_ok"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
