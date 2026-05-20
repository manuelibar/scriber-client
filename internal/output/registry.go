package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"scriber/internal/ipc"
)

// TargetBackend supplies backend-specific liveness checks for registry
// operations. Text delivery happens through SendText.
type TargetBackend interface {
	Alive(target ipc.Target) bool
}

type ptyBackend struct{}

func (ptyBackend) Alive(target ipc.Target) bool { return PTYAlive(target) }

// Registry tracks logical dictation streams. Thread-safe. Persists to disk
// after every mutation.
type Registry struct {
	path    string
	backend TargetBackend

	mu    sync.Mutex
	state registryFile
}

type registryFile struct {
	ActiveStream string       `json:"active_stream,omitempty"`
	Streams      []ipc.Stream `json:"streams,omitempty"`
}

func NewRegistry(path string) (*Registry, error) {
	return NewRegistryWithBackend(path, ptyBackend{})
}

func NewRegistryWithBackend(path string, backend TargetBackend) (*Registry, error) {
	if backend == nil {
		backend = ptyBackend{}
	}
	r := &Registry{path: path, backend: backend}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) load() error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(data, &r.state); err != nil {
		return err
	}
	r.normalizeLocked()
	return nil
}

func (r *Registry) normalizeLocked() {
	seenNames := map[string]bool{}
	seenSlots := map[int]bool{}
	now := time.Now().UTC()
	for i := range r.state.Streams {
		s := &r.state.Streams[i]
		s.Name = strings.TrimSpace(s.Name)
		if s.Name == "" {
			s.Name = fmt.Sprintf("stream-%d", i+1)
		}
		if seenNames[s.Name] {
			s.Name = uniqueName(s.Name, seenNames)
		}
		seenNames[s.Name] = true
		if s.ID == "" {
			s.ID = streamIDFor(s.Name)
		}
		if s.Status == "" {
			s.Status = ipc.StreamStatusActive
		}
		if s.AttachedAt.IsZero() {
			s.AttachedAt = now
		}
		if s.LastUsedAt.IsZero() {
			s.LastUsedAt = s.AttachedAt
		}
		if s.Target.TargetType == "" && s.Target.TargetRef != "" {
			s.Target.TargetType = ipc.TargetTypePTY
		}
		if s.Target.ID == "" && s.Target.TargetRef != "" {
			s.Target.ID = targetIDFor(s.Name, s.Target.TargetRef)
		}
		if s.Target.StreamID == "" {
			s.Target.StreamID = s.ID
		}
		if s.Target.Label == "" {
			s.Target.Label = s.Name
		}
		if s.Target.AttachedAt.IsZero() {
			s.Target.AttachedAt = s.AttachedAt
		}
		if s.Target.LastSeenAt.IsZero() {
			s.Target.LastSeenAt = s.Target.AttachedAt
		}
		if s.Slot < 0 || s.Slot > 9 || (s.Slot > 0 && seenSlots[s.Slot]) {
			s.Slot = 0
		}
		if s.Slot > 0 {
			seenSlots[s.Slot] = true
		}
	}
	if r.state.ActiveStream != "" && r.findStreamLocked(r.state.ActiveStream) < 0 {
		r.state.ActiveStream = ""
	}
}

