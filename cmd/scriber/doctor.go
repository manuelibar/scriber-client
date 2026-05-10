package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"scriber/internal/asr"
	"scriber/internal/audio"
	"scriber/internal/config"
	"scriber/internal/hotkey"
	"scriber/internal/ipc"

	"github.com/holoplot/go-evdev"
)

type checkResult struct {
	name string
	ok   bool
	msg  string
	fix  string
}

func runDoctor() error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	checks := []checkResult{
		checkInputGroup(),
		checkEvdev(),
		checkMic(cfg),
		checkTmux(),
		checkServer(cfg),
		checkDaemon(),
	}

	allOK := true
	for _, c := range checks {
		mark := "[ok]"
		if !c.ok {
			mark = "[FAIL]"
			allOK = false
		}
		fmt.Printf("%-7s %s — %s\n", mark, c.name, c.msg)
		if !c.ok && c.fix != "" {
			fmt.Printf("        fix: %s\n", c.fix)
		}
	}

	if !allOK {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

func checkInputGroup() checkResult {
	u, err := user.Current()
	if err != nil {
		return checkResult{name: "input group", ok: false, msg: "cannot read current user: " + err.Error()}
	}
	gids, err := u.GroupIds()
	if err != nil {
		return checkResult{name: "input group", ok: false, msg: "cannot list groups: " + err.Error()}
	}
	for _, gid := range gids {
		g, err := user.LookupGroupId(gid)
		if err == nil && g.Name == "input" {
			return checkResult{name: "input group", ok: true, msg: "user is in 'input' group"}
		}
	}
	return checkResult{
		name: "input group",
		ok:   false,
		msg:  fmt.Sprintf("user %q is not in 'input' group; evdev keyboards are unreadable", u.Username),
		fix:  "sudo usermod -aG input $USER && relogin",
	}
}

func checkEvdev() checkResult {
	paths, err := evdev.ListDevicePaths()
	if err != nil {
		return checkResult{name: "evdev devices", ok: false, msg: err.Error(), fix: "ensure /dev/input/event* are accessible"}
	}
	keyboards := 0
	for _, p := range paths {
		d, err := evdev.Open(p.Path)
		if err != nil {
			continue
		}
		if isKeyboardDevice(d) {
			keyboards++
		}
		d.Close()
	}
	if keyboards == 0 {
		return checkResult{
			name: "evdev devices",
			ok:   false,
			msg:  "no readable keyboard devices found",
			fix:  "check 'input' group membership; relogin after group change",
		}
	}
	return checkResult{name: "evdev devices", ok: true, msg: fmt.Sprintf("%d keyboard device(s) readable", keyboards)}
}

func checkMic(cfg *config.Config) checkResult {
	mic, err := audio.New(cfg.Audio.SampleRate)
	if err != nil {
		return checkResult{
			name: "microphone",
			ok:   false,
			msg:  err.Error(),
			fix:  "ensure pipewire-pulse is running (systemctl --user status pipewire-pulse)",
		}
	}
	defer mic.Close()
	if err := mic.Start(); err != nil {
		return checkResult{name: "microphone", ok: false, msg: "start failed: " + err.Error()}
	}
	time.Sleep(50 * time.Millisecond)
	pcm, err := mic.Stop()
	if err != nil {
		return checkResult{name: "microphone", ok: false, msg: "stop failed: " + err.Error()}
	}
	return checkResult{name: "microphone", ok: true, msg: fmt.Sprintf("captured %d bytes in 50ms", len(pcm))}
}

func checkTmux() checkResult {
	if _, err := exec.LookPath("tmux"); err != nil {
		return checkResult{name: "tmux", ok: false, msg: "not found in $PATH", fix: "sudo apt install tmux"}
	}
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return checkResult{name: "tmux", ok: false, msg: err.Error()}
	}
	return checkResult{name: "tmux", ok: true, msg: strings.TrimSpace(string(out))}
}

func checkServer(cfg *config.Config) checkResult {
	c := asr.New(cfg.Server.URL, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Healthz(ctx); err != nil {
		return checkResult{
			name: "scriber-server",
			ok:   false,
			msg:  err.Error(),
			fix:  "systemctl --user start scriber-server (and watch journalctl --user -u scriber-server)",
		}
	}
	return checkResult{name: "scriber-server", ok: true, msg: "healthy at " + cfg.Server.URL}
}

func checkDaemon() checkResult {
	sock := config.SocketPath()
	if _, err := os.Stat(sock); err != nil {
		return checkResult{
			name: "scriber daemon",
			ok:   false,
			msg:  "socket not present at " + sock,
			fix:  "systemctl --user start scriber-daemon (or run `scriber daemon`)",
		}
	}
	cli := ipc.NewClient(sock)
	resp, err := cli.Status()
	if err != nil {
		return checkResult{name: "scriber daemon", ok: false, msg: err.Error()}
	}
	return checkResult{
		name: "scriber daemon",
		ok:   true,
		msg:  fmt.Sprintf("running, state=%s, panes=%d", resp.State, resp.PaneCount),
	}
}

// Compile-time assertion that hotkey.ParseKey is exported (helps catch refactors).
var _ = hotkey.ParseKey

func isKeyboardDevice(d *evdev.InputDevice) bool {
	hasKey := false
	for _, t := range d.CapableTypes() {
		if t == evdev.EV_KEY {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return false
	}
	for _, c := range d.CapableEvents(evdev.EV_KEY) {
		if c == evdev.KEY_A {
			return true
		}
	}
	return false
}
