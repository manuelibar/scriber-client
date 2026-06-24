package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
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
	jobs               map[string]ipc.JobSnapshot
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

func (d *daemonState) Jobs() []ipc.JobSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.jobs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	out := make([]ipc.JobSnapshot, 0, len(d.jobs))
	for _, job := range d.jobs {
		if !job.CreatedAt.IsZero() {
			job.AgeMs = int(now.Sub(job.CreatedAt) / time.Millisecond)
		}
		if !job.UpdatedAt.IsZero() {
			job.UpdatedAgoMs = int(now.Sub(job.UpdatedAt) / time.Millisecond)
		}
		out = append(out, job)
	}
	return out
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

func (d *daemonState) updateJob(meta persist.CaptureMeta) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.jobs == nil {
		d.jobs = map[string]ipc.JobSnapshot{}
	}
	now := time.Now().UTC()
	age := 0
	if !meta.CreatedAt.IsZero() {
		age = int(now.Sub(meta.CreatedAt) / time.Millisecond)
	}
	updatedAgo := 0
	if !meta.UpdatedAt.IsZero() {
		updatedAgo = int(now.Sub(meta.UpdatedAt) / time.Millisecond)
	}
	d.jobs[meta.CaptureID] = ipc.JobSnapshot{
		CaptureID:    meta.CaptureID,
		Stage:        meta.Stage,
		AgeMs:        age,
		UpdatedAgoMs: updatedAgo,
		CreatedAt:    meta.CreatedAt,
		UpdatedAt:    meta.UpdatedAt,
		AudioPath:    meta.AudioPath,
		TargetStream: meta.TargetStream,
		TargetType:   meta.TargetType,
		TargetRef:    meta.TargetRef,
		Language:     meta.Language,
		Error:        meta.Error,
		Retryable:    meta.Retryable,
	}
	d.fsmState = meta.Stage
}

