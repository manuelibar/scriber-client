package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"scriber/internal/ipc"
)

// Registry tracks attached tmux panes. Thread-safe. Persists to disk after every mutation.
type Registry struct {
	path string

	mu     sync.Mutex
	state  registryFile
}

type registryFile struct {
	Active string     `json:"active"`
	Panes  []ipc.Pane `json:"panes"`
}

func NewRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
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
	return json.Unmarshal(data, &r.state)
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

func (r *Registry) Attach(req *ipc.AttachRequest) (*ipc.Pane, string, error) {
	if req.TMUXPane == "" {
		return nil, "", fmt.Errorf("not inside tmux ($TMUX_PANE empty); MVP supports tmux only")
	}

	session, err := TmuxSession(req.TMUXPane)
	if err != nil {
		return nil, "", fmt.Errorf("tmux pane %s not found: %w", req.TMUXPane, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	alias := req.Alias
	if alias == "" {
		alias = defaultAliasFor(req.TMUXPane, session)
	}
	alias = r.uniqueAliasLocked(alias, req.TMUXPane)

	// replace by pane_id if already present
	idx := -1
	for i, p := range r.state.Panes {
		if p.PaneID == req.TMUXPane {
			idx = i
			break
		}
	}
	pane := ipc.Pane{
		Alias:      alias,
		PaneID:     req.TMUXPane,
		Session:    session,
		AttachedAt: time.Now().UTC(),
		Mode:       "tmux",
	}
	if idx >= 0 {
		r.state.Panes[idx] = pane
	} else {
		r.state.Panes = append(r.state.Panes, pane)
	}
	r.state.Active = alias

	if err := r.saveLocked(); err != nil {
		return nil, "", err
	}
	slog.Info("attached pane", "alias", alias, "pane", pane.PaneID, "session", session)
	return &pane, "", nil
}

func defaultAliasFor(paneID, session string) string {
	// "%1" -> "1"; combined with session for readability
	id := strings.TrimPrefix(paneID, "%")
	if session == "" {
		return "pane-" + id
	}
	return session + "-" + id
}

// uniqueAliasLocked returns a non-colliding alias. If `desired` collides with a pane
// other than `currentPaneID`, suffixes "-2", "-3", ... are appended.
func (r *Registry) uniqueAliasLocked(desired, currentPaneID string) string {
	taken := map[string]bool{}
	for _, p := range r.state.Panes {
		if p.PaneID == currentPaneID {
			continue
		}
		taken[p.Alias] = true
	}
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

func (r *Registry) Detach(alias, tmuxPane string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	target := alias
	if target == "" {
		// detach current pane by tmux pane id
		for _, p := range r.state.Panes {
			if p.PaneID == tmuxPane {
				target = p.Alias
				break
			}
		}
	}
	if target == "" {
		return fmt.Errorf("no matching pane to detach (alias=%q tmux_pane=%q)", alias, tmuxPane)
	}

	idx := -1
	for i, p := range r.state.Panes {
		if p.Alias == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("alias %q not registered", target)
	}
	r.state.Panes = append(r.state.Panes[:idx], r.state.Panes[idx+1:]...)
	if r.state.Active == target {
		if len(r.state.Panes) > 0 {
			r.state.Active = r.state.Panes[0].Alias
		} else {
			r.state.Active = ""
		}
	}
	return r.saveLocked()
}

func (r *Registry) DetachAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Panes = nil
	r.state.Active = ""
	return r.saveLocked()
}

func (r *Registry) List() ([]ipc.Pane, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ipc.Pane, len(r.state.Panes))
	copy(out, r.state.Panes)
	return out, r.state.Active
}

func (r *Registry) Switch(alias string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.state.Panes {
		if p.Alias == alias {
			r.state.Active = alias
			return alias, r.saveLocked()
		}
	}
	return "", fmt.Errorf("alias %q not registered", alias)
}

func (r *Registry) Cycle() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.state.Panes) == 0 {
		return "", fmt.Errorf("no panes registered")
	}
	idx := -1
	for i, p := range r.state.Panes {
		if p.Alias == r.state.Active {
			idx = i
			break
		}
	}
	next := (idx + 1) % len(r.state.Panes)
	r.state.Active = r.state.Panes[next].Alias
	return r.state.Active, r.saveLocked()
}

// ActivePane returns the currently active pane, or nil if none.
func (r *Registry) ActivePane() *ipc.Pane {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.state.Panes {
		if p.Alias == r.state.Active {
			cp := p
			return &cp
		}
	}
	return nil
}

// PruneDead drops panes whose tmux pane no longer exists. Returns the aliases removed.
func (r *Registry) PruneDead() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var keep []ipc.Pane
	var removed []string
	for _, p := range r.state.Panes {
		if TmuxAlive(p.PaneID) {
			keep = append(keep, p)
		} else {
			removed = append(removed, p.Alias)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	r.state.Panes = keep
	if !aliasInList(r.state.Active, keep) {
		if len(keep) > 0 {
			r.state.Active = keep[0].Alias
		} else {
			r.state.Active = ""
		}
	}
	_ = r.saveLocked()
	return removed
}

func aliasInList(alias string, panes []ipc.Pane) bool {
	for _, p := range panes {
		if p.Alias == alias {
			return true
		}
	}
	return false
}
