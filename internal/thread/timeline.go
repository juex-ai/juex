package thread

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const reverseReadBlock = int64(64 * 1024)

type pageCursor struct {
	Offset int64  `json:"o"`
	Seq    uint64 `json:"s"`
}

func LoadTimelinePage(dir, cursor string, limit int) (TimelinePage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	threadID := filepath.Base(dir)
	if !ValidID(threadID) {
		return TimelinePage{}, fmt.Errorf("%w: %q", ErrInvalidID, threadID)
	}
	file, err := os.Open(filepath.Join(dir, journalFile))
	if err != nil {
		return TimelinePage{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return TimelinePage{}, err
	}
	position := info.Size()
	expectedBefore := uint64(0)
	if cursor != "" {
		decoded, err := decodePageCursor(cursor)
		if err != nil {
			return TimelinePage{}, err
		}
		if decoded.Offset < 0 || decoded.Offset > position {
			return TimelinePage{}, fmt.Errorf("thread: invalid page cursor offset")
		}
		position = decoded.Offset
		expectedBefore = decoded.Seq
	}
	var groups [][]TimelineItem
	oldestSeq := uint64(0)
	itemCount := 0
	for position > 0 && itemCount < limit {
		line, start, err := previousJournalLine(file, position)
		if err != nil {
			return TimelinePage{}, err
		}
		position = start
		var commit Commit
		if err := decodeCommit(line, &commit); err != nil {
			return TimelinePage{}, fmt.Errorf("%w at offset %d: %v", ErrCorruptJournal, start, err)
		}
		if err := validateCommit(threadID, commit); err != nil {
			return TimelinePage{}, fmt.Errorf("%w at sequence %d: %v", ErrCorruptJournal, commit.Seq, err)
		}
		if expectedBefore != 0 && commit.Seq >= expectedBefore {
			return TimelinePage{}, fmt.Errorf("thread: stale or invalid page cursor sequence")
		}
		expectedBefore = commit.Seq
		oldestSeq = commit.Seq
		items := timelineItems(commit)
		if len(items) > 0 {
			groups = append(groups, items)
			itemCount += len(items)
		}
	}
	page := TimelinePage{HasMoreBefore: position > 0}
	for i := len(groups) - 1; i >= 0; i-- {
		page.Items = append(page.Items, groups[i]...)
	}
	if page.HasMoreBefore {
		page.PreviousCursor, err = encodePageCursor(pageCursor{Offset: position, Seq: oldestSeq})
		if err != nil {
			return TimelinePage{}, err
		}
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
				Type:             fact.Type,
				At:               commit.At,
				FromGenerationID: fact.FromGenerationID,
				ToGenerationID:   fact.ToGenerationID,
				Summary:          fact.Summary,
				Automatic:        fact.Automatic,
			}
			items = append(items, TimelineItem{Type: "activity", Seq: commit.Seq, At: commit.At, Activity: &activity})
		}
	}
	return items
}

func previousJournalLine(file *os.File, end int64) ([]byte, int64, error) {
	if end <= 0 {
		return nil, 0, io.EOF
	}
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, end-1); err != nil {
		return nil, 0, err
	}
	if last[0] != '\n' {
		return nil, 0, fmt.Errorf("%w: page boundary is not newline terminated", ErrCorruptJournal)
	}
	searchEnd := end - 1
	for searchEnd > 0 {
		start := searchEnd - reverseReadBlock
		if start < 0 {
			start = 0
		}
		buffer := make([]byte, searchEnd-start)
		if _, err := file.ReadAt(buffer, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, err
		}
		if index := bytes.LastIndexByte(buffer, '\n'); index >= 0 {
			lineStart := start + int64(index) + 1
			line := make([]byte, end-lineStart)
			if _, err := file.ReadAt(line, lineStart); err != nil && !errors.Is(err, io.EOF) {
				return nil, 0, err
			}
			return line, lineStart, nil
		}
		searchEnd = start
	}
	line := make([]byte, end)
	if _, err := file.ReadAt(line, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	return line, 0, nil
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
	return cursor, nil
}