func (d *daemonState) removeJob(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.jobs, id)
	if len(d.jobs) == 0 && d.recordingStartedAt.IsZero() {
		d.fsmState = hotkey.StateIdle.String()
	}
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
	queryCode, err := hotkey.ParseKey(cfg.Hotkey.QueryKey)
	if err != nil {
		return fmt.Errorf("query_key: %w", err)
	}

	watched := map[evdev.EvCode]bool{talkCode: true, cycleCode: true, cancelCode: true, queryCode: true}
	for code := range slotByKeyCode {
		watched[code] = true
	}
	evChan, err := hotkey.Listen(ctx, watched)
	if err != nil {
		return fmt.Errorf("hotkey listen: %w", err)
	}

	mic, err := audio.New(cfg.Audio.SampleRate)
	if err != nil {
		return fmt.Errorf("audio init: %w", err)
	}
	defer closeCaptureWithTimeout(mic, time.Second)

	asrClient := asr.New(cfg.Server.URL, 0)
	captureStore := persist.NewCaptureStore(cfg.Storage.TranscriptsDir)

	reg, err := output.NewRegistry(cfg.Storage.RegistryPath)
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}

	state := &daemonState{fsmState: hotkey.StateIdle.String(), jobs: map[string]ipc.JobSnapshot{}}
	var serviceWG sync.WaitGroup
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

	notifier := notify.NewWorker(ctx, cfg.UI.Notifications, 8)
	commands := make(chan hotkey.Command, 32)
	goService(func() {
		hotkey.RunRecognizer(ctx, hotkey.FSMConfig{
			HoldThreshold:   time.Duration(cfg.Hotkey.HoldThresholdMs) * time.Millisecond,
			DoubleTapWindow: time.Duration(cfg.Hotkey.DoubleTapWindowMs) * time.Millisecond,
			TalkKey:         talkCode,
			CancelKey:       cancelCode,
			QueryKey:        queryCode,
			CycleKey:        cycleCode,
			SlotKeys:        slotByKeyCode,
		}, evChan, commands)
	})

	recorderCommands := make(chan recorderCommand, 16)
	finalizedCaptures := make(chan persist.CaptureMeta, 16)
	asrJobs := make(chan string, 16)
	deliveryJobs := make(chan string, 16)
	goService(func() { recorderLoop(ctx, mic, captureStore, cfg, state, recorderCommands, finalizedCaptures) })
	goService(func() { finalizerLoop(ctx, captureStore, state, finalizedCaptures, asrJobs) })
	goService(func() { asrWorkerLoop(ctx, captureStore, asrClient, cfg, state, asrJobs, deliveryJobs) })
	goService(func() { deliveryWorkerLoop(ctx, captureStore, reg, cfg, state, deliveryJobs) })

	if err := recoverDurableWorkflow(ctx, captureStore, state, asrJobs, deliveryJobs); err != nil {
		slog.Warn("capture recovery", "err", err)
	}

	slog.Info("stt daemon ready", "talk", cfg.Hotkey.TalkKey, "cycle", cfg.Hotkey.CycleKey, "cancel", cfg.Hotkey.CancelKey, "query", cfg.Hotkey.QueryKey)

	lockedCaptureActive := false
	for {
		select {
		case <-ctx.Done():
			slog.Info("daemon stopping")
			waitGroupWithTimeout("daemon services", &serviceWG, 2*time.Second)
			return nil
		case cmd := <-commands:
			routeCommand(ctx, cmd, recorderCommands, reg, notifier, state, &lockedCaptureActive)
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

var slotByKeyCode = hotkey.DefaultSlotKeys()

type recorderCommand struct {
	action  hotkey.Action
	options persist.CaptureOptions
}

func routeCommand(ctx context.Context, cmd hotkey.Command, recorderCommands chan<- recorderCommand, reg *output.Registry, notifier *notify.Worker, state *daemonState, lockedCaptureActive *bool) {
	switch cmd.Action {
	case hotkey.ActionStartMomentaryCapture:
		*lockedCaptureActive = false
		options, ok := captureOptionsForActiveStream(reg)
		if ok {
			sendRecorderCommand(ctx, recorderCommands, hotkey.ActionStartMomentaryCapture, options)
		}
	case hotkey.ActionToggleLockedCapture:
		if *lockedCaptureActive {
			*lockedCaptureActive = false
			sendRecorderCommand(ctx, recorderCommands, hotkey.ActionFinalizeCapture, persist.CaptureOptions{})
		} else {
			options, ok := captureOptionsForActiveStream(reg)
			if ok {
				*lockedCaptureActive = true
				sendRecorderCommand(ctx, recorderCommands, hotkey.ActionStartMomentaryCapture, options)
			}
		}
	case hotkey.ActionFinalizeCapture:
		*lockedCaptureActive = false
		sendRecorderCommand(ctx, recorderCommands, hotkey.ActionFinalizeCapture, persist.CaptureOptions{})
	case hotkey.ActionDiscardCapture:
		*lockedCaptureActive = false
		sendRecorderCommand(ctx, recorderCommands, hotkey.ActionDiscardCapture, persist.CaptureOptions{})
	case hotkey.ActionSelectSlot:
		handleSelectSlot(reg, cmd.Slot, notifier)
	case hotkey.ActionReportActiveStream:
		handleTargetNotification(reg, notifier)
	case hotkey.ActionCycleStream:
		handleCycle(reg)
	}
	if state.State() == "" {
		state.setState(hotkey.StateIdle.String())
	}
}

func captureOptionsForActiveStream(reg *output.Registry) (persist.CaptureOptions, bool) {
	stream := reg.ActiveStream()
	options := persist.CaptureOptions{Kind: persist.CaptureKindDictation}
	if stream != nil {
		options.TargetStream = daemonStreamLabel(*stream)
		options.TargetType = stream.Target.TargetType
		options.TargetRef = stream.Target.TargetRef
		options.Language = stream.Language
	}
	return options, true
}

func sendRecorderCommand(ctx context.Context, ch chan<- recorderCommand, action hotkey.Action, options persist.CaptureOptions) {
	select {
	case ch <- recorderCommand{action: action, options: options}:
	case <-ctx.Done():
	default:
		slog.Warn("recorder command queue full", "action", action)
	}
}

func recorderLoop(ctx context.Context, mic *audio.Capture, store *persist.CaptureStore, cfg *config.Config, state *daemonState, commands <-chan recorderCommand, finalized chan<- persist.CaptureMeta) {
	var current *persist.CaptureWriter
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-commands:
			switch cmd.action {
			case hotkey.ActionStartMomentaryCapture:
				if current != nil {
					slog.Warn("capture already active", "capture_id", current.CaptureID())
					continue
				}
				writer, err := store.NewCaptureWithOptions(cfg.Audio.SampleRate, cmd.options)
				if err != nil {
					slog.Error("capture spool start", "err", err)
					state.setState(hotkey.StateIdle.String())
					continue
				}
				if err := mic.StartWithChunks(func(chunk []byte) {
					if err := writer.WriteChunk(chunk); err != nil {
						slog.Error("capture spool write", "capture_id", writer.CaptureID(), "err", err)
					}
				}); err != nil {
					slog.Error("audio start", "err", err)
					meta, failErr := writer.Fail(persist.StageRecording, err, false)
					if failErr == nil {
						state.updateJob(meta)
					}
					state.stopRecording()
					state.setState(hotkey.StateIdle.String())
					continue
				}
				current = writer
				state.startRecording()
				state.updateJob(writer.Meta())
			case hotkey.ActionFinalizeCapture:
				if current == nil {
					continue
				}
				if meta, err := current.MarkStopping(); err == nil {
					state.stopRecording()
					state.updateJob(meta)
				}
				pcm, err := mic.Stop()
				if err != nil {
					slog.Error("audio stop", "err", err)
					meta, failErr := current.Fail(persist.StageStoppingCapture, err, true)
					if failErr == nil {
						state.updateJob(meta)
						saveCaptureRecord(cfg.Storage.TranscriptsDir, meta, false, err)
					}
					current = nil
					continue
				}
				if len(pcm) == 0 {
					err := fmt.Errorf("empty audio buffer")
					slog.Warn("empty audio buffer")
					meta, failErr := current.Fail(persist.StageSavingAudio, err, false)
					if failErr == nil {
						state.updateJob(meta)
						saveCaptureRecord(cfg.Storage.TranscriptsDir, meta, false, err)
					}
					current = nil
					continue
				}
				if meta, err := store.Update(current.CaptureID(), func(meta *persist.CaptureMeta) {
					meta.Stage = persist.StageSavingAudio
				}); err == nil {
					state.updateJob(meta)
				}
				meta, err := current.FinalizeWithPCM(pcm)
				if err != nil {
					slog.Error("finalize audio", "err", err)
					meta, failErr := current.Fail(persist.StageSavingAudio, err, true)
					if failErr == nil {
						state.updateJob(meta)
						saveCaptureRecord(cfg.Storage.TranscriptsDir, meta, false, err)
					}
					current = nil
					continue
				}
				state.updateJob(meta)
				current = nil
				select {
				case finalized <- meta:
				case <-ctx.Done():
					return
				}
			case hotkey.ActionDiscardCapture:
				if current == nil {
					state.stopRecording()
					state.setState(hotkey.StateIdle.String())
					continue
				}
				err := fmt.Errorf("discarded by user")
				_ = mic.Discard()
				meta, failErr := current.Fail(persist.StageRecording, err, false)
				if failErr == nil {
					state.updateJob(meta)
				}
				current = nil
				state.stopRecording()
				state.setState(hotkey.StateIdle.String())
			}
		}
	}
}

