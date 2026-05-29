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
	var transcriptsDir string
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Run the STT daemon (hotkey loop + audio + IPC + stream output)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			if transcriptsDir != "" {
				cfg.Storage.TranscriptsDir = config.ExpandPath(transcriptsDir)
			}
			return runDaemon(cmd.Context(), cfg)
		},
	}
	c.Flags().StringVar(&transcriptsDir, "transcripts-dir", "", "directory for transcript JSON and raw WAV files")
	return c
}

type daemonState struct {
	mu                 sync.Mutex
	fsmState           string
	recordingStartedAt time.Time
	audioLevel         float64
	serverOK           bool
	lastTranscript     string
	lastAt             time.Time
}

func (d *daemonState) State() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fsmState
}

func (d *daemonState) RecordingStartedAt() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.recordingStartedAt
}

func (d *daemonState) AudioLevel() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.audioLevel
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

func (d *daemonState) startRecording() {
	d.mu.Lock()
	d.fsmState = "Recording"
	d.recordingStartedAt = time.Now()
	d.audioLevel = 0
	d.mu.Unlock()
}

func (d *daemonState) stopRecording() {
	d.mu.Lock()
	d.recordingStartedAt = time.Time{}
	d.audioLevel = 0
	d.mu.Unlock()
}

func (d *daemonState) setAudioLevel(level float64) {
	d.mu.Lock()
	d.audioLevel = level
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

	lock, err := acquireDaemonLock()
	if err != nil {
		return err
	}
	defer releaseDaemonLock(lock)

	if procs, err := findDaemonProcesses(); err == nil {
		if others := otherDaemonProcesses(procs, os.Getpid()); len(others) > 0 {
			return fmt.Errorf("another stt daemon process is already running: pid(s)=%s", formatPIDs(daemonProcessPIDs(others)))
		}
	} else {
		slog.Warn("could not inspect stt daemon processes", "err", err)
	}

	signalCtx, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	talkCode, err := hotkey.ParseKey(cfg.Hotkey.TalkKey)
	if err != nil {
		return fmt.Errorf("talk_key: %w", err)
	}
	cycleCode, err := hotkey.ParseKey(cfg.Hotkey.CycleKey)
	if err != nil {
		return fmt.Errorf("cycle_key: %w", err)
	}
	cancelCode, err := hotkey.ParseKey(cfg.Hotkey.CancelKey)
	if err != nil {
		return fmt.Errorf("cancel_key: %w", err)
	}

	watched := map[evdev.EvCode]bool{talkCode: true, cycleCode: true, cancelCode: true}
	for code := range slotByKeyCode {
		watched[code] = true
	}
	watched[targetKeyCode] = true
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
	var serviceWG sync.WaitGroup
	var workWG sync.WaitGroup
	goService := func(fn func()) {
		serviceWG.Add(1)
		go func() {
			defer serviceWG.Done()
			fn()
		}()
	}

	requestShutdown := func() {
		state.setState("ShuttingDown")
		cancel()
	}
	ipcSrv := ipc.NewServer(config.SocketPath(), reg, state, requestShutdown, cfg.Storage.TranscriptsDir)
	goService(func() {
		if err := ipcSrv.Serve(ctx); err != nil {
			slog.Error("ipc server", "err", err)
		}
	})

	goService(func() { healthLoop(ctx, asrClient, state) })
	goService(func() { levelLoop(ctx, mic, state) })
	goService(func() { pruneLoop(ctx, reg) })

	talkEvents := make(chan hotkey.Event, 16)
	goService(func() {
		splitEvents(ctx, evChan, talkCode, cycleCode, cancelCode, talkEvents, reg, cfg.UI.Notifications)
	})

	actions := make(chan hotkey.Action, 8)
	goService(func() {
		hotkey.Run(ctx, hotkey.FSMConfig{
			HoldThreshold:   time.Duration(cfg.Hotkey.HoldThresholdMs) * time.Millisecond,
			DoubleTapWindow: time.Duration(cfg.Hotkey.DoubleTapWindowMs) * time.Millisecond,
		}, talkEvents, actions)
	})

	slog.Info("stt daemon ready", "talk", cfg.Hotkey.TalkKey, "cycle", cfg.Hotkey.CycleKey, "cancel", cfg.Hotkey.CancelKey)

	for {
		select {
		case <-ctx.Done():
			slog.Info("daemon stopping")
			_ = mic.Discard()
			waitGroupWithTimeout("transcription work", &workWG, 5*time.Second)
			waitGroupWithTimeout("daemon services", &serviceWG, 2*time.Second)
			return nil
		case action := <-actions:
			handleAction(ctx, action, mic, asrClient, reg, cfg, state, &workWG)
		}
	}
}