func (r *Registry) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func (r *Registry) Attach(req *ipc.AttachRequest) (*ipc.Stream, string, error) {
	name := strings.TrimSpace(req.StreamName)
	if name == "" {
		name = defaultNameFor(req)
	}
	if req.TargetType == "" {
		req.TargetType = ipc.TargetTypePTY
	}
	if req.TargetType != ipc.TargetTypePTY {
		return nil, "", fmt.Errorf("unsupported target type %q", req.TargetType)
	}
	if req.TargetRef == "" {
		return nil, "", fmt.Errorf("missing target_ref")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	idx := r.findStreamLocked(name)
	message := ""
	if idx < 0 {
		r.state.Streams = append(r.state.Streams, ipc.Stream{
			ID:         streamIDFor(name),
			Name:       name,
			AttachedAt: now,
		})
		idx = len(r.state.Streams) - 1
	} else if existing := r.state.Streams[idx].Target.TargetRef; existing != "" && existing != req.TargetRef {
		message = fmt.Sprintf("replaced previous target %s", existing)
	}

	stream := &r.state.Streams[idx]
	if stream.ID == "" {
		stream.ID = streamIDFor(name)
	}
	if stream.AttachedAt.IsZero() {
		stream.AttachedAt = now
	}
	stream.Name = name
	stream.Status = ipc.StreamStatusActive
	stream.LastUsedAt = now
	stream.Target = ipc.Target{
		ID:         targetIDFor(name, req.TargetRef),
		StreamID:   stream.ID,
		TargetType: ipc.TargetTypePTY,
		TargetRef:  req.TargetRef,
		Label:      labelFor(req, name),
		TTY:        req.TTY,
		CWD:        req.CWD,
		PID:        req.PID,
		AttachedAt: now,
		LastSeenAt: now,
	}
	r.state.ActiveStream = name

	if err := r.saveLocked(); err != nil {
		return nil, "", err
	}
	slog.Info("attached stream", "stream", name, "target", req.TargetRef, "pid", req.PID)
	cp := *stream
	return &cp, message, nil
}

func defaultNameFor(req *ipc.AttachRequest) string {
	if req.Label != "" {
		return req.Label
	}
	if req.TTY != "" {
		base := filepath.Base(req.TTY)
		if base != "" && base != "." && base != "/" {
			return "term-" + base
		}
	}
	if req.PID > 0 {
		return fmt.Sprintf("term-%d", req.PID)
	}
	return "term"
}

func labelFor(req *ipc.AttachRequest, fallback string) string {
	if strings.TrimSpace(req.Label) != "" {
		return strings.TrimSpace(req.Label)
	}
	return fallback
}

func uniqueName(desired string, taken map[string]bool) string {
	if !taken[desired] {
		return desired
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", desired, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

func (r *Registry) Detach(name, targetRef string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	target := strings.TrimSpace(name)
	if target == "" {
		for _, s := range r.state.Streams {
			if s.Target.TargetRef == targetRef {
				target = s.Name
				break
			}
		}
	}
	if target == "" {
		return fmt.Errorf("no matching stream to detach (name=%q target_ref=%q)", name, targetRef)
	}

	idx := r.findStreamLocked(target)
	if idx < 0 {
		return fmt.Errorf("stream %q not registered", target)
	}
	r.state.Streams = append(r.state.Streams[:idx], r.state.Streams[idx+1:]...)
	if r.state.ActiveStream == target {
		r.state.ActiveStream = r.firstLiveStreamNameLocked()
	}
	return r.saveLocked()
}

func (r *Registry) DetachAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Streams = nil
	r.state.ActiveStream = ""
	return r.saveLocked()
}

func (r *Registry) Streams() ([]ipc.Stream, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ipc.Stream, len(r.state.Streams))
	copy(out, r.state.Streams)
	return out, r.state.ActiveStream
}

func (r *Registry) Select(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.findStreamLocked(name)
	if idx < 0 {
		return "", fmt.Errorf("stream %q not registered", name)
	}
	if !streamIsLive(r.state.Streams[idx]) {
		return "", fmt.Errorf("stream %q has no live target", name)
	}
	r.state.ActiveStream = r.state.Streams[idx].Name
	r.state.Streams[idx].LastUsedAt = time.Now().UTC()
	return r.state.ActiveStream, r.saveLocked()
}

func (r *Registry) SetSlot(name string, slot int) (*ipc.Stream, error) {
	if slot < 1 || slot > 9 {
		return nil, fmt.Errorf("slot must be between 1 and 9")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	idx := r.findStreamLocked(name)
	if idx < 0 {
		return nil, fmt.Errorf("stream %q not registered", name)
	}

	for i := range r.state.Streams {
		if i != idx && r.state.Streams[i].Slot == slot {
			r.state.Streams[i].Slot = 0
		}
	}
	r.state.Streams[idx].Slot = slot
	r.state.Streams[idx].LastUsedAt = time.Now().UTC()
	if err := r.saveLocked(); err != nil {
		return nil, err
	}
	cp := r.state.Streams[idx]
	return &cp, nil
}

func (r *Registry) ClearSlot(name string) (*ipc.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := r.findStreamLocked(name)
	if idx < 0 {
		return nil, fmt.Errorf("stream %q not registered", name)
	}
	r.state.Streams[idx].Slot = 0
	if err := r.saveLocked(); err != nil {
		return nil, err
	}
	cp := r.state.Streams[idx]
	return &cp, nil
}

func (r *Registry) SelectSlot(slot int) (string, error) {
	if slot < 1 || slot > 9 {
		return "", fmt.Errorf("slot must be between 1 and 9")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for i, s := range r.state.Streams {
		if s.Slot != slot {
			continue
		}
		if !streamIsLive(s) {
			return "", fmt.Errorf("stream %q in slot %d has no live target", s.Name, slot)
		}
		r.state.ActiveStream = s.Name
		r.state.Streams[i].LastUsedAt = time.Now().UTC()
		return r.state.ActiveStream, r.saveLocked()
	}
	return "", fmt.Errorf("no stream assigned to slot %d", slot)
}

func (r *Registry) Cycle() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	live := make([]int, 0, len(r.state.Streams))
	for i, s := range r.state.Streams {
		if streamIsLive(s) {
			live = append(live, i)
		}
	}
	if len(live) == 0 {
		return "", fmt.Errorf("no streams registered")
	}
	currentLiveIdx := -1
	for i, streamIdx := range live {
		if r.state.Streams[streamIdx].Name == r.state.ActiveStream {
			currentLiveIdx = i
			break
		}
	}
	next := live[(currentLiveIdx+1)%len(live)]
	r.state.ActiveStream = r.state.Streams[next].Name
	r.state.Streams[next].LastUsedAt = time.Now().UTC()
	return r.state.ActiveStream, r.saveLocked()
}

// ActiveStream returns the currently selected live stream, or nil if none.
func (r *Registry) ActiveStream() *ipc.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.findStreamLocked(r.state.ActiveStream)
	if idx < 0 || !streamIsLive(r.state.Streams[idx]) {
		return nil
	}
	cp := r.state.Streams[idx]
	return &cp
}

// PruneDead marks streams whose target process/socket no longer exists as dead.
func (r *Registry) PruneDead() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var dead []string
	for i := range r.state.Streams {
		s := &r.state.Streams[i]
		if s.Status == ipc.StreamStatusDead || s.Target.TargetRef == "" {
			continue
		}
		if r.backend.Alive(s.Target) {
			s.Target.LastSeenAt = time.Now().UTC()
			continue
		}
		s.Status = ipc.StreamStatusDead
		dead = append(dead, s.Name)
	}
	if len(dead) == 0 {
		return nil
	}
	if r.state.ActiveStream != "" {
		idx := r.findStreamLocked(r.state.ActiveStream)
		if idx < 0 || !streamIsLive(r.state.Streams[idx]) {
			r.state.ActiveStream = r.firstLiveStreamNameLocked()
		}
	}
	_ = r.saveLocked()
	return dead
}

func (r *Registry) findStreamLocked(name string) int {
	for i, s := range r.state.Streams {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func (r *Registry) firstLiveStreamNameLocked() string {
	for _, s := range r.state.Streams {
		if streamIsLive(s) {
			return s.Name
		}
	}
	return ""
}

func streamIsLive(s ipc.Stream) bool {
	return s.Status == ipc.StreamStatusActive && s.Target.TargetType == ipc.TargetTypePTY && s.Target.TargetRef != ""
}

var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func streamIDFor(name string) string {
	return "stream_" + stableSlug(name)
}

func targetIDFor(name, targetRef string) string {
	return "target_" + stableSlug(name+"_"+targetRef)
}

func stableSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "unnamed"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%s_%08x", s, h.Sum32())
}