func finalizerLoop(ctx context.Context, store *persist.CaptureStore, state *daemonState, finalized <-chan persist.CaptureMeta, asrJobs chan<- string) {
	for {
		select {
		case <-ctx.Done():
			return
		case meta := <-finalized:
			queued, err := store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
				next.Stage = persist.StageQueuedForASR
			})
			if err != nil {
				slog.Error("queue asr", "capture_id", meta.CaptureID, "err", err)
				continue
			}
			state.updateJob(queued)
			select {
			case asrJobs <- queued.CaptureID:
			case <-ctx.Done():
				return
			}
		}
	}
}

func asrWorkerLoop(ctx context.Context, store *persist.CaptureStore, asrClient *asr.Client, cfg *config.Config, state *daemonState, jobs <-chan string, deliveryJobs chan<- string) {
	for {
		select {
		case <-ctx.Done():
			return
		case captureID := <-jobs:
			meta, err := store.Update(captureID, func(next *persist.CaptureMeta) {
				next.Stage = persist.StageTranscribing
				next.Error = ""
				next.FailedStage = ""
				next.Retryable = false
			})
			if err != nil {
				slog.Error("start asr job", "capture_id", captureID, "err", err)
				continue
			}
			state.updateJob(meta)
			pcm, err := os.ReadFile(meta.PCMPath)
			if err != nil {
				failCapture(store, state, cfg.Storage.TranscriptsDir, meta.CaptureID, persist.StageTranscribing, err, true)
				continue
			}
			tctx, cancel := context.WithTimeout(ctx, transcriptionTimeout(cfg, meta.AudioMs))
			result, err := asrClient.Transcribe(tctx, pcm, meta.SampleRate, meta.Language)
			cancel()
			if err != nil {
				slog.Error("transcribe", "capture_id", meta.CaptureID, "err", err)
				failCapture(store, state, cfg.Storage.TranscriptsDir, meta.CaptureID, persist.StageTranscribing, err, true)
				continue
			}
			transcribed, err := store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
				next.Stage = persist.StageTranscribed
				next.Transcript = result.Text
				next.Raw = result.Raw
				next.InferenceMs = result.Ms
				if result.Language != "" {
					next.Language = result.Language
				}
			})
			if err != nil {
				slog.Error("save transcription", "capture_id", meta.CaptureID, "err", err)
				continue
			}
			state.updateJob(transcribed)
			select {
			case deliveryJobs <- transcribed.CaptureID:
			case <-ctx.Done():
				return
			}
		}
	}
}

