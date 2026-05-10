package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/holoplot/go-evdev"
	"github.com/spf13/cobra"

	"scriber/internal/asr"
	"scriber/internal/audio"
	"scriber/internal/config"
	"scriber/internal/hotkey"
	"scriber/internal/ipc"
	"scriber/internal/notify"
	"scriber/internal/output"
	"scriber/internal/persist"
)

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the scriber daemon (hotkey loop + audio + IPC + tmux output)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			return runDaemon(cmd.Context(), cfg)
		},
	}
}

type daemonState struct {
	mu             sync.Mutex
	fsmState       string
	serverOK       bool
	lastTranscript string
	lastAt         time.Time
}

func (d *daemonState) State() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fsmState
}

func (d *daemonState) LastTranscript() (string, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastTranscript, d.lastAt
}

func (d *daemonState) ServerOK() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.serverOK
}

func (d *daemonState) setState(s string) {
	d.mu.Lock()
	d.fsmState = s
	d.mu.Unlock()
}

func (d *daemonState) setLastTranscript(t string) {
	d.mu.Lock()
	d.lastTranscript = t
	d.lastAt = time.Now()
	d.mu.Unlock()
}

func (d *daemonState) setServerOK(ok bool) {
	d.mu.Lock()
	d.serverOK = ok
	d.mu.Unlock()
}

func runDaemon(parent context.Context, cfg *config.Config) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	talkCode, err := hotkey.ParseKey(cfg.Hotkey.TalkKey)
	if err != nil {
		return fmt.Errorf("talk_key: %w", err)
	}
	cycleCode, err := hotkey.ParseKey(cfg.Hotkey.CycleKey)
	if err != nil {
		return fmt.Errorf("cycle_key: %w", err)
	}

	watched := map[evdev.EvCode]bool{talkCode: true, cycleCode: true}
	evChan, err := hotkey.Listen(ctx, watched)
	if err != nil {
		return fmt.Errorf("hotkey listen: %w", err)
	}

	mic, err := audio.New(cfg.Audio.SampleRate)
	if err != nil {
		return fmt.Errorf("audio init: %w", err)
	}
	defer mic.Close()

	asrClient := asr.New(cfg.Server.URL, time.Duration(cfg.Server.TimeoutMs)*time.Millisecond)

	reg, err := output.NewRegistry(cfg.Storage.RegistryPath)
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}

	state := &daemonState{fsmState: hotkey.StateIdle.String()}

	ipcSrv := ipc.NewServer(config.SocketPath(), reg, state)
	go func() {
		if err := ipcSrv.Serve(ctx); err != nil {
			slog.Error("ipc server", "err", err)
		}
	}()

	go healthLoop(ctx, asrClient, state)
	go pruneLoop(ctx, reg, cfg.UI.Notifications)

	talkEvents := make(chan hotkey.Event, 16)
	go splitEvents(ctx, evChan, talkCode, cycleCode, talkEvents, reg, cfg.UI.Notifications)

	actions := make(chan hotkey.Action, 8)
	go hotkey.Run(ctx, hotkey.FSMConfig{
		HoldThreshold:   time.Duration(cfg.Hotkey.HoldThresholdMs) * time.Millisecond,
		DoubleTapWindow: time.Duration(cfg.Hotkey.DoubleTapWindowMs) * time.Millisecond,
	}, talkEvents, actions)

	slog.Info("scriber daemon ready", "talk", cfg.Hotkey.TalkKey, "cycle", cfg.Hotkey.CycleKey)

	for {
		select {
		case <-ctx.Done():
			slog.Info("daemon stopping")
			return nil
		case action := <-actions:
			handleAction(ctx, action, mic, asrClient, reg, cfg, state)
		}
	}
}

func splitEvents(ctx context.Context, in <-chan hotkey.Event, talkCode, cycleCode evdev.EvCode, talkOut chan<- hotkey.Event, reg *output.Registry, notifyOn bool) {
	defer close(talkOut)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-in:
			if !ok {
				return
			}
			switch ev.Code {
			case talkCode:
				select {
				case talkOut <- ev:
				case <-ctx.Done():
					return
				}
			case cycleCode:
				if ev.Kind == hotkey.KeyDown {
					handleCycle(reg, notifyOn)
				}
			}
		}
	}
}

