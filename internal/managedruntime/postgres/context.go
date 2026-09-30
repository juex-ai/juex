package postgres

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) ReadContext(ctx context.Context, scope managedruntime.Scope, thread, reference string, offset, limit int) (managedruntime.ContextPage, error) {
	var page managedruntime.ContextPage
	messageID, index, field, err := managedruntime.ParseContextReference(reference)
	if err != nil || offset < 0 || limit < 0 || limit > 4096 || thread == "" {
		return page, managedruntime.ErrInvalid
	}
	if limit == 0 {
		limit = 2048
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return page, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return page, err
	}
	if _, err := readThread(ctx, tx, scope.AgentID, thread); err != nil {
		return page, err
	}
	var encoded []byte
	if err := tx.QueryRow(ctx, `SELECT data FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND data->>'id'=$2 ORDER BY sequence LIMIT 1`, thread, messageID).Scan(&encoded); err != nil {
		return page, classify(err)
	}
	var message llm.Message
	if err := json.Unmarshal(encoded, &message); err != nil {
		return page, err
	}
	if index >= len(message.Blocks) {
		return page, managedruntime.ErrInvalid
	}
	block := message.Blocks[index]
	var content string
	switch field {
	case "text":
		content = block.Text
	case "content":
		content = block.Content
	case "input":
		if block.Type != llm.BlockToolUse {
			return page, managedruntime.ErrInvalid
		}
		data, err := json.Marshal(block.Input)
		if err != nil {
			return page, err
		}
		content = string(data)
	}
	if offset > len(content) || offset < len(content) && !utf8.RuneStart(content[offset]) {
		return page, managedruntime.ErrInvalid
	}
	end := min(len(content), offset+limit)
	for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	if end == offset && offset < len(content) {
		return page, managedruntime.ErrInvalid
	}
	page = managedruntime.ContextPage{Reference: reference, Text: content[offset:end], Offset: offset, NextOffset: end, TotalBytes: len(content), HasMore: end < len(content)}
	return page, tx.Commit(ctx)
}
