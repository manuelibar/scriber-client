package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scriber/internal/ipc"
)

type fakeTargetBackend struct {
	alive map[string]bool
}

func (f fakeTargetBackend) Alive(target ipc.Target) bool {
	return f.alive[target.TargetRef]
}

type targetBackendFunc func(ipc.Target) bool

func (f targetBackendFunc) Alive(target ipc.Target) bool { return f(target) }

func newTestRegistry(t *testing.T, backend fakeTargetBackend) *Registry {
	t.Helper()
	reg, err := NewRegistryWithBackend(filepath.Join(t.TempDir(), "registry.json"), backend)
	if err != nil {
		t.Fatalf("NewRegistryWithBackend() error = %v", err)
	}
	return reg
}

func attachReq(name, target string) *ipc.AttachRequest {
	return &ipc.AttachRequest{
		PID:        1234,
		StreamName: name,
		TargetType: ipc.TargetTypePTY,
		TargetRef:  target,
		TTY:        "/dev/pts/7",
		CWD:        "/tmp",
	}
}

func TestAttachCreatesAndSelectsNamedStream(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{alive: map[string]bool{"/tmp/workbench.sock": true}})

	stream, msg, err := reg.Attach(attachReq("workbench", "/tmp/workbench.sock"))
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if msg != "" {
		t.Fatalf("unexpected attach message: %q", msg)
	}
	if stream.Name != "workbench" {
		t.Fatalf("stream name = %q, want workbench", stream.Name)
	}
	if stream.Target.TargetType != ipc.TargetTypePTY || stream.Target.TargetRef != "/tmp/workbench.sock" {
		t.Fatalf("target = %+v, want pty /tmp/workbench.sock", stream.Target)
	}
	if stream.Slot != 1 {
		t.Fatalf("slot = %d, want first one-based slot 1", stream.Slot)
	}

	streams, active, activeSlot := reg.Streams()
	if active != "workbench" || activeSlot != 1 {
		t.Fatalf("active = %q slot=%d, want workbench slot=1", active, activeSlot)
	}
	if len(streams) != 1 || streams[0].Name != "workbench" {
		t.Fatalf("streams = %+v, want one workbench stream", streams)
	}
	selected := reg.ActiveStream()
	if selected == nil || selected.Name != "workbench" {
		t.Fatalf("ActiveStream() = %+v, want workbench", selected)
	}
}

func TestAttachAssignsFirstAvailableOneBasedSlot(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{
		alive: map[string]bool{
			"/tmp/one.sock":   true,
			"/tmp/two.sock":   true,
			"/tmp/three.sock": true,
		},
	})
	if _, _, err := reg.Attach(attachReq("one", "/tmp/one.sock")); err != nil {
		t.Fatalf("Attach(one) error = %v", err)
	}
	if _, _, err := reg.Attach(attachReq("two", "/tmp/two.sock")); err != nil {
		t.Fatalf("Attach(two) error = %v", err)
	}
	if _, err := reg.ClearSlot("one"); err != nil {
		t.Fatalf("ClearSlot(one) error = %v", err)
	}
	three, _, err := reg.Attach(attachReq("three", "/tmp/three.sock"))
	if err != nil {
		t.Fatalf("Attach(three) error = %v", err)
	}
	if three.Slot != 1 {
		t.Fatalf("slot = %d, want first available slot 1", three.Slot)
	}
}

func TestAttachWithoutNameUsesFirstAvailableSlotWithoutAssigningName(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{alive: map[string]bool{
		"/tmp/one.sock": true,
		"/tmp/two.sock": true,
	}})

	first, _, err := reg.Attach(&ipc.AttachRequest{
		PID:        4321,
		TTY:        "/dev/pts/8",
		TargetType: ipc.TargetTypePTY,
		TargetRef:  "/tmp/one.sock",
	})
	if err != nil {
		t.Fatalf("Attach(first) error = %v", err)
	}
	if first.Name != "" || first.Slot != 1 {
		t.Fatalf("first stream name=%q slot=%d, want empty name slot=1", first.Name, first.Slot)
	}

	second, _, err := reg.Attach(&ipc.AttachRequest{
		PID:        4322,
		TTY:        "/dev/pts/1",
		TargetType: ipc.TargetTypePTY,
		TargetRef:  "/tmp/two.sock",
	})
	if err != nil {
		t.Fatalf("Attach(second) error = %v", err)
	}
	if second.Name != "" || second.Slot != 2 {
		t.Fatalf("second stream name=%q slot=%d, want empty name slot=2", second.Name, second.Slot)
	}

	streams, active, activeSlot := reg.Streams()
	if len(streams) != 2 {
		t.Fatalf("streams len = %d, want 2", len(streams))
	}
	if active != "slot 2" || activeSlot != 2 {
		t.Fatalf("active = %q slot=%d, want slot 2 / 2", active, activeSlot)
	}
	if streams[0].ID != "stream_slot_1" || streams[1].ID != "stream_slot_2" {
		t.Fatalf("stream IDs = [%q %q], want slot IDs", streams[0].ID, streams[1].ID)
	}
}