func waitGroupWithTimeout(name string, wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("timed out waiting during shutdown", "component", name, "timeout", timeout)
	}
}

var slotByKeyCode = map[evdev.EvCode]int{
	evdev.KEY_1: 1,
	evdev.KEY_2: 2,
	evdev.KEY_3: 3,
	evdev.KEY_4: 4,
	evdev.KEY_5: 5,
	evdev.KEY_6: 6,
	evdev.KEY_7: 7,
	evdev.KEY_8: 8,
	evdev.KEY_9: 9,
}

const targetKeyCode = evdev.KEY_0

var notifySend = notify.Send

func splitEvents(ctx context.Context, in <-chan hotkey.Event, talkCode, cycleCode, cancelCode evdev.EvCode, talkOut chan<- hotkey.Event, reg *output.Registry, notifyOn bool) {
	defer close(talkOut)
	talkDown := false
	cycleDown := false
	suppressTalkUp := false
	chordHandled := false
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
				if ev.Kind == hotkey.KeyDown {
					if talkDown {
						continue
					}
					talkDown = true
					suppressTalkUp = false
					chordHandled = false
					select {
					case talkOut <- ev:
					case <-ctx.Done():
						return
					}
					continue
				}
				if ev.Kind == hotkey.KeyUp {
					if !talkDown {
						continue
					}
					talkDown = false
					chordHandled = false
					if suppressTalkUp {
						suppressTalkUp = false
						continue
					}
					select {
					case talkOut <- ev:
					case <-ctx.Done():
						return
					}
				}
			case cycleCode:
				if ev.Kind == hotkey.KeyDown {
					if cycleDown {
						continue
					}
					cycleDown = true
					handleCycle(reg)
				}
				if ev.Kind == hotkey.KeyUp {
					cycleDown = false
				}
			case targetKeyCode:
				if ev.Kind == hotkey.KeyDown && talkDown {
					if chordHandled {
						continue
					}
					chordHandled = true
					select {
					case talkOut <- hotkey.Event{Kind: hotkey.Cancel, Code: talkCode, At: ev.At}:
					case <-ctx.Done():
						return
					}
					suppressTalkUp = true
					handleTargetNotification(reg, notifyOn)
				}
			case cancelCode:
				if ev.Kind == hotkey.KeyDown {
					if talkDown && chordHandled {
						continue
					}
					if talkDown {
						chordHandled = true
					}
					select {
					case talkOut <- hotkey.Event{Kind: hotkey.Cancel, Code: cancelCode, At: ev.At}:
					case <-ctx.Done():
						return
					}
					if talkDown {
						suppressTalkUp = true
					}
				}
			default:
				slot, ok := slotByKeyCode[ev.Code]
				if ok && ev.Kind == hotkey.KeyDown && talkDown {
					if chordHandled {
						continue
					}
					chordHandled = true
					select {
					case talkOut <- hotkey.Event{Kind: hotkey.Cancel, Code: talkCode, At: ev.At}:
					case <-ctx.Done():
						return
					}
					suppressTalkUp = true
					handleSelectSlot(reg, slot, notifyOn)
				}
			}
		}
	}
}

func handleAction(ctx context.Context, action hotkey.Action, mic *audio.Capture, asrClient *asr.Client, reg *output.Registry, cfg *config.Config, state *daemonState, workWG *sync.WaitGroup) {
	switch action {
	case hotkey.ActionStartCapture:
		if err := mic.Start(); err != nil {
			slog.Error("audio start", "err", err)
			state.stopRecording()
			state.setState(hotkey.StateIdle.String())
		} else {
			state.startRecording()
		}
	case hotkey.ActionStopAndSend:
		state.stopRecording()
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
		workWG.Add(1)
		go func() {
			defer workWG.Done()
			transcribeAndSend(ctx, pcm, asrClient, reg, cfg, state)
		}()
	case hotkey.ActionDiscardCapture:
		_ = mic.Discard()
		state.stopRecording()
		state.setState(hotkey.StateIdle.String())
	}
}

