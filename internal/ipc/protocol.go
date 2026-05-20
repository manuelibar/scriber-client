package ipc

import "time"

const (
	TargetTypePTY = "pty"

	StreamStatusActive   = "active"
	StreamStatusDead     = "dead"
	StreamStatusDisabled = "disabled"
)

// Target is the concrete STT-owned endpoint attached to a logical stream.
// For the terminal backend, TargetRef is a private Unix socket served by
// `stt attach`; the daemon POSTs final text there and the attach process writes
// it into the PTY master it owns.
type Target struct {
	ID         string    `json:"id"`
	StreamID   string    `json:"stream_id"`
	TargetType string    `json:"target_type"`
	TargetRef  string    `json:"target_ref"`
	Label      string    `json:"label,omitempty"`
	TTY        string    `json:"tty,omitempty"`
	CWD        string    `json:"cwd,omitempty"`
	PID        int       `json:"pid,omitempty"`
	AttachedAt time.Time `json:"attached_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// Stream is the user-facing destination for dictated final text.
type Stream struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Slot       int       `json:"slot,omitempty"`
	Target     Target    `json:"target"`
	Status     string    `json:"status"`
	AttachedAt time.Time `json:"attached_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

type AttachRequest struct {
	PID        int    `json:"pid"`
	PPID       int    `json:"ppid"`
	TTY        string `json:"tty"`
	CWD        string `json:"cwd"`
	Term       string `json:"term"`
	StreamName string `json:"stream_name"`
	TargetType string `json:"target_type"`
	TargetRef  string `json:"target_ref"`
	Label      string `json:"label,omitempty"`
}

type AttachResponse struct {
	Stream  Stream `json:"stream"`
	Message string `json:"message,omitempty"`
}

type DetachRequest struct {
	Name      string `json:"name,omitempty"`
	TargetRef string `json:"target_ref,omitempty"`
}

type ListResponse struct {
	Active  string   `json:"active"`
	Streams []Stream `json:"streams"`
}

type SelectRequest struct {
	Name string `json:"name"`
}

type SelectResponse struct {
	Active string `json:"active"`
}

type SetSlotRequest struct {
	Name string `json:"name"`
	Slot int    `json:"slot"`
}

type SetSlotResponse struct {
	Stream Stream `json:"stream"`
}

type ClearSlotRequest struct {
	Name string `json:"name"`
}

type SelectSlotRequest struct {
	Slot int `json:"slot"`
}

type CycleResponse struct {
	Active string `json:"active"`
}

type StatusResponse struct {
	State            string    `json:"state"`
	Active           string    `json:"active"`
	ActiveSlot       int       `json:"active_slot,omitempty"`
	RecordingMs      int       `json:"recording_ms,omitempty"`
	AudioLevel       float64   `json:"audio_level,omitempty"`
	StreamCount      int       `json:"stream_count"`
	LastTranscript   string    `json:"last_transcript,omitempty"`
	LastTranscriptAt time.Time `json:"last_transcript_at,omitempty"`
	ServerOK         bool      `json:"server_ok"`
}

type InjectRequest struct {
	Text string `json:"text"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
