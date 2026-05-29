package persist

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type RedemptionSelection struct {
	Messages []Record
	Text     string
}

func QueryOwnedHistory(dir string, query HistoryQuery) ([]Record, error) {
	if query.Limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}
	all, err := QueryHistory(dir, HistoryQuery{IncludeEmpty: true})
	if err != nil {
		return nil, err
	}
	owned := computeOwnedHistory(all)
	filtered := make([]Record, 0, len(owned))
	for _, rec := range owned {
		if strings.TrimSpace(rec.Transcript) == "" {
			continue
		}
		at := rec.Timestamp
		if !query.From.IsZero() && at.Before(query.From) {
			continue
		}
		if !query.Before.IsZero() && !at.Before(query.Before) {
			continue
		}
		if query.Stream != "" && rec.OwnedStream != query.Stream {
			continue
		}
		if query.TargetRef != "" && rec.TargetRef != query.TargetRef {
			continue
		}
		filtered = append(filtered, rec)
	}
	if query.Limit > 0 && len(filtered) > query.Limit {
		filtered = filtered[len(filtered)-query.Limit:]
	}
	return filtered, nil
}

func SelectRedemptionMessages(dir, fromStream string, last int, separator string) (RedemptionSelection, error) {
	if strings.TrimSpace(fromStream) == "" {
		return RedemptionSelection{}, fmt.Errorf("source stream is required")
	}
	if last <= 0 {
		return RedemptionSelection{}, fmt.Errorf("last must be positive")
	}
	records, err := QueryOwnedHistory(dir, HistoryQuery{Stream: fromStream})
	if err != nil {
		return RedemptionSelection{}, err
	}
	delivered := make([]Record, 0, len(records))
	for _, rec := range records {
		if !isDeliveredTranscript(rec) {
			continue
		}
		delivered = append(delivered, rec)
	}
	if len(delivered) == 0 {
		return RedemptionSelection{}, fmt.Errorf("no delivered transcript messages owned by stream %q", fromStream)
	}
	if len(delivered) < last {
		return RedemptionSelection{}, fmt.Errorf("stream %q has only %d delivered transcript messages", fromStream, len(delivered))
	}
	selected := delivered[len(delivered)-last:]
	parts := make([]string, 0, len(selected))
	for _, rec := range selected {
		parts = append(parts, strings.TrimSpace(rec.Transcript))
	}
	return RedemptionSelection{
		Messages: selected,
		Text:     strings.Join(parts, separator),
	}, nil
}

func SaveRedemption(dir string, fromStream, toStream string, messages []Record, text string) (*RedemptionRecord, error) {
	if strings.TrimSpace(fromStream) == "" {
		return nil, fmt.Errorf("source stream is required")
	}
	if strings.TrimSpace(toStream) == "" {
		return nil, fmt.Errorf("destination stream is required")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("message selection is empty")
	}
	ids := make([]string, 0, len(messages))
	for _, msg := range messages {
		ids = append(ids, RecordMessageID(msg))
	}
	now := time.Now().UTC()
	id, err := newRedemptionID(now)
	if err != nil {
		return nil, err
	}
	redemption := &RedemptionRecord{
		ID:         id,
		At:         now,
		FromStream: fromStream,
		ToStream:   toStream,
		MessageIDs: ids,
		Text:       text,
	}
	rec := Record{
		Timestamp:  now,
		MessageID:  id,
		Type:       "redemption",
		Redemption: redemption,
		Mode:       "history",
		Success:    true,
	}
	if err := Save(dir, rec); err != nil {
		return nil, err
	}
	return redemption, nil
}

func RecordMessageID(rec Record) string {
	if rec.MessageID != "" {
		return rec.MessageID
	}
	if rec.CaptureID != "" {
		return "capture:" + rec.CaptureID
	}
	return rec.Timestamp.UTC().Format(time.RFC3339Nano) + "|" + rec.TargetStream + "|" + rec.TargetRef + "|" + rec.Transcript
}

func computeOwnedHistory(records []Record) []Record {
	type messageState struct {
		index    int
		original string
		owner    string
	}
	messages := map[string]messageState{}
	out := make([]Record, 0, len(records))
	for _, rec := range records {
		if rec.Type == "redemption" && rec.Redemption != nil {
			redemption := rec.Redemption
			for _, id := range redemption.MessageIDs {
				state, ok := messages[id]
				if !ok {
					continue
				}
				if state.owner != redemption.FromStream {
					continue
				}
				state.owner = redemption.ToStream
				messages[id] = state
				out[state.index].OwnedStream = state.owner
				out[state.index].RedeemedFrom = state.original
				out[state.index].RedeemedTo = state.owner
			}
			continue
		}
		if strings.TrimSpace(rec.Transcript) == "" {
			continue
		}
		id := RecordMessageID(rec)
		rec.MessageID = id
		if rec.OwnedStream == "" {
			rec.OwnedStream = rec.TargetStream
		}
		messages[id] = messageState{
			index:    len(out),
			original: rec.TargetStream,
			owner:    rec.OwnedStream,
		}
		out = append(out, rec)
	}
	return out
}

func isDeliveredTranscript(rec Record) bool {
	return rec.Type != "redemption" &&
		rec.Success &&
		rec.Error == "" &&
		strings.TrimSpace(rec.Transcript) != "" &&
		strings.TrimSpace(rec.OwnedStream) != ""
}

func newRedemptionID(now time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("random redemption id: %w", err)
	}
	return "redeem-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:]), nil
}
