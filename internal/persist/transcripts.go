package persist

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

type HistoryQuery struct {
	From         time.Time
	Before       time.Time
	Stream       string
	TargetRef    string
	Limit        int
	IncludeEmpty bool
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

// NonEmpty returns every non-empty transcript record from dir.
// Records are returned oldest-to-newest so callers can replay them in reading order.
func NonEmpty(dir string) ([]Record, error) {
	return QueryHistory(dir, HistoryQuery{})
}

// LatestNonEmpty returns up to n non-empty transcript records from dir.
// Records are returned oldest-to-newest so callers can replay them in reading order.
func LatestNonEmpty(dir string, n int) ([]Record, error) {
	if n <= 0 {
		return nil, fmt.Errorf("count must be positive")
	}
	return QueryHistory(dir, HistoryQuery{Limit: n})
}

func QueryHistory(dir string, query HistoryQuery) ([]Record, error) {
	if query.Limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	type stampedRecord struct {
		rec Record
		at  time.Time
	}
	var records []stampedRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		rec, at, err := readHistoryRecord(path, info)
		if err != nil {
			return nil, err
		}
		if !query.IncludeEmpty && strings.TrimSpace(rec.Transcript) == "" {
			continue
		}
		if !query.From.IsZero() && at.Before(query.From) {
			continue
		}
		if !query.Before.IsZero() && !at.Before(query.Before) {
			continue
		}
		if query.Stream != "" && rec.TargetStream != query.Stream {
			continue
		}
		if query.TargetRef != "" && rec.TargetRef != query.TargetRef {
			continue
		}
		if rec.Timestamp.IsZero() {
			rec.Timestamp = at
		}
		records = append(records, stampedRecord{rec: rec, at: at})
	}

	sort.SliceStable(records, func(i, j int) bool {
		return records[i].at.Before(records[j].at)
	})
	if query.Limit > 0 && len(records) > query.Limit {
		records = records[len(records)-query.Limit:]
	}
	out := make([]Record, len(records))
	for i, record := range records {
		out[i] = record.rec
	}
	return out, nil
}

type HistoryPruneFilter struct {
	Empty              bool
	Failed             bool
	Successful         bool
	Stream             string
	Before             time.Time
	OlderThan          time.Duration
	KeepLast           int
	IncludeOrphanAudio bool
	Now                time.Time
}

type HistoryPrunePlan struct {
	Directory                string
	RecordsScanned           int
	RecordsMatched           int
	EmptyRecordsMatched      int
	FailedRecordsMatched     int
	SuccessfulRecordsMatched int
	JSONFilesMatched         int
	AudioFilesMatched        int
	OrphanAudioFilesMatched  int
	BytesMatched             int64
	files                    []historyPruneFile
}

type HistoryPruneResult struct {
	FilesDeleted int
	BytesDeleted int64
}

type historyPruneRecord struct {
	path     string
	rec      Record
	at       time.Time
	jsonSize int64
}

type historyPruneFile struct {
	path string
	size int64
	kind string
}

func (p *HistoryPrunePlan) FilesMatched() int {
	if p == nil {
		return 0
	}
	return len(p.files)
}

func (p *HistoryPrunePlan) Apply() (*HistoryPruneResult, error) {
	result := &HistoryPruneResult{}
	if p == nil {
		return result, nil
	}
	for _, file := range p.files {
		if err := os.Remove(file.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, fmt.Errorf("remove %s: %w", file.path, err)
		}
		result.FilesDeleted++
		result.BytesDeleted += file.size
	}
	return result, nil
}

