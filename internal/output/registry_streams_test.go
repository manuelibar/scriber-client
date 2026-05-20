package output

import (
	"path/filepath"
	"testing"

	"scriber/internal/ipc"
)

type fakeTargetBackend struct {
	alive map[string]bool
}

func (f fakeTargetBackend) Alive(target ipc.Target) bool {
	return f.alive[target.TargetRef]
}

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

	streams, active := reg.Streams()
	if active != "workbench" {
		t.Fatalf("active = %q, want workbench", active)
	}
	if len(streams) != 1 || streams[0].Name != "workbench" {
		t.Fatalf("streams = %+v, want one workbench stream", streams)
	}
	selected := reg.ActiveStream()
	if selected == nil || selected.Name != "workbench" {
		t.Fatalf("ActiveStream() = %+v, want workbench", selected)
	}
}

func TestAttachWithoutNameGeneratesTerminalName(t *testing.T) {
	reg := newTestRegistry(t, fakeTargetBackend{alive: map[string]bool{"/tmp/term.sock": true}})

	stream, _, err := reg.Attach(&ipc.AttachRequest{
		PID:        4321,
		TTY:        "/dev/pts/3",
		TargetType: ipc.TargetTypePTY,
		TargetRef:  "/tmp/term.sock",
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if stream.Name != "term-3" {
		t.Fatalf("generated stream name = %q, want term-3", stream.Name)
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
	streams, _ := reg.Streams()
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
	streams, active := reg.Streams()
	if len(streams) != 1 || streams[0].Name != "codex-main" {
		t.Fatalf("streams after prune = %+v, want name preserved", streams)
	}
	if streams[0].Status != ipc.StreamStatusDead {
		t.Fatalf("status after prune = %q, want dead", streams[0].Status)
	}
	if active != "" {
		t.Fatalf("active after pruning only live stream = %q, want empty", active)
	}
}
