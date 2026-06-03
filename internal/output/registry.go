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
	ActiveSlot   int          `json:"active_slot,omitempty"`
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
		if language, err := ipc.NormalizeLanguage(s.Language); err == nil {
			s.Language = language
		} else {
			s.Language = ""
		}
		if s.Name != "" && seenNames[s.Name] {
			s.Name = uniqueName(s.Name, seenNames)
		}
		if s.Name != "" {
			seenNames[s.Name] = true
		}
		if s.ID == "" {
			s.ID = streamIDForStream(*s)
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
			s.Target.ID = targetIDForStream(*s, s.Target.TargetRef)
		}
		if s.Target.StreamID == "" {
			s.Target.StreamID = s.ID
		}
		if s.Target.Label == "" && s.Name != "" {
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
	if r.state.ActiveSlot < 0 || r.state.ActiveSlot > 9 {
		r.state.ActiveSlot = 0
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
	language, err := ipc.NormalizeLanguage(req.Language)
	if err != nil {
		return nil, "", err
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
	idx := -1
	if name != "" {
		idx = r.findStreamLocked(name)
	}
	message := ""
	if idx < 0 {
		r.state.Streams = append(r.state.Streams, ipc.Stream{
			Name:       name,
			AttachedAt: now,
		})
		idx = len(r.state.Streams) - 1
	} else if existing := r.state.Streams[idx].Target.TargetRef; existing != "" && existing != req.TargetRef {
		message = fmt.Sprintf("replaced previous target %s", existing)
	}

	stream := &r.state.Streams[idx]
	if stream.AttachedAt.IsZero() {
		stream.AttachedAt = now
	}
	stream.Name = name
	stream.Language = language
	stream.Status = ipc.StreamStatusActive
	stream.LastUsedAt = now
	if stream.Slot == 0 {
		stream.Slot = r.firstAvailableSlotLocked(idx)
	}
	if stream.ID == "" {
		stream.ID = streamIDForStream(*stream)
	}
	stream.Target = ipc.Target{
		ID:         targetIDForStream(*stream, req.TargetRef),
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
	r.setActiveLocked(*stream)

	if err := r.saveLocked(); err != nil {
		return nil, "", err
	}
	slog.Info("attached stream", "stream", streamLabel(*stream), "target", req.TargetRef, "pid", req.PID)
	cp := *stream
	return &cp, message, nil
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

func (r *Registry) Detach(name, targetRef string, slot int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := r.findDetachTargetLocked(strings.TrimSpace(name), targetRef, slot)
	if idx < 0 {
		if slot > 0 {
			return fmt.Errorf("no stream assigned to slot %d", slot)
		}
		return fmt.Errorf("no matching stream to detach (name=%q target_ref=%q)", name, targetRef)
	}
	target := r.state.Streams[idx]
	r.state.Streams = append(r.state.Streams[:idx], r.state.Streams[idx+1:]...)
	if r.isActiveLocked(target) {
		r.setActiveToFirstLiveLocked()
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

func (r *Registry) Streams() ([]ipc.Stream, string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ipc.Stream, len(r.state.Streams))
	copy(out, r.state.Streams)
	idx := r.activeIndexLocked()
	if idx < 0 {
		return out, "", 0
	}
	active := r.state.Streams[idx]
	return out, streamLabel(active), active.Slot
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
	r.setActiveLocked(r.state.Streams[idx])
	r.state.Streams[idx].LastUsedAt = time.Now().UTC()
	return streamLabel(r.state.Streams[idx]), r.saveLocked()
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
	if r.isActiveLocked(r.state.Streams[idx]) {
		r.setActiveLocked(r.state.Streams[idx])
	}
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
	wasActive := r.isActiveLocked(r.state.Streams[idx])
	r.state.Streams[idx].Slot = 0
	if wasActive {
		r.state.ActiveSlot = 0
	}
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
			return "", fmt.Errorf("%s in slot %d has no live target", streamLabel(s), slot)
		}
		r.setActiveLocked(s)
		r.state.Streams[i].LastUsedAt = time.Now().UTC()
		return streamLabel(r.state.Streams[i]), r.saveLocked()
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
		if r.isActiveLocked(r.state.Streams[streamIdx]) {
			currentLiveIdx = i
			break
		}
	}
	next := live[(currentLiveIdx+1)%len(live)]
	r.setActiveLocked(r.state.Streams[next])
	r.state.Streams[next].LastUsedAt = time.Now().UTC()
	return streamLabel(r.state.Streams[next]), r.saveLocked()
}

// ActiveStream returns the currently selected live stream, or nil if none.
func (r *Registry) ActiveStream() *ipc.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.activeIndexLocked()
	if idx < 0 || !streamIsLive(r.state.Streams[idx]) {
		return nil
	}
	cp := r.state.Streams[idx]
	return &cp
}

func (r *Registry) Stream(name string) *ipc.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.findStreamLocked(strings.TrimSpace(name))
	if idx < 0 {
		idx = r.findStreamByLabelLocked(strings.TrimSpace(name))
	}
	if idx < 0 || !streamIsLive(r.state.Streams[idx]) {
		return nil
	}
	cp := r.state.Streams[idx]
	return &cp
}

func (r *Registry) SendTextToStream(name, text string) (string, error) {
	if text == "" {
		return "", fmt.Errorf("text is empty")
	}
	stream := r.Stream(name)
	if stream == nil {
		return "", fmt.Errorf("stream %q has no live target", name)
	}
	if err := SendText(stream.Target, text); err != nil {
		_ = r.PruneDead()
		return streamLabel(*stream), err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.findStreamByIDLocked(stream.ID)
	if idx >= 0 {
		r.state.Streams[idx].LastUsedAt = time.Now().UTC()
		if err := r.saveLocked(); err != nil {
			return streamLabel(*stream), err
		}
	}
	return streamLabel(*stream), nil
}

// PruneDead marks streams whose target process/socket no longer exists as dead.
func (r *Registry) PruneDead() []string {
	r.mu.Lock()
	checks := make([]ipc.Stream, 0, len(r.state.Streams))
	for i := range r.state.Streams {
		s := r.state.Streams[i]
		if s.Status == ipc.StreamStatusDead || s.Target.TargetRef == "" {
			continue
		}
		checks = append(checks, s)
	}
	r.mu.Unlock()

	type liveness struct {
		id    string
		alive bool
	}
	results := make([]liveness, 0, len(checks))
	for _, stream := range checks {
		results = append(results, liveness{id: stream.ID, alive: r.backend.Alive(stream.Target)})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	aliveByID := map[string]bool{}
	for _, result := range results {
		aliveByID[result.id] = result.alive
	}
	now := time.Now().UTC()
	var dead []string
	for i := range r.state.Streams {
		s := &r.state.Streams[i]
		alive, checked := aliveByID[s.ID]
		if !checked || s.Status == ipc.StreamStatusDead || s.Target.TargetRef == "" {
			continue
		}
		if alive {
			s.Target.LastSeenAt = now
			continue
		}
		s.Status = ipc.StreamStatusDead
		dead = append(dead, streamLabel(*s))
	}
	if len(dead) == 0 {
		return nil
	}
	if r.activeIndexLocked() < 0 {
		r.setActiveToFirstLiveLocked()
	}
	_ = r.saveLocked()
	return dead
}

func (r *Registry) findDetachTargetLocked(name, targetRef string, slot int) int {
	if targetRef != "" {
		for i, s := range r.state.Streams {
			if s.Target.TargetRef == targetRef {
				return i
			}
		}
	}
	if slot > 0 {
		for i, s := range r.state.Streams {
			if s.Slot == slot {
				return i
			}
		}
	}
	if name != "" {
		return r.findStreamLocked(name)
	}
	return -1
}

func (r *Registry) findStreamLocked(name string) int {
	for i, s := range r.state.Streams {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func (r *Registry) findStreamByIDLocked(id string) int {
	if id == "" {
		return -1
	}
	for i, s := range r.state.Streams {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func (r *Registry) findStreamByLabelLocked(label string) int {
	if label == "" {
		return -1
	}
	for i, s := range r.state.Streams {
		if streamLabel(s) == label {
			return i
		}
	}
	return -1
}

func (r *Registry) activeIndexLocked() int {
	if r.state.ActiveSlot > 0 {
		for i, s := range r.state.Streams {
			if s.Slot == r.state.ActiveSlot && streamIsLive(s) {
				return i
			}
		}
	}
	if r.state.ActiveStream != "" {
		idx := r.findStreamLocked(r.state.ActiveStream)
		if idx >= 0 && streamIsLive(r.state.Streams[idx]) {
			return idx
		}
	}
	return -1
}

func (r *Registry) isActiveLocked(s ipc.Stream) bool {
	if r.state.ActiveSlot > 0 && s.Slot == r.state.ActiveSlot {
		return true
	}
	return r.state.ActiveStream != "" && s.Name == r.state.ActiveStream
}

func (r *Registry) setActiveLocked(s ipc.Stream) {
	r.state.ActiveSlot = s.Slot
	r.state.ActiveStream = strings.TrimSpace(s.Name)
}

func (r *Registry) setActiveToFirstLiveLocked() {
	r.state.ActiveStream = ""
	r.state.ActiveSlot = 0
	for _, s := range r.state.Streams {
		if streamIsLive(s) {
			r.setActiveLocked(s)
			return
		}
	}
}

func (r *Registry) firstAvailableSlotLocked(except int) int {
	used := map[int]bool{}
	for i, s := range r.state.Streams {
		if i == except {
			continue
		}
		if s.Slot >= 1 && s.Slot <= 9 {
			used[s.Slot] = true
		}
	}
	return firstAvailableSlot(used)
}

func firstAvailableSlot(used map[int]bool) int {
	for slot := 1; slot <= 9; slot++ {
		if !used[slot] {
			return slot
		}
	}
	return 0
}

func streamIsLive(s ipc.Stream) bool {
	return s.Status == ipc.StreamStatusActive && s.Target.TargetType == ipc.TargetTypePTY && s.Target.TargetRef != ""
}

func streamLabel(s ipc.Stream) string {
	if strings.TrimSpace(s.Name) != "" {
		return strings.TrimSpace(s.Name)
	}
	if s.Slot > 0 {
		return fmt.Sprintf("slot %d", s.Slot)
	}
	if s.ID != "" {
		return s.ID
	}
	return "(unnamed)"
}

var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func streamIDForStream(s ipc.Stream) string {
	if s.Slot > 0 {
		return fmt.Sprintf("stream_slot_%d", s.Slot)
	}
	if strings.TrimSpace(s.Name) != "" {
		return "stream_" + stableSlug(s.Name)
	}
	if s.Target.TargetRef != "" {
		return "stream_" + stableSlug(s.Target.TargetRef)
	}
	return "stream_unnamed"
}

func targetIDForStream(s ipc.Stream, targetRef string) string {
	return "target_" + stableSlug(streamIDForStream(s)+"_"+targetRef)
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
