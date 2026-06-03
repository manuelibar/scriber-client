package persist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type BufferEntry struct {
	ID           string    `json:"id"`
	CaptureID    string    `json:"capture_id,omitempty"`
	Stream       string    `json:"stream"`
	TargetType   string    `json:"target_type,omitempty"`
	TargetRef    string    `json:"target_ref,omitempty"`
	Language     string    `json:"language,omitempty"`
	Text         string    `json:"text"`
	AudioMs      int       `json:"audio_ms,omitempty"`
	InferenceMs  int       `json:"inference_ms,omitempty"`
	TranscriptAt time.Time `json:"transcript_at"`
	StagedAt     time.Time `json:"staged_at"`
}

type BufferSnapshot struct {
	Stream    string        `json:"stream"`
	UpdatedAt time.Time     `json:"updated_at"`
	Entries   []BufferEntry `json:"entries"`
}

type BufferStore struct {
	Root string
	mu   sync.Mutex
}

func NewBufferStore(transcriptsDir string) *BufferStore {
	return &BufferStore{Root: filepath.Join(transcriptsDir, "buffers")}
}

func (s *BufferStore) Append(entry BufferEntry) (BufferEntry, error) {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return BufferEntry{}, fmt.Errorf("buffer store root is empty")
	}
	entry.Stream = strings.TrimSpace(entry.Stream)
	if entry.Stream == "" {
		return BufferEntry{}, fmt.Errorf("buffer stream is required")
	}
	entry.Text = strings.TrimSpace(entry.Text)
	if entry.Text == "" {
		return BufferEntry{}, fmt.Errorf("buffer text is empty")
	}
	now := time.Now().UTC()
	if entry.StagedAt.IsZero() {
		entry.StagedAt = now
	}
	if entry.TranscriptAt.IsZero() {
		entry.TranscriptAt = entry.StagedAt
	}
	if entry.ID == "" {
		entry.ID = entry.CaptureID
	}
	if entry.ID == "" {
		entry.ID = entry.StagedAt.Format(time.RFC3339Nano) + "|" + entry.Stream + "|" + entry.Text
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.readLocked(entry.Stream)
	if err != nil {
		return BufferEntry{}, err
	}
	for _, existing := range snapshot.Entries {
		if existing.ID == entry.ID {
			return existing, nil
		}
	}
	snapshot.Stream = entry.Stream
	snapshot.UpdatedAt = now
	snapshot.Entries = append(snapshot.Entries, entry)
	if err := s.writeLocked(snapshot); err != nil {
		return BufferEntry{}, err
	}
	return entry, nil
}

func (s *BufferStore) Read(stream string) (BufferSnapshot, error) {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return BufferSnapshot{}, fmt.Errorf("buffer store root is empty")
	}
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return BufferSnapshot{}, fmt.Errorf("buffer stream is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(stream)
}

func (s *BufferStore) Replace(stream string, entries []BufferEntry) error {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return fmt.Errorf("buffer store root is empty")
	}
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return fmt.Errorf("buffer stream is required")
	}

	next := make([]BufferEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Stream = stream
		entry.Text = strings.TrimSpace(entry.Text)
		if entry.ID == "" {
			return fmt.Errorf("buffer entry id is required")
		}
		if entry.Text == "" {
			continue
		}
		if entry.StagedAt.IsZero() {
			entry.StagedAt = time.Now().UTC()
		}
		if entry.TranscriptAt.IsZero() {
			entry.TranscriptAt = entry.StagedAt
		}
		next = append(next, entry)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(next) == 0 {
		path := s.pathLocked(stream)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return s.writeLocked(BufferSnapshot{
		Stream:    stream,
		UpdatedAt: time.Now().UTC(),
		Entries:   next,
	})
}

func (s *BufferStore) Clear(stream string) error {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return fmt.Errorf("buffer store root is empty")
	}
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return fmt.Errorf("buffer stream is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.pathLocked(stream)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func BufferText(entries []BufferEntry, separator string) string {
	if separator == "" {
		separator = " "
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, separator)
}

func (s *BufferStore) readLocked(stream string) (BufferSnapshot, error) {
	path := s.pathLocked(stream)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return BufferSnapshot{Stream: stream}, nil
		}
		return BufferSnapshot{}, err
	}
	var snapshot BufferSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return BufferSnapshot{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if snapshot.Stream == "" {
		snapshot.Stream = stream
	}
	return snapshot, nil
}

func (s *BufferStore) writeLocked(snapshot BufferSnapshot) error {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	path := s.pathLocked(snapshot.Stream)
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

func (s *BufferStore) pathLocked(stream string) string {
	sum := sha256.Sum256([]byte(stream))
	return filepath.Join(s.Root, hex.EncodeToString(sum[:12])+".json")
}