func PlanHistoryPrune(dir string, filter HistoryPruneFilter) (*HistoryPrunePlan, error) {
	if filter.KeepLast < 0 {
		return nil, fmt.Errorf("keep-last must be zero or greater")
	}
	if filter.Now.IsZero() {
		filter.Now = time.Now()
	}

	plan := &HistoryPrunePlan{Directory: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return plan, nil
		}
		return nil, err
	}

	audioFiles := map[string]historyPruneFile{}
	referencedAudio := map[string]bool{}
	var records []historyPruneRecord
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		rawPath := filepath.Join(dir, entry.Name())
		path, ok := safeHistoryPath(dir, entry.Name())
		if !ok {
			path = rawPath
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		switch filepath.Ext(entry.Name()) {
		case ".wav":
			audioFiles[path] = historyPruneFile{path: path, size: info.Size(), kind: "audio"}
		case ".json":
			rec, at, err := readHistoryRecord(path, info)
			if err != nil {
				return nil, err
			}
			records = append(records, historyPruneRecord{
				path:     path,
				rec:      rec,
				at:       at,
				jsonSize: info.Size(),
			})
		}
	}

	sort.SliceStable(records, func(i, j int) bool {
		return records[i].at.Before(records[j].at)
	})
	for _, record := range records {
		plan.RecordsScanned++
		for _, audioPath := range recordAudioCandidates(dir, record) {
			if _, ok := audioFiles[audioPath]; ok {
				referencedAudio[audioPath] = true
			}
		}
	}

	var matched []historyPruneRecord
	if shouldSelectHistoryRecords(filter) {
		for _, record := range records {
			if matchesHistoryPruneFilter(record.rec, record.at, filter) {
				matched = append(matched, record)
			}
		}
		if filter.KeepLast > 0 {
			if len(matched) <= filter.KeepLast {
				matched = nil
			} else {
				matched = matched[:len(matched)-filter.KeepLast]
			}
		}
	}

	files := map[string]historyPruneFile{}
	addFile := func(file historyPruneFile) {
		if _, ok := files[file.path]; ok {
			return
		}
		files[file.path] = file
		plan.BytesMatched += file.size
		switch file.kind {
		case "json":
			plan.JSONFilesMatched++
		case "audio":
			plan.AudioFilesMatched++
		case "orphan-audio":
			plan.AudioFilesMatched++
			plan.OrphanAudioFilesMatched++
		}
	}

	for _, record := range matched {
		plan.RecordsMatched++
		if strings.TrimSpace(record.rec.Transcript) == "" {
			plan.EmptyRecordsMatched++
		}
		if record.rec.Success && record.rec.Error == "" {
			plan.SuccessfulRecordsMatched++
		} else {
			plan.FailedRecordsMatched++
		}
		addFile(historyPruneFile{path: record.path, size: record.jsonSize, kind: "json"})
		for _, audioPath := range recordAudioCandidates(dir, record) {
			if audioFile, ok := audioFiles[audioPath]; ok {
				addFile(audioFile)
			}
		}
	}

	if shouldIncludeAllAudio(filter) || filter.IncludeOrphanAudio {
		for path, audioFile := range audioFiles {
			if shouldIncludeAllAudio(filter) || !referencedAudio[path] {
				if !referencedAudio[path] {
					audioFile.kind = "orphan-audio"
				}
				addFile(audioFile)
			}
		}
	}

	plan.files = make([]historyPruneFile, 0, len(files))
	for _, file := range files {
		plan.files = append(plan.files, file)
	}
	sort.Slice(plan.files, func(i, j int) bool {
		return plan.files[i].path < plan.files[j].path
	})
	return plan, nil
}

func readHistoryRecord(path string, info os.FileInfo) (Record, time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, time.Time{}, fmt.Errorf("parse %s: %w", path, err)
	}
	at := rec.Timestamp
	if at.IsZero() {
		at = info.ModTime()
	}
	return rec, at, nil
}

func matchesHistoryPruneFilter(rec Record, at time.Time, filter HistoryPruneFilter) bool {
	if filter.Empty && strings.TrimSpace(rec.Transcript) != "" {
		return false
	}
	if filter.Failed && rec.Success && rec.Error == "" {
		return false
	}
	if filter.Successful && (!rec.Success || rec.Error != "") {
		return false
	}
	if filter.Stream != "" && rec.TargetStream != filter.Stream {
		return false
	}
	if !filter.Before.IsZero() && !at.Before(filter.Before) {
		return false
	}
	if filter.OlderThan > 0 && !at.Before(filter.Now.Add(-filter.OlderThan)) {
		return false
	}
	return true
}

func recordAudioCandidates(dir string, record historyPruneRecord) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path, ok := safeHistoryPath(dir, path)
		if !ok || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	add(record.rec.AudioPath)
	if record.rec.Timestamp.IsZero() {
		base := strings.TrimSuffix(filepath.Base(record.path), filepath.Ext(record.path))
		add(base + ".wav")
	} else {
		add(recordName(record.rec.Timestamp, ".wav"))
	}
	return out
}

func safeHistoryPath(dir, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(dir, candidate)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return absPath, true
}

func shouldIncludeAllAudio(filter HistoryPruneFilter) bool {
	return !filter.IncludeOrphanAudio && !hasHistoryRecordSelector(filter)
}

func shouldSelectHistoryRecords(filter HistoryPruneFilter) bool {
	return !filter.IncludeOrphanAudio || hasHistoryRecordSelector(filter)
}

func hasHistoryRecordSelector(filter HistoryPruneFilter) bool {
	return filter.Empty ||
		filter.Failed ||
		filter.Successful ||
		filter.Stream != "" ||
		!filter.Before.IsZero() ||
		filter.OlderThan != 0 ||
		filter.KeepLast != 0
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