func transcriptionTimeout(cfg *config.Config, audioMs int) time.Duration {
	timeout := time.Duration(cfg.Server.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(config.MinServerTimeoutMs) * time.Millisecond
	}
	if audioMs <= 0 {
		return timeout
	}
	audioBased := time.Duration(audioMs)*time.Millisecond/4 + 30*time.Second
	if timeout < audioBased {
		return audioBased
	}
	return timeout
}

func deliveryWorkerLoop(ctx context.Context, store *persist.CaptureStore, reg *output.Registry, cfg *config.Config, state *daemonState, jobs <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case captureID := <-jobs:
			meta, err := store.Read(captureID)
			if err != nil {
				slog.Error("read delivery job", "capture_id", captureID, "err", err)
				continue
			}
			if meta.TargetRef == "" {
				stream := reg.ActiveStream()
				if stream == nil {
					err := fmt.Errorf("no stream selected")
					failCapture(store, state, cfg.Storage.TranscriptsDir, meta.CaptureID, persist.StageQueuedForDelivery, err, true)
					continue
				}
				target := stream.Target
				meta, err = store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
					next.Stage = persist.StageQueuedForDelivery
					next.TargetStream = daemonStreamLabel(*stream)
					next.TargetType = target.TargetType
					next.TargetRef = target.TargetRef
					next.Language = stream.Language
				})
				if err != nil {
					slog.Error("queue delivery", "capture_id", captureID, "err", err)
					continue
				}
				state.updateJob(meta)
			}
			meta, err = store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
				next.Stage = persist.StageDelivering
			})
			if err != nil {
				slog.Error("start delivery", "capture_id", captureID, "err", err)
				continue
			}
			state.updateJob(meta)

			text := strings.TrimSpace(meta.Transcript)
			if text == "" {
				err := fmt.Errorf("transcript text is empty")
				failCapture(store, state, cfg.Storage.TranscriptsDir, meta.CaptureID, persist.StageDelivering, err, false)
				continue
			}
			if _, err := reg.SendTextToStream(meta.TargetStream, text+" "); err != nil {
				slog.Warn("deliver transcript", "capture_id", meta.CaptureID, "stream", meta.TargetStream, "err", err)
				failCapture(store, state, cfg.Storage.TranscriptsDir, meta.CaptureID, persist.StageDelivering, err, true)
				continue
			}
			delivered, err := store.Update(meta.CaptureID, func(next *persist.CaptureMeta) {
				next.Stage = persist.StageDelivered
				next.Error = ""
				next.FailedStage = ""
				next.Retryable = false
			})
			if err != nil {
				slog.Error("mark delivered", "capture_id", meta.CaptureID, "err", err)
				continue
			}
			state.updateJob(delivered)
			saveCaptureRecord(cfg.Storage.TranscriptsDir, delivered, true, nil)
			state.setLastTranscript(delivered.Transcript)
			state.removeJob(delivered.CaptureID)
			slog.Info("delivered transcript", "capture_id", delivered.CaptureID, "stream", delivered.TargetStream, "target", delivered.TargetRef, "chars", len(delivered.Transcript), "audio_ms", delivered.AudioMs, "infer_ms", delivered.InferenceMs)
		}
	}
}

