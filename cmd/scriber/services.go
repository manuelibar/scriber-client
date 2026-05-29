package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"scriber/internal/asr"
	"scriber/internal/config"
	"scriber/internal/ipc"
)

func startCmd() *cobra.Command {
	var repoDir string
	var noBackend bool
	var noDaemon bool
	var noBuild bool

	c := &cobra.Command{
		Use:   "start",
		Short: "Start the STT backend and host daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if noBackend && noDaemon {
				return fmt.Errorf("nothing to start")
			}
			if !noBackend {
				repo, err := resolveRepoDir(repoDir)
				if err != nil {
					return err
				}
				if err := ensurePrivateConfig(repo); err != nil {
					return err
				}
				composeArgs := []string{"up", "-d"}
				if !noBuild {
					composeArgs = append(composeArgs, "--build")
				}
				if err := runCompose(cmd.Context(), repo, composeArgs...); err != nil {
					return err
				}
				if err := waitForBackend(cmd.Context()); err != nil {
					return err
				}
			}
			if !noDaemon {
				return startDaemon(cmd.Context())
			}
			return nil
		},
	}
	c.Flags().StringVar(&repoDir, "repo", "", "scriber repo path for backend orchestration")
	c.Flags().BoolVar(&noBackend, "no-backend", false, "do not start Docker backend services")
	c.Flags().BoolVar(&noDaemon, "no-daemon", false, "do not start the host daemon")
	c.Flags().BoolVar(&noBuild, "no-build", false, "skip Docker image rebuild")
	return c
}

func shutdownCmd() *cobra.Command {
	var repoDir string
	var noBackend bool
	var noDaemon bool

	c := &cobra.Command{
		Use:     "shutdown",
		Aliases: []string{"stop"},
		Short:   "Gracefully stop the STT daemon and backend",
		RunE: func(cmd *cobra.Command, args []string) error {
			if noBackend && noDaemon {
				return fmt.Errorf("nothing to stop")
			}
			if !noDaemon {
				if err := shutdownDaemon(cmd.Context()); err != nil {
					fmt.Fprintf(os.Stderr, "stt daemon: %v\n", err)
				}
			}
			if !noBackend {
				repo, err := resolveRepoDir(repoDir)
				if err != nil {
					return err
				}
				if err := runCompose(cmd.Context(), repo, "down"); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&repoDir, "repo", "", "scriber repo path for backend orchestration")
	c.Flags().BoolVar(&noBackend, "no-backend", false, "do not stop Docker backend services")
	c.Flags().BoolVar(&noDaemon, "no-daemon", false, "do not stop the host daemon")
	return c
}

func resolveRepoDir(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if env := os.Getenv("STT_REPO"); env != "" {
		candidates = append(candidates, env)
	}
	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			candidates = append(candidates, dir)
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Projects", "scriber"))
	}

	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(config.ExpandPath(candidate))
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if isScriberRepo(abs) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("cannot find scriber repo; run from the repo, pass --repo, or set STT_REPO")
}

func isScriberRepo(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "scripts", "stt-docker-compose.sh"))
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.private.yml.example")); err != nil {
		return false
	}
	return true
}

func ensurePrivateConfig(repo string) error {
	privateDir := filepath.Join(repo, ".private")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", privateDir, err)
	}

	envPath := filepath.Join(privateDir, ".env")
	if _, err := os.Stat(envPath); errors.Is(err, os.ErrNotExist) {
		env := "STT_WHISPER_MODEL=base.en\n" +
			"STT_WHISPER_DEVICE=auto\n" +
			"STT_WHISPER_LANGUAGE=en\n" +
			"STT_SILENCE_RMS_THRESHOLD=0.0005\n" +
			"BUILD_CACHE_MODEL=1\n"
		if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", envPath, err)
		}
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", envPath, err)
	}

	composePath := filepath.Join(privateDir, "docker-compose.yml")
	if _, err := os.Stat(composePath); errors.Is(err, os.ErrNotExist) {
		templatePath := filepath.Join(repo, "docker-compose.private.yml.example")
		data, err := os.ReadFile(templatePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", templatePath, err)
		}
		if err := os.WriteFile(composePath, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", composePath, err)
		}
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", composePath, err)
	}
	return nil
}

func runCompose(ctx context.Context, repo string, args ...string) error {
	script := filepath.Join(repo, "scripts", "stt-docker-compose.sh")
	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Dir = repo
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Run()
}

func waitForBackend(ctx context.Context) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	client := asr.New(cfg.Server.URL, 2*time.Second)
	deadline := time.Now().Add(60 * time.Second)
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := client.Healthz(checkCtx)
		cancel()
		if err == nil {
			fmt.Println("stt backend healthy")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("stt backend did not become healthy: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

func startDaemon(ctx context.Context) error {
	cli := ipc.NewClient(config.SocketPath())
	if resp, err := cli.Monitor(); err == nil {
		if resp.PID > 0 {
			if stopped, err := terminateDaemonProcesses(ctx, resp.PID); err != nil {
				return err
			} else if len(stopped) > 0 {
				fmt.Printf("stopped stale stt daemon pid(s): %s\n", formatPIDs(stopped))
			}
		} else if procs, err := findDaemonProcesses(); err == nil && len(procs) > 1 {
			return fmt.Errorf("multiple stt daemon processes are running, but the reachable daemon does not report its pid; run `stt shutdown --no-backend` and then `stt start --no-backend`")
		}
		if resp.PID > 0 {
			fmt.Printf("stt daemon already running pid=%d state=%s streams=%d\n", resp.PID, resp.State, len(resp.Streams))
		} else {
			fmt.Printf("stt daemon already running, state=%s streams=%d\n", resp.State, len(resp.Streams))
		}
		return nil
	}

	if stopped, err := terminateDaemonProcesses(ctx, 0); err != nil {
		return err
	} else if len(stopped) > 0 {
		fmt.Printf("stopped stale stt daemon pid(s): %s\n", formatPIDs(stopped))
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	stateDir := filepath.Join(home, ".local", "state", "stt")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", stateDir, err)
	}
	logPath := filepath.Join(stateDir, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start stt daemon: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release stt daemon process: %w", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for {
		resp, err := cli.Monitor()
		if err == nil {
			keepPID := resp.PID
			if keepPID == 0 {
				keepPID = pid
			}
			if stopped, err := terminateDaemonProcesses(ctx, keepPID); err != nil {
				return err
			} else if len(stopped) > 0 {
				fmt.Printf("stopped stale stt daemon pid(s): %s\n", formatPIDs(stopped))
			}
			fmt.Printf("stt daemon started pid=%d state=%s log=%s\n", keepPID, resp.State, logPath)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("stt daemon did not become reachable; see %s", logPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func shutdownDaemon(ctx context.Context) error {
	cli := ipc.NewClient(config.SocketPath())
	ipcErr := cli.Shutdown()

	var waitErr error
	if ipcErr == nil {
		waitErr = waitForDaemonShutdown(ctx, cli)
		if waitErr == nil {
			fmt.Println("stt daemon stopped")
		}
	}

	stopped, stopErr := terminateDaemonProcesses(ctx, 0)
	if stopErr != nil {
		return stopErr
	}
	if len(stopped) > 0 {
		fmt.Printf("stopped stale stt daemon pid(s): %s\n", formatPIDs(stopped))
	}
	if waitErr != nil && len(stopped) == 0 {
		return waitErr
	}
	if ipcErr != nil && len(stopped) == 0 {
		return ipcErr
	}
	return nil
}

func waitForDaemonShutdown(ctx context.Context, cli *ipc.Client) error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := cli.Monitor(); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for daemon shutdown")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