func TestDetachBySlotRemovesUnnamedStream(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{alive: map[string]bool{
		"/tmp/one.sock": true,
		"/tmp/two.sock": true,
	}})
	if _, _, err := reg.Attach(&ipc.AttachRequest{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/one.sock"}); err != nil {
		t.Fatalf("Attach(first) error = %v", err)
	}
	if _, _, err := reg.Attach(&ipc.AttachRequest{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/two.sock"}); err != nil {
		t.Fatalf("Attach(second) error = %v", err)
	}

	if err := reg.Detach("", "", 1); err != nil {
		t.Fatalf("Detach(slot 1) error = %v", err)
	}

	streams, active, activeSlot := reg.Streams()
	if len(streams) != 1 {
		t.Fatalf("streams len = %d, want 1", len(streams))
	}
	if streams[0].Slot != 2 {
		t.Fatalf("remaining slot = %d, want 2", streams[0].Slot)
	}
	if active != "slot 2" || activeSlot != 2 {
		t.Fatalf("active = %q slot=%d, want slot 2 / 2", active, activeSlot)
	}
}

func TestStreamFindsUnnamedStreamBySlotLabel(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{alive: map[string]bool{
		"/tmp/one.sock": true,
	}})
	if _, _, err := reg.Attach(&ipc.AttachRequest{TargetType: ipc.TargetTypePTY, TargetRef: "/tmp/one.sock"}); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	stream := reg.Stream("slot 1")
	if stream == nil || stream.Slot != 1 || stream.Target.TargetRef != "/tmp/one.sock" {
		t.Fatalf("Stream(slot 1) = %+v, want unnamed slot 1 stream", stream)
	}
}

func TestLoadDoesNotBackfillSlotlessStreams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	state := struct {
		ActiveStream string       `json:"active_stream,omitempty"`
		Streams      []ipc.Stream `json:"streams,omitempty"`
	}{
		ActiveStream: "alpha",
		Streams: []ipc.Stream{
			{
				Name:   "alpha",
				Status: ipc.StreamStatusActive,
				Target: ipc.Target{
					TargetType: ipc.TargetTypePTY,
					TargetRef:  "/tmp/alpha.sock",
				},
			},
			{
				Name:   "beta",
				Status: ipc.StreamStatusActive,
				Target: ipc.Target{
					TargetType: ipc.TargetTypePTY,
					TargetRef:  "/tmp/beta.sock",
				},
			},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistryWithBackend(path, fakeTargetBackend{})
	if err != nil {
		t.Fatalf("NewRegistryWithBackend() error = %v", err)
	}
	streams, active, activeSlot := reg.Streams()
	if active != "alpha" || activeSlot != 0 {
		t.Fatalf("active = %q slot=%d, want alpha slot=0", active, activeSlot)
	}
	if len(streams) != 2 {
		t.Fatalf("streams len = %d, want 2", len(streams))
	}
	if streams[0].Slot != 0 || streams[1].Slot != 0 {
		t.Fatalf("slots = [%d %d], want [0 0]", streams[0].Slot, streams[1].Slot)
	}

	var persisted struct {
		ActiveStream string       `json:"active_stream,omitempty"`
		Streams      []ipc.Stream `json:"streams,omitempty"`
	}
	persistedData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ActiveStream != "alpha" {
		t.Fatalf("persisted active = %q, want alpha", persisted.ActiveStream)
	}
	if len(persisted.Streams) != 2 || persisted.Streams[0].Slot != 0 || persisted.Streams[1].Slot != 0 {
		t.Fatalf("persisted streams = %+v, want slotless streams", persisted.Streams)
	}
}

func TestSelectAndCycleOperateOnStreams(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{
		alive: map[string]bool{"/tmp/codex.sock": true, "/tmp/notes.sock": true},
	})
	if _, _, err := reg.Attach(attachReq("codex-main", "/tmp/codex.sock")); err != nil {
		t.Fatalf("Attach(codex-main) error = %v", err)
	}
	if _, _, err := reg.Attach(attachReq("notes", "/tmp/notes.sock")); err != nil {
		t.Fatalf("Attach(notes) error = %v", err)
	}

	active, err := reg.Select("codex-main")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if active != "codex-main" {
		t.Fatalf("Select active = %q, want codex-main", active)
	}
	active, err = reg.Cycle()
	if err != nil {
		t.Fatalf("Cycle() error = %v", err)
	}
	if active != "notes" {
		t.Fatalf("Cycle active = %q, want notes", active)
	}
}

