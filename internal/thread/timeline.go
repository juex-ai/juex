package thread

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	timelineCursorVersion  = 1
	maxTimelineScanRecords = 4096
)

type pageCursor struct {
	Version      int    `json:"v"`
	ThreadID     string `json:"t"`
	GenerationID string `json:"g"`
	EndOffset    int64  `json:"o"`
	BeforeSeq    uint64 `json:"s"`
}

func (t *Thread) Timeline(cursor string, limit int) (TimelinePage, error) {
	if t == nil {
		return TimelinePage{}, fmt.Errorf("thread: nil Thread")
	}
	t.mu.Lock()
	if t.closed || t.eventStore == nil {
		t.mu.Unlock()
		return TimelinePage{}, fmt.Errorf("thread: closed")
	}
	generations, err := t.eventStore.captureGenerations(t.state.Projection.Generations)
	if err != nil {
		t.mu.Unlock()
		return TimelinePage{}, err
	}
	last := t.state.Projection.EventCursor
	id := t.ID
	t.mu.Unlock()
	page, readErr := loadTimelinePage(id, generations, last, cursor, limit)
	return page, errors.Join(readErr, closeCapturedGenerations(generations))
}

func loadTimelinePage(threadID string, generations []capturedGeneration, latest EventCursor, cursor string, limit int) (TimelinePage, error) {
	return loadTimelinePageWithScanLimit(threadID, generations, latest, cursor, limit, maxTimelineScanRecords)
}

func loadTimelinePageWithScanLimit(
	threadID string,
	generations []capturedGeneration,
	latest EventCursor,
	cursor string,
	limit int,
	scanLimit int,
) (TimelinePage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if scanLimit <= 0 {
		return TimelinePage{}, fmt.Errorf("thread: timeline scan limit must be positive")
	}
	if len(generations) == 0 {
		return TimelinePage{}, fmt.Errorf("%w: missing Generation registry", ErrInvalidMetadata)
	}
	generationIndex := len(generations) - 1
	position := latest.Offset
	expectedBefore := latest.Seq + 1
	if cursor != "" {
		decoded, err := decodePageCursor(cursor)
		if err != nil {
			return TimelinePage{}, err
		}
		if decoded.ThreadID != threadID {
			return TimelinePage{}, fmt.Errorf("thread: page cursor belongs to another Thread")
		}
		generationIndex = -1
		for index := range generations {
			if generations[index].ID == decoded.GenerationID {
				generationIndex = index
				break
			}
		}
		if generationIndex < 0 {
			return TimelinePage{}, fmt.Errorf("thread: page cursor names an unknown Generation")
		}
		position = decoded.EndOffset
		expectedBefore = decoded.BeforeSeq
	}

	var groups [][]TimelineItem
	itemCount := 0
	scannedRecords := 0
	oldestSeq := expectedBefore
	for generationIndex >= 0 && itemCount < limit && scannedRecords < scanLimit {
		generation := generations[generationIndex]
		if position == 0 {
			generationIndex--
			if generationIndex >= 0 {
				position = generations[generationIndex].End
			}
			continue
		}
		readLimit := limit - itemCount
		if readLimit < 1 {
			readLimit = 1
		}
		remainingBudget := scanLimit - scannedRecords
		if readLimit > remainingBudget {
			readLimit = remainingBudget
		}
		commits, nextPosition, nextExpected, err := readGenerationReverse(
			threadID, generation, position, readLimit, expectedBefore,
		)
		if err != nil {
			return TimelinePage{}, err
		}
		position = nextPosition
		expectedBefore = nextExpected
		oldestSeq = nextExpected
		for _, commit := range commits {
			scannedRecords++
			items := timelineItems(commit.Commit)
			if len(items) > 0 {
				groups = append(groups, items)
				itemCount += len(items)
			}
		}
	}

	page := TimelinePage{Items: make([]TimelineItem, 0, itemCount)}
	for index := len(groups) - 1; index >= 0; index-- {
		page.Items = append(page.Items, groups[index]...)
	}
	page.HasMoreBefore = position > 0 || generationIndex > 0
	if page.HasMoreBefore {
		encoded, err := encodePageCursor(pageCursor{
			Version: timelineCursorVersion, ThreadID: threadID,
			GenerationID: generations[generationIndex].ID,
			EndOffset:    position, BeforeSeq: oldestSeq,
		})
		if err != nil {
			return TimelinePage{}, err
		}
		page.PreviousCursor = encoded
	}
	return page, nil
}

func timelineItems(commit Commit) []TimelineItem {
	var items []TimelineItem
	for _, fact := range commit.Facts {
		switch fact.Type {
		case FactMessageAppended:
			message := *fact.Message
			items = append(items, TimelineItem{Type: "message", Seq: commit.Seq, At: commit.At, Message: &message})
		case FactContextRenewed, FactContextCompacted:
			activity := Activity{
				Type: fact.Type, At: commit.At, FromGenerationID: fact.FromGenerationID,
				ToGenerationID: fact.ToGenerationID, Summary: fact.Summary, Automatic: fact.Automatic,
			}
			items = append(items, TimelineItem{Type: "activity", Seq: commit.Seq, At: commit.At, Activity: &activity})
		}
	}
	return items
}

func encodePageCursor(cursor pageCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodePageCursor(encoded string) (pageCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return pageCursor{}, fmt.Errorf("thread: invalid page cursor")
	}
	var cursor pageCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return pageCursor{}, fmt.Errorf("thread: invalid page cursor")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return pageCursor{}, fmt.Errorf("thread: invalid page cursor")
	}
	if cursor.Version != timelineCursorVersion || cursor.ThreadID == "" || cursor.GenerationID == "" ||
		cursor.EndOffset < 0 || cursor.BeforeSeq == 0 {
		return pageCursor{}, fmt.Errorf("thread: invalid page cursor")
	}
	return cursor, nil
}
