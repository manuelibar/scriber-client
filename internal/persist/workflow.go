package persist

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	CaptureKindDictation = "dictation"
)

const (
	StageRecording         = "Recording"
	StageStoppingCapture   = "StoppingCapture"
	StageSavingAudio       = "SavingAudio"
	StageAudioFinalized    = "AudioFinalized"
	StageQueuedForASR      = "QueuedForASR"
	StageTranscribing      = "Transcribing"
	StageTranscribed       = "Transcribed"
	StageQueuedForDelivery = "QueuedForDelivery"
	StageDelivering        = "Delivering"
	StageDelivered         = "Delivered"
	StageFailed            = "Failed"
)

const (
	CaptureInflightPCM = "audio.inflight.pcm"
	CaptureFinalPCM    = "audio.final.pcm"
	CaptureWAV         = "audio.wav"
	CaptureMetaFile    = "capture.json"
)

type CaptureMeta struct {
	CaptureID       string    `json:"capture_id"`
	Kind            string    `json:"kind,omitempty"`
	Stage           string    `json:"stage"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	SampleRate      int       `json:"sample_rate"`
	InflightPCMPath string    `json:"inflight_pcm_path,omitempty"`
	PCMPath         string    `json:"pcm_path,omitempty"`
	AudioPath       string    `json:"audio_path,omitempty"`
	PCMBytes        int64     `json:"pcm_bytes,omitempty"`
	AudioMs         int       `json:"audio_ms,omitempty"`
	Transcript      string    `json:"transcript,omitempty"`
	Raw             string    `json:"raw,omitempty"`
	InferenceMs     int       `json:"inference_ms,omitempty"`
	TargetStream    string    `json:"target_stream,omitempty"`
	TargetType      string    `json:"target_type,omitempty"`
	TargetRef       string    `json:"target_ref,omitempty"`
	Language        string    `json:"language,omitempty"`
	FailedStage     string    `json:"failed_stage,omitempty"`
	Error           string    `json:"error,omitempty"`
	Retryable       bool      `json:"retryable,omitempty"`
}

type CaptureStore struct {
	Root string
}

type CaptureWriter struct {
	store *CaptureStore
	meta  CaptureMeta

	mu     sync.Mutex
	file   *os.File
	closed bool
}

type RecoveryPlan struct {
	ASR      []CaptureMeta
	Delivery []CaptureMeta
	Failed   []CaptureMeta
}

type CaptureOptions struct {
	Kind         string
	TargetStream string
	TargetType   string
	TargetRef    string
	Language     string
}

func NewCaptureStore(transcriptsDir string) *CaptureStore {
	return &CaptureStore{Root: filepath.Join(transcriptsDir, "captures")}
}

func (s *CaptureStore) NewCapture(sampleRate int) (*CaptureWriter, error) {
	return s.NewCaptureWithOptions(sampleRate, CaptureOptions{})
}

func (s *CaptureStore) NewCaptureWithOptions(sampleRate int, opts CaptureOptions) (*CaptureWriter, error) {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return nil, fmt.Errorf("capture store root is empty")
	}
	now := time.Now().UTC()
	id, err := newCaptureID(now)
	if err != nil {
		return nil, err
	}
	dir := s.captureDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	inflight := filepath.Join(dir, CaptureInflightPCM)
	f, err := os.OpenFile(inflight, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", inflight, err)
	}
	meta := CaptureMeta{
		CaptureID:       id,
		Stage:           StageRecording,
		CreatedAt:       now,
		UpdatedAt:       now,
		SampleRate:      sampleRate,
		InflightPCMPath: inflight,
		Kind:            normalizeCaptureKind(opts.Kind),
		TargetStream:    strings.TrimSpace(opts.TargetStream),
		TargetType:      strings.TrimSpace(opts.TargetType),
		TargetRef:       strings.TrimSpace(opts.TargetRef),
		Language:        strings.TrimSpace(opts.Language),
	}
	if err := s.writeMeta(meta); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &CaptureWriter{store: s, meta: meta, file: f}, nil
}

func normalizeCaptureKind(kind string) string {
	return CaptureKindDictation
}

func (w *CaptureWriter) CaptureID() string {
	if w == nil {
		return ""
	}
	return w.meta.CaptureID
}

func (w *CaptureWriter) Meta() CaptureMeta {
	if w == nil {
		return CaptureMeta{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.meta
}

func (w *CaptureWriter) WriteChunk(chunk []byte) error {
	if w == nil || len(chunk) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.file == nil {
		return nil
	}
	n, err := w.file.Write(chunk)
	w.meta.PCMBytes += int64(n)
	if err != nil {
		return err
	}
	if n != len(chunk) {
		return fmt.Errorf("short audio spool write: %d of %d bytes", n, len(chunk))
	}
	return nil
}

func (w *CaptureWriter) MarkStopping() (CaptureMeta, error) {
	if w == nil {
		return CaptureMeta{}, fmt.Errorf("nil capture writer")
	}
	return w.store.Update(w.meta.CaptureID, func(meta *CaptureMeta) {
		meta.Stage = StageStoppingCapture
	})
}

func (w *CaptureWriter) FinalizeWithPCM(pcm []byte) (CaptureMeta, error) {
	if w == nil {
		return CaptureMeta{}, fmt.Errorf("nil capture writer")
	}
	w.mu.Lock()
	if !w.closed && w.file != nil {
		if err := w.file.Close(); err != nil {
			w.mu.Unlock()
			return CaptureMeta{}, err
		}
	}
	w.closed = true
	w.file = nil
	w.mu.Unlock()

	id := w.meta.CaptureID
	dir := w.store.captureDir(id)
	finalPCM := filepath.Join(dir, CaptureFinalPCM)
	wavPath := filepath.Join(dir, CaptureWAV)
	if len(pcm) > 0 {
		if err := os.WriteFile(finalPCM+".tmp", pcm, 0o600); err != nil {
			return CaptureMeta{}, fmt.Errorf("write %s.tmp: %w", finalPCM, err)
		}
		if err := os.Rename(finalPCM+".tmp", finalPCM); err != nil {
			return CaptureMeta{}, err
		}
		_ = os.Remove(filepath.Join(dir, CaptureInflightPCM))
	} else if err := finalizeInflightPCM(filepath.Join(dir, CaptureInflightPCM), finalPCM); err != nil {
		return CaptureMeta{}, err
	}
	final, err := os.ReadFile(finalPCM)
	if err != nil {
		return CaptureMeta{}, fmt.Errorf("read finalized pcm: %w", err)
	}
	if err := WritePCM16WAV(wavPath, final, w.meta.SampleRate); err != nil {
		return CaptureMeta{}, fmt.Errorf("write finalized wav: %w", err)
	}
	return w.store.Update(id, func(meta *CaptureMeta) {
		meta.Stage = StageAudioFinalized
		meta.PCMPath = finalPCM
		meta.AudioPath = wavPath
		meta.PCMBytes = int64(len(final))
		meta.AudioMs = AudioDurationMs(len(final), meta.SampleRate)
	})
}

func (w *CaptureWriter) Fail(stage string, err error, retryable bool) (CaptureMeta, error) {
	if w == nil {
		return CaptureMeta{}, fmt.Errorf("nil capture writer")
	}
	w.mu.Lock()
	if !w.closed && w.file != nil {
		_ = w.file.Close()
	}
	w.closed = true
	w.file = nil
	w.mu.Unlock()
	return w.store.Fail(w.meta.CaptureID, stage, err, retryable)
}

func (s *CaptureStore) Read(id string) (CaptureMeta, error) {
	if strings.TrimSpace(id) == "" {
		return CaptureMeta{}, fmt.Errorf("capture id is empty")
	}
	path := filepath.Join(s.captureDir(id), CaptureMetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return CaptureMeta{}, fmt.Errorf("read %s: %w", path, err)
	}
	var meta CaptureMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return CaptureMeta{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return meta, nil
}

func (s *CaptureStore) Update(id string, mutate func(*CaptureMeta)) (CaptureMeta, error) {
	meta, err := s.Read(id)
	if err != nil {
		return CaptureMeta{}, err
	}
	if mutate != nil {
		mutate(&meta)
	}
	meta.UpdatedAt = time.Now().UTC()
	if err := s.writeMeta(meta); err != nil {
		return CaptureMeta{}, err
	}
	return meta, nil
}

func (s *CaptureStore) Fail(id, stage string, failure error, retryable bool) (CaptureMeta, error) {
	if stage == "" {
		stage = StageFailed
	}
	errText := ""
	if failure != nil {
		errText = failure.Error()
	}
	return s.Update(id, func(meta *CaptureMeta) {
		meta.FailedStage = stage
		meta.Stage = StageFailed
		meta.Error = errText
		meta.Retryable = retryable
	})
}

func (s *CaptureStore) List() ([]CaptureMeta, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var metas []CaptureMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := s.Read(entry.Name())
		if err != nil {
			return nil, err
		}
		metas = append(metas, meta)
	}
	sort.SliceStable(metas, func(i, j int) bool {
		return metas[i].CreatedAt.Before(metas[j].CreatedAt)
	})
	return metas, nil
}

func (s *CaptureStore) Recover(minPCMBytes int64) (RecoveryPlan, error) {
	metas, err := s.List()
	if err != nil {
		return RecoveryPlan{}, err
	}
	var plan RecoveryPlan
	for _, meta := range metas {
		switch meta.Stage {
		case StageRecording, StageStoppingCapture:
			recovered, err := s.recoverInflight(meta, minPCMBytes)
			if err != nil {
				return RecoveryPlan{}, err
			}
			if recovered.Stage == StageFailed {
				plan.Failed = append(plan.Failed, recovered)
			} else {
				plan.ASR = append(plan.ASR, recovered)
			}
		case StageSavingAudio:
			if meta.PCMPath == "" {
				recovered, err := s.recoverInflight(meta, minPCMBytes)
				if err != nil {
					return RecoveryPlan{}, err
				}
				if recovered.Stage == StageFailed {
					plan.Failed = append(plan.Failed, recovered)
				} else {
					plan.ASR = append(plan.ASR, recovered)
				}
				continue
			}
			queued, err := s.Update(meta.CaptureID, func(next *CaptureMeta) {
				next.Stage = StageQueuedForASR
				next.Error = ""
				next.FailedStage = ""
				next.Retryable = false
			})
			if err != nil {
				return RecoveryPlan{}, err
			}
			plan.ASR = append(plan.ASR, queued)
		case StageAudioFinalized, StageQueuedForASR, StageTranscribing:
			queued, err := s.Update(meta.CaptureID, func(next *CaptureMeta) {
				next.Stage = StageQueuedForASR
				next.Error = ""
				next.FailedStage = ""
				next.Retryable = false
			})
			if err != nil {
				return RecoveryPlan{}, err
			}
			plan.ASR = append(plan.ASR, queued)
		case StageTranscribed:
			queued, err := s.Update(meta.CaptureID, func(next *CaptureMeta) {
				next.Stage = StageQueuedForDelivery
				next.Error = ""
				next.FailedStage = ""
				next.Retryable = false
			})
			if err != nil {
				return RecoveryPlan{}, err
			}
			plan.Delivery = append(plan.Delivery, queued)
		case StageQueuedForDelivery, StageDelivering:
			queued, err := s.Update(meta.CaptureID, func(next *CaptureMeta) {
				next.Stage = StageQueuedForDelivery
				next.Error = ""
				next.FailedStage = ""
				next.Retryable = false
			})
			if err != nil {
				return RecoveryPlan{}, err
			}
			plan.Delivery = append(plan.Delivery, queued)
		case StageFailed:
			continue
		}
	}
	return plan, nil
}

func (s *CaptureStore) recoverInflight(meta CaptureMeta, minPCMBytes int64) (CaptureMeta, error) {
	inflight := meta.InflightPCMPath
	if inflight == "" {
		inflight = filepath.Join(s.captureDir(meta.CaptureID), CaptureInflightPCM)
	}
	info, err := os.Stat(inflight)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.Fail(meta.CaptureID, meta.Stage, fmt.Errorf("inflight audio missing"), false)
		}
		return CaptureMeta{}, err
	}
	if info.Size() < minPCMBytes {
		return s.Fail(meta.CaptureID, meta.Stage, fmt.Errorf("inflight audio too short: %d bytes", info.Size()), false)
	}
	finalPCM := filepath.Join(s.captureDir(meta.CaptureID), CaptureFinalPCM)
	if err := finalizeInflightPCM(inflight, finalPCM); err != nil {
		return CaptureMeta{}, err
	}
	pcm, err := os.ReadFile(finalPCM)
	if err != nil {
		return CaptureMeta{}, err
	}
	wavPath := filepath.Join(s.captureDir(meta.CaptureID), CaptureWAV)
	if err := WritePCM16WAV(wavPath, pcm, meta.SampleRate); err != nil {
		return CaptureMeta{}, err
	}
	return s.Update(meta.CaptureID, func(next *CaptureMeta) {
		next.Stage = StageAudioFinalized
		next.PCMPath = finalPCM
		next.AudioPath = wavPath
		next.PCMBytes = int64(len(pcm))
		next.AudioMs = AudioDurationMs(len(pcm), next.SampleRate)
		next.Error = ""
		next.FailedStage = ""
		next.Retryable = false
	})
}

func (s *CaptureStore) writeMeta(meta CaptureMeta) error {
	if meta.CaptureID == "" {
		return fmt.Errorf("capture id is empty")
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.CreatedAt
	}
	dir := s.captureDir(meta.CaptureID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, CaptureMetaFile)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

func (s *CaptureStore) captureDir(id string) string {
	return filepath.Join(s.Root, id)
}

func AudioDurationMs(pcmBytes int, sampleRate int) int {
	if sampleRate <= 0 {
		return 0
	}
	return pcmBytes * 1000 / 2 / sampleRate
}

func finalizeInflightPCM(inflight, finalPCM string) error {
	if _, err := os.Stat(finalPCM); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(finalPCM), 0o755); err != nil {
		return err
	}
	if err := os.Rename(inflight, finalPCM); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		data, readErr := os.ReadFile(inflight)
		if readErr != nil {
			return err
		}
		if writeErr := os.WriteFile(finalPCM, data, 0o600); writeErr != nil {
			return writeErr
		}
		return os.Remove(inflight)
	}
	return fmt.Errorf("inflight audio missing")
}

func newCaptureID(now time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("random capture id: %w", err)
	}
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:]), nil
}