func TestSetSlotAndSelectSlotOperateOnStreams(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{
		alive: map[string]bool{"/tmp/codex.sock": true, "/tmp/notes.sock": true},
	})
	if _, _, err := reg.Attach(attachReq("codex-main", "/tmp/codex.sock")); err != nil {
		t.Fatalf("Attach(codex-main) error = %v", err)
	}
	if _, _, err := reg.Attach(attachReq("notes", "/tmp/notes.sock")); err != nil {
		t.Fatalf("Attach(notes) error = %v", err)
	}

	stream, err := reg.SetSlot("codex-main", 1)
	if err != nil {
		t.Fatalf("SetSlot(codex-main, 1) error = %v", err)
	}
	if stream.Slot != 1 {
		t.Fatalf("slot = %d, want 1", stream.Slot)
	}
	if _, err := reg.SetSlot("notes", 1); err != nil {
		t.Fatalf("SetSlot(notes, 1) error = %v", err)
	}
	streams, _, _ := reg.Streams()
	slots := map[string]int{}
	for _, s := range streams {
		slots[s.Name] = s.Slot
	}
	if slots["codex-main"] != 0 || slots["notes"] != 1 {
		t.Fatalf("slots after reassignment = %v, want codex-main cleared and notes=1", slots)
	}

	active, err := reg.SelectSlot(1)
	if err != nil {
		t.Fatalf("SelectSlot(1) error = %v", err)
	}
	if active != "notes" {
		t.Fatalf("active = %q, want notes", active)
	}
}

func TestPruneDeadMarksStreamDeadWithoutDeletingName(t *testing.T) {
	backend := fakeTargetBackend{alive: map[string]bool{"/tmp/codex.sock": true}}
	reg := newTestRegistry(t, backend)
	if _, _, err := reg.Attach(attachReq("codex-main", "/tmp/codex.sock")); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	backend.alive["/tmp/codex.sock"] = false
	dead := reg.PruneDead()
	if len(dead) != 1 || dead[0] != "codex-main" {
		t.Fatalf("PruneDead() = %v, want [codex-main]", dead)
	}
	streams, active, activeSlot := reg.Streams()
	if len(streams) != 1 || streams[0].Name != "codex-main" {
		t.Fatalf("streams after prune = %+v, want name preserved", streams)
	}
	if streams[0].Status != ipc.StreamStatusDead {
		t.Fatalf("status after prune = %q, want dead", streams[0].Status)
	}
	if active != "" || activeSlot != 0 {
		t.Fatalf("active after pruning only live stream = %q slot=%d, want empty slot=0", active, activeSlot)
	}
}

func TestPruneDeadDoesNotHoldRegistryLockDuringLivenessCheck(t *testing.T) {
	var reg *Registry
	backend := targetBackendFunc(func(target ipc.Target) bool {
		done := make(chan struct{})
		go func() {
			_, _, _ = reg.Streams()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("registry lock was held while checking target liveness")
		}
		return true
	})
	var err error
	reg, err = NewRegistryWithBackend(filepath.Join(t.TempDir(), "registry.json"), backend)
	if err != nil {
		t.Fatalf("NewRegistryWithBackend() error = %v", err)
	}
	if _, _, err := reg.Attach(attachReq("codex-main", "/tmp/codex.sock")); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	if dead := reg.PruneDead(); len(dead) != 0 {
		t.Fatalf("PruneDead() = %v, want no dead streams", dead)
	}
}
