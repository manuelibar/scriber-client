package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"scriber/internal/config"
)

type daemonProcess struct {
	PID     int
	Cmdline string
}

var findDaemonProcesses = discoverDaemonProcesses

func discoverDaemonProcesses() ([]daemonProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []daemonProcess
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		args := procCmdlineArgs(data)
		if !isSTTDaemonCmdline(args) {
			continue
		}
		procs = append(procs, daemonProcess{
			PID:     pid,
			Cmdline: strings.Join(args, " "),
		})
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	return procs, nil
}

func procCmdlineArgs(data []byte) []string {
	raw := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	args := raw[:0]
	for _, arg := range raw {
		if arg != "" {
			args = append(args, arg)
		}
	}
	return args
}

func isSTTDaemonCmdline(args []string) bool {
	if len(args) < 2 || args[1] != "daemon" {
		return false
	}
	switch filepath.Base(args[0]) {
	case "stt", "scriber":
		return true
	default:
		return false
	}
}

func acquireDaemonLock() (*os.File, error) {
	if err := os.MkdirAll(config.RuntimeDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(config.RuntimeDir(), "daemon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("another stt daemon already holds %s", path)
		}
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.Seek(0, 0)
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return f, nil
}

func releaseDaemonLock(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func otherDaemonProcesses(procs []daemonProcess, keepPID int) []daemonProcess {
	self := os.Getpid()
	out := make([]daemonProcess, 0, len(procs))
	for _, proc := range procs {
		if proc.PID == self || proc.PID == keepPID {
			continue
		}
		out = append(out, proc)
	}
	return out
}

func daemonProcessPIDs(procs []daemonProcess) []int {
	pids := make([]int, 0, len(procs))
	for _, proc := range procs {
		pids = append(pids, proc.PID)
	}
	sort.Ints(pids)
	return pids
}

func formatPIDs(pids []int) string {
	if len(pids) == 0 {
		return "-"
	}
	parts := make([]string, len(pids))
	for i, pid := range pids {
		parts[i] = strconv.Itoa(pid)
	}
	return strings.Join(parts, ",")
}

func terminateDaemonProcesses(ctx context.Context, keepPID int) ([]int, error) {
	procs, err := findDaemonProcesses()
	if err != nil {
		return nil, fmt.Errorf("list stt daemon processes: %w", err)
	}
	targets := otherDaemonProcesses(procs, keepPID)
	if len(targets) == 0 {
		return nil, nil
	}
	return terminateDaemonProcessList(ctx, targets)
}

func terminateDaemonProcessList(ctx context.Context, targets []daemonProcess) ([]int, error) {
	pids := daemonProcessPIDs(targets)
	for _, pid := range pids {
		if err := signalPID(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return pids, fmt.Errorf("stop stale stt daemon pid %d: %w", pid, err)
		}
	}

	remaining, err := waitForPIDs(ctx, pids, 2*time.Second)
	if err != nil {
		return pids, err
	}
	if len(remaining) == 0 {
		return pids, nil
	}

	for _, pid := range remaining {
		if err := signalPID(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return pids, fmt.Errorf("kill stale stt daemon pid %d: %w", pid, err)
		}
	}
	remaining, err = waitForPIDs(ctx, remaining, 2*time.Second)
	if err != nil {
		return pids, err
	}
	if len(remaining) > 0 {
		return pids, fmt.Errorf("stale stt daemon pid(s) still running: %s", formatPIDs(remaining))
	}
	return pids, nil
}

func signalPID(pid int, sig syscall.Signal) error {
	if pid <= 0 || pid == os.Getpid() {
		return nil
	}
	return syscall.Kill(pid, sig)
}

func waitForPIDs(ctx context.Context, pids []int, timeout time.Duration) ([]int, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := livePIDs(pids)
		if len(remaining) == 0 || time.Now().After(deadline) {
			return remaining, nil
		}
		select {
		case <-ctx.Done():
			return remaining, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func livePIDs(pids []int) []int {
	remaining := make([]int, 0, len(pids))
	for _, pid := range pids {
		err := syscall.Kill(pid, 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			remaining = append(remaining, pid)
		}
	}
	return remaining
}
