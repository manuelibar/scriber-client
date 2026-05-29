package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
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
		checkRuntimeDir(),
		checkDocker(),
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
			fix:  "ensure PipeWire/PulseAudio is running and the default input device is available",
		}
	}
	defer mic.Close()
	if err := mic.Start(); err != nil {
		return checkResult{name: "microphone", ok: false, msg: "start failed: " + err.Error()}
	}
	time.Sleep(250 * time.Millisecond)
	pcm, err := mic.Stop()
	if err != nil {
		return checkResult{name: "microphone", ok: false, msg: "stop failed: " + err.Error()}
	}
	if len(pcm) == 0 {
		return checkResult{
			name: "microphone",
			ok:   false,
			msg:  "captured 0 bytes in 250ms",
			fix:  "check the default PipeWire/PulseAudio input device and microphone permission",
		}
	}
	return checkResult{name: "microphone", ok: true, msg: fmt.Sprintf("captured %d bytes in 250ms", len(pcm))}
}

func checkRuntimeDir() checkResult {
	dir := config.RuntimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return checkResult{name: "runtime dir", ok: false, msg: err.Error()}
	}
	return checkResult{name: "runtime dir", ok: true, msg: dir}
}

func checkDocker() checkResult {
	docker, err := exec.LookPath("docker")
	if err != nil {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			candidate := filepath.Join(home, ".local", "bin", "docker")
			if st, statErr := os.Stat(candidate); statErr == nil && st.Mode()&0o111 != 0 {
				docker = candidate
				err = nil
			}
		}
	}
	if err != nil {
		return checkResult{name: "docker", ok: false, msg: "docker client not found", fix: "run ./setup-docker.sh from the repo root"}
	}
	cmd := exec.Command(docker, "info")
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(docker)+":/usr/local/bin:/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err != nil {
		return checkResult{
			name: "docker",
			ok:   false,
			msg:  "daemon not reachable: " + lastNonEmptyLine(string(out)),
			fix:  "run ./setup-docker.sh, then log out and back in if group membership changed",
		}
	}
	return checkResult{name: "docker", ok: true, msg: "client and daemon reachable"}
}

func lastNonEmptyLine(s string) string {
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

func checkServer(cfg *config.Config) checkResult {
	c := asr.New(cfg.Server.URL, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Healthz(ctx); err != nil {
		return checkResult{
			name: "stt-server",
			ok:   false,
			msg:  err.Error(),
			fix:  "run `stt start`",
		}
	}
	return checkResult{name: "stt-server", ok: true, msg: "healthy at " + cfg.Server.URL}
}

func checkDaemon() checkResult {
	procs, procErr := findDaemonProcesses()
	sock := config.SocketPath()
	if _, err := os.Stat(sock); err != nil {
		if procErr == nil && len(procs) > 0 {
			return checkResult{
				name: "stt daemon",
				ok:   false,
				msg:  fmt.Sprintf("socket missing, but daemon process pid(s)=%s still exist", formatPIDs(daemonProcessPIDs(procs))),
				fix:  "run `stt start --no-backend` to stop stale daemons and start one fresh",
			}
		}
		return checkResult{
			name: "stt daemon",
			ok:   false,
			msg:  "socket not present at " + sock,
			fix:  "run `stt start`",
		}
	}
	cli := ipc.NewClient(sock)
	resp, err := cli.Monitor()
	if err != nil {
		if procErr == nil && len(procs) > 0 {
			return checkResult{
				name: "stt daemon",
				ok:   false,
				msg:  fmt.Sprintf("socket is not reachable, but daemon process pid(s)=%s still exist", formatPIDs(daemonProcessPIDs(procs))),
				fix:  "run `stt start --no-backend` to stop stale daemons and start one fresh",
			}
		}
		return checkResult{name: "stt daemon", ok: false, msg: err.Error()}
	}
	if procErr != nil {
		return checkResult{
			name: "stt daemon",
			ok:   false,
			msg:  "running, but process scan failed: " + procErr.Error(),
			fix:  "ensure /proc is mounted and readable",
		}
	}
	if len(procs) > 1 {
		pid := "-"
		fix := "run `stt start --no-backend` to keep the reachable daemon and stop stale duplicates"
		if resp.PID > 0 {
			pid = fmt.Sprintf("%d", resp.PID)
		} else {
			fix = "run `stt shutdown --no-backend` and then `stt start --no-backend`"
		}
		return checkResult{
			name: "stt daemon",
			ok:   false,
			msg:  fmt.Sprintf("multiple daemon processes: socket pid=%s all pid(s)=%s", pid, formatPIDs(daemonProcessPIDs(procs))),
			fix:  fix,
		}
	}
	if len(procs) == 1 && resp.PID > 0 && procs[0].PID != resp.PID {
		return checkResult{
			name: "stt daemon",
			ok:   false,
			msg:  fmt.Sprintf("socket reports pid=%d, but process scan found pid=%d", resp.PID, procs[0].PID),
			fix:  "run `stt start --no-backend` to stop stale daemons and start one fresh",
		}
	}
	msg := fmt.Sprintf("running, state=%s, streams=%d", resp.State, len(resp.Streams))
	if resp.PID > 0 {
		msg = fmt.Sprintf("running, pid=%d, state=%s, streams=%d", resp.PID, resp.State, len(resp.Streams))
	}
	return checkResult{
		name: "stt daemon",
		ok:   true,
		msg:  msg,
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