func transcribeAndSend(ctx context.Context, pcm []byte, asrClient *asr.Client, reg *output.Registry, cfg *config.Config, state *daemonState) {
	defer state.setState(hotkey.StateIdle.String())

	audioMs := len(pcm) * 1000 / 2 / cfg.Audio.SampleRate

	now := time.Now().UTC()
	rec := persist.Record{
		Timestamp: now,
		AudioMs:   audioMs,
		Mode:      ipc.TargetTypePTY,
	}

	if audioPath, err := persist.SavePCM16WAV(cfg.Storage.TranscriptsDir, now, pcm, cfg.Audio.SampleRate); err != nil {
		slog.Error("save raw audio", "err", err)
		rec.AudioSaveError = err.Error()
	} else {
		rec.AudioPath = audioPath
	}

	tctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Server.TimeoutMs)*time.Millisecond)
	defer cancel()
	result, err := asrClient.Transcribe(tctx, pcm, cfg.Audio.SampleRate)
	if err != nil {
		slog.Error("transcribe", "err", err)
		rec.Success = false
		rec.Error = err.Error()
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		return
	}
	rec.Transcript = result.Text
	rec.Raw = result.Raw
	rec.InferenceMs = result.Ms

	stream := reg.ActiveStream()
	if stream == nil {
		rec.Mode = "noop"
		rec.Error = "no stream selected"
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		return
	}
	target := stream.Target
	rec.TargetStream = stream.Name
	rec.TargetType = target.TargetType
	rec.TargetRef = target.TargetRef

	if err := output.SendText(target, result.Text); err != nil {
		slog.Error("send text", "err", err)
		_ = reg.PruneDead()
		rec.Error = err.Error()
		_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
		return
	}

	rec.Success = true
	_ = persist.Save(cfg.Storage.TranscriptsDir, rec)
	state.setLastTranscript(result.Text)
	slog.Info("dictated", "stream", stream.Name, "target", target.TargetRef, "chars", len(result.Text), "audio_ms", audioMs, "infer_ms", result.Ms)
}

func handleCycle(reg *output.Registry) {
	active, err := reg.Cycle()
	if err != nil {
		slog.Warn("cycle", "err", err)
		return
	}
	slog.Info("cycled", "active", active)
}

func handleSelectSlot(reg *output.Registry, slot int, notifyOn bool) {
	active, err := reg.SelectSlot(slot)
	if err != nil {
		if notifyOn {
			notifySend(fmt.Sprintf("stt: slot %d failed", slot), err.Error())
		}
		slog.Warn("select slot", "slot", slot, "err", err)
		return
	}
	if notifyOn {
		notifySend(fmt.Sprintf("stt slot %d → %s", slot, active), "")
	}
	slog.Info("selected slot", "slot", slot, "active", active)
}

func handleTargetNotification(reg *output.Registry, notifyOn bool) {
	title, body := currentTargetNotification(reg)
	if notifyOn {
		notifySend(title, body)
	}
	slog.Info("reported target", "body", body)
}

func currentTargetNotification(reg *output.Registry) (string, string) {
	streams, active, activeSlot := reg.Streams()
	return formatTargetNotification(streams, active, activeSlot)
}

func formatTargetNotification(streams []ipc.Stream, active string, activeSlot int) (string, string) {
	slot := "-"
	name := "(none)"
	if active != "" || activeSlot > 0 {
		name = active
		if name == "" {
			name = "-"
		}
		for _, stream := range streams {
			if activeSlot > 0 && stream.Slot != activeSlot {
				continue
			}
			if activeSlot == 0 && stream.Name != active {
				continue
			}
			if stream.Slot > 0 {
				slot = fmt.Sprintf("%d", stream.Slot)
			}
			break
		}
	}
	return "stt target", fmt.Sprintf("slot=%s name=%s", slot, name)
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

func levelLoop(ctx context.Context, mic *audio.Capture, state *daemonState) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if state.RecordingStartedAt().IsZero() {
				state.setAudioLevel(0)
				continue
			}
			state.setAudioLevel(mic.Level())
		}
	}
}

func pruneLoop(ctx context.Context, reg *output.Registry) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, streamName := range reg.PruneDead() {
				slog.Info("marked dead stream target", "stream", streamName)
			}
		}
	}
}
