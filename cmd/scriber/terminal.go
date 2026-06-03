package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"scriber/internal/config"
	"scriber/internal/ipc"
)

func runAttachedTerminal(ctx context.Context, streamName, language string, command []string) error {
	if !isTerminal(int(os.Stdin.Fd())) || !isTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("stt attach must run from an interactive terminal")
	}
	if len(command) == 0 {
		command = defaultShellCommand()
	}
	daemonClient := ipc.NewClient(config.SocketPath())
	if _, err := daemonClient.Monitor(); err != nil {
		return err
	}

	socketPath := config.TargetSocketPath(streamName, os.Getpid())
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cwd, _ := os.Getwd()
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	if streamName != "" {
		cmd.Env = append(cmd.Env, "STT_STREAM="+streamName)
	}
	if language != "" {
		cmd.Env = append(cmd.Env, "STT_LANGUAGE="+language)
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	injectSrv, err := startInjectServer(socketPath, ptmx)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	defer func() {
		_ = injectSrv.Shutdown(context.Background())
		_ = os.Remove(socketPath)
	}()

	tty, _ := os.Readlink("/proc/self/fd/0")
	resp, err := daemonClient.Attach(&ipc.AttachRequest{
		PID:        os.Getpid(),
		PPID:       os.Getppid(),
		TTY:        tty,
		CWD:        cwd,
		Term:       os.Getenv("TERM"),
		StreamName: streamName,
		Language:   language,
		TargetType: ipc.TargetTypePTY,
		TargetRef:  socketPath,
		Label:      streamName,
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	if resp.Stream.Language != "" {
		fmt.Fprintf(os.Stderr, "stt attached stream %q language=%s. Exit this shell to detach.\n", resp.Stream.Name, resp.Stream.Language)
	} else {
		fmt.Fprintf(os.Stderr, "stt attached stream %q. Exit this shell to detach.\n", resp.Stream.Name)
	}

	restore, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	defer restore()

	resizePTY(ptmx)
	resizeStop := watchResize(ctx, ptmx)
	defer resizeStop()

	go func() {
		_, _ = io.Copy(ptmx, os.Stdin)
	}()
	_, _ = io.Copy(os.Stdout, ptmx)

	err = cmd.Wait()
	_ = daemonClient.Detach(&ipc.DetachRequest{Name: streamName, TargetRef: socketPath})
	return normalizeExitError(err)
}

func defaultShellCommand() []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	switch filepath.Base(shell) {
	case "bash", "zsh", "sh", "dash":
		return []string{shell, "-l"}
	default:
		return []string{shell}
	}
}

func startInjectServer(socketPath string, dst io.Writer) (*http.Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen target socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}

	var writeMu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/inject", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ipc.InjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeMu.Lock()
		_, err := io.WriteString(dst, req.Text)
		writeMu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "stt target server error: %v\n", err)
		}
	}()
	return srv, nil
}

func resizePTY(ptmx *os.File) {
	_ = pty.InheritSize(os.Stdin, ptmx)
}

func watchResize(ctx context.Context, ptmx *os.File) func() {
	resizeCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(resizeCh, syscall.SIGWINCH)
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-resizeCh:
				if !ok {
					return
				}
				resizePTY(ptmx)
			}
		}
	}()
	return func() {
		signal.Stop(resizeCh)
		close(resizeCh)
		<-done
	}
}

func normalizeExitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() && status.Signal() == syscall.SIGHUP {
				return nil
			}
		}
	}
	return err
}

func isTerminal(fd int) bool {
	return term.IsTerminal(fd)
}

func makeRaw(fd int) (func(), error) {
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() {
		_ = term.Restore(fd, old)
	}, nil
}
