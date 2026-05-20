package persist

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Record struct {
	Timestamp      time.Time `json:"ts"`
	AudioMs        int       `json:"audio_ms"`
	AudioPath      string    `json:"audio_path,omitempty"`
	AudioSaveError string    `json:"audio_save_error,omitempty"`
	Transcript     string    `json:"transcript"`
	Raw            string    `json:"raw,omitempty"`
	TargetStream   string    `json:"target_stream,omitempty"`
	TargetType     string    `json:"target_type,omitempty"`
	TargetRef      string    `json:"target_ref,omitempty"`
	Mode           string    `json:"mode"` // "pty" | "noop"
	Success        bool      `json:"success"`
	Error          string    `json:"error,omitempty"`
	InferenceMs    int       `json:"inference_ms,omitempty"`
}

// Save writes the record to <dir>/<ISO8601>.json. Best-effort: if the dir cannot be
// created or the write fails, returns the error but the record is lost.
func Save(dir string, rec Record) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := recordName(rec.Timestamp, ".json")
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

// SavePCM16WAV writes raw mono PCM16 audio to <dir>/<ISO8601>.wav.
func SavePCM16WAV(dir string, ts time.Time, pcm []byte, sampleRate int) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, recordName(ts, ".wav"))
	data, err := wavData(pcm, sampleRate)
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func recordName(ts time.Time, ext string) string {
	return ts.UTC().Format("20060102T150405.000000Z") + ext
}

func wavData(pcm []byte, sampleRate int) ([]byte, error) {
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("pcm byte count must be even")
	}
	const (
		channels      = 1
		bitsPerSample = 16
		audioFormat   = 1 // PCM
	)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	dataSize := uint32(len(pcm))
	riffSize := uint32(36 + len(pcm))

	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, riffSize)
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(audioFormat))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&b, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bitsPerSample))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, dataSize)
	b.Write(pcm)
	return b.Bytes(), nil
}