func recoverDurableWorkflow(ctx context.Context, store *persist.CaptureStore, state *daemonState, asrJobs chan<- string, deliveryJobs chan<- string) error {
	plan, err := store.Recover(2)
	if err != nil {
		return err
	}
	for _, meta := range plan.Failed {
		state.updateJob(meta)
	}
	for _, meta := range plan.ASR {
		state.updateJob(meta)
		select {
		case asrJobs <- meta.CaptureID:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, meta := range plan.Delivery {
		state.updateJob(meta)
		select {
		case deliveryJobs <- meta.CaptureID:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if len(plan.ASR)+len(plan.Delivery)+len(plan.Failed) > 0 {
		slog.Info("recovered durable captures", "asr", len(plan.ASR), "delivery", len(plan.Delivery), "failed", len(plan.Failed))
	}
	return nil
}

func failCapture(store *persist.CaptureStore, state *daemonState, transcriptsDir, captureID, stage string, err error, retryable bool) {
	meta, failErr := store.Fail(captureID, stage, err, retryable)
	if failErr != nil {
		slog.Error("mark capture failed", "capture_id", captureID, "err", failErr)
		return
	}
	state.updateJob(meta)
	saveCaptureRecord(transcriptsDir, meta, false, err)
}

func saveCaptureRecord(dir string, meta persist.CaptureMeta, success bool, failure error) {
	if strings.TrimSpace(dir) == "" || meta.CaptureID == "" {
		return
	}
	rec := captureRecord(dir, meta, success, failure)
	if err := persist.Save(dir, rec); err != nil {
		slog.Error("save transcript history", "capture_id", meta.CaptureID, "err", err)
	}
}

func captureRecord(dir string, meta persist.CaptureMeta, success bool, failure error) persist.Record {
	errText := meta.Error
	if failure != nil {
		errText = failure.Error()
	}
	mode := ipc.TargetTypePTY
	if meta.TargetRef == "" {
		mode = "noop"
	}
	rec := persist.Record{
		Timestamp:    meta.CreatedAt,
		MessageID:    persist.RecordMessageID(persist.Record{CaptureID: meta.CaptureID}),
		CaptureID:    meta.CaptureID,
		Stage:        meta.Stage,
		AudioMs:      meta.AudioMs,
		AudioPath:    meta.AudioPath,
		Transcript:   meta.Transcript,
		Raw:          meta.Raw,
		TargetStream: meta.TargetStream,
		TargetType:   meta.TargetType,
		TargetRef:    meta.TargetRef,
		Language:     meta.Language,
		Mode:         mode,
		Success:      success,
		Error:        errText,
		InferenceMs:  meta.InferenceMs,
		Type:         "transcript",
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	return rec
}

func closeCaptureWithTimeout(mic *audio.Capture, timeout time.Duration) {
	if mic == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		mic.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("timed out closing audio capture", "timeout", timeout)
	}
}

func handleCycle(reg *output.Registry) {
	active, err := reg.Cycle()
	if err != nil {
		slog.Warn("cycle", "err", err)
		return
	}
	slog.Info("cycled", "active", active)
}

func daemonStreamLabel(stream ipc.Stream) string {
	if strings.TrimSpace(stream.Name) != "" {
		return strings.TrimSpace(stream.Name)
	}
	if stream.Slot > 0 {
		return fmt.Sprintf("slot %d", stream.Slot)
	}
	if stream.ID != "" {
		return stream.ID
	}
	return "(unnamed)"
}

func handleSelectSlot(reg *output.Registry, slot int, notifier *notify.Worker) {
	active, err := reg.SelectSlot(slot)
	if err != nil {
		if notifier != nil {
			notifier.Send(fmt.Sprintf("stt: slot %d failed", slot), err.Error())
		}
		slog.Warn("select slot", "slot", slot, "err", err)
		return
	}
	if notifier != nil {
		notifier.Send(fmt.Sprintf("stt slot %d -> %s", slot, active), "")
	}
	slog.Info("selected slot", "slot", slot, "active", active)
}

func handleTargetNotification(reg *output.Registry, notifier *notify.Worker) {
	title, body := currentTargetNotification(reg)
	if notifier != nil {
		notifier.Send(title, body)
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