func handleAction(ctx context.Context, action hotkey.Action, mic *audio.Capture, asrClient *asr.Client, reg *output.Registry, cfg *config.Config, state *daemonState) {
	switch action {
	case hotkey.ActionStartCapture:
		state.setState("Recording")
		if err := mic.Start(); err != nil {
			slog.Error("audio start", "err", err)
			if cfg.UI.Notifications {
				notify.Send("scriber error", "audio start: "+err.Error())
			}
			state.setState(hotkey.StateIdle.String())
		}
	case hotkey.ActionStopAndSend:
		state.setState("Transcribing")
		pcm, err := mic.Stop()
		if err != nil {
			slog.Error("audio stop", "err", err)
			state.setState(hotkey.StateIdle.String())
			return
		}
		if len(pcm) == 0 {
			slog.Warn("empty audio buffer")
			state.setState(hotkey.StateIdle.String())
			return
		}
		go transcribeAndSend(ctx, pcm, asrClient, reg, cfg, state)
	case hotkey.ActionDiscardCapture:
		_ = mic.Discard()
		state.setState(hotkey.StateIdle.String())
	}
}

func transcribeAndSend(ctx context.Context, pcm []byte, asrClient *asr.Client, reg *output.Registry, cfg *config.Config, state *daemonState) {
	defer state.setState(hotkey.StateIdle.String())

	audioMs := len(pcm) * 1000 / 2 / cfg.Audio.SampleRate

	rec := persist.Record{
		Timestamp: time.Now().UTC(),
		AudioMs:   audioMs,
		Mode:      "tmux",
	}

	tctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Server.TimeoutMs)*time.Millisecond)
	defer cancel()
	result, err := asrClient.Transcribe(tctx, pcm, cfg.Audio.SampleRate)
	if err != nil {
		slog.Error("transcribe", "err", err)
		rec.Success = false
		rec.Error = err.Error()
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		if cfg.UI.Notifications {
			notify.Send("scriber: transcribe failed", err.Error())
		}
		return
	}
	rec.Transcript = result.Text
	rec.Raw = result.Raw
	rec.InferenceMs = result.Ms

	pane := reg.ActivePane()
	if pane == nil {
		rec.Mode = "noop"
		rec.Error = "no pane attached"
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		if cfg.UI.Notifications {
			notify.Send("scriber: no pane attached", "run `scriber attach` in a tmux pane")
		}
		return
	}
	rec.TargetPane = pane.PaneID
	rec.TargetAlias = pane.Alias

	if !output.TmuxAlive(pane.PaneID) {
		_ = reg.Detach(pane.Alias, "")
		rec.Error = "target pane is dead"
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		if cfg.UI.Notifications {
			notify.Send("scriber: pane gone", pane.Alias+" removed")
		}
		return
	}

	if err := output.TmuxSendKeys(pane.PaneID, result.Text); err != nil {
		slog.Error("tmux send-keys", "err", err)
		rec.Error = err.Error()
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		if cfg.UI.Notifications {
			notify.Send("scriber: send failed", err.Error())
		}
		return
	}

	rec.Success = true
	_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
	state.setLastTranscript(result.Text)
	slog.Info("dictated", "to", pane.Alias, "chars", len(result.Text), "audio_ms", audioMs, "infer_ms", result.Ms)
}

func handleCycle(reg *output.Registry, notifyOn bool) {
	active, err := reg.Cycle()
	if err != nil {
		if notifyOn {
			notify.Send("scriber: cycle failed", err.Error())
		}
		slog.Warn("cycle", "err", err)
		return
	}
	if notifyOn {
		notify.Send("scriber → "+active, "")
	}
	slog.Info("cycled", "active", active)
}

func healthLoop(ctx context.Context, asrClient *asr.Client, state *daemonState) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	check := func() {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		state.setServerOK(asrClient.Healthz(cctx) == nil)
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			check()
		}
	}
}

func pruneLoop(ctx context.Context, reg *output.Registry, notifyOn bool) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, alias := range reg.PruneDead() {
				slog.Info("pruned dead pane", "alias", alias)
				if notifyOn {
					notify.Send("scriber: pane removed", alias+" gone")
				}
			}
		}
	}
}
