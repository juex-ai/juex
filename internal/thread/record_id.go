package thread

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func newRecordID(prefix string) string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Errorf("thread: generate %s id: %w", prefix, err))
	}
	return prefix + hex.EncodeToString(raw[:])
}

func StableMessageID(createdAt time.Time, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return "msg_" + createdAt.UTC().Format("20060102T150405.000") + "_" + hex.EncodeToString(sum[:4])
}

func MessageCreatedAt(id string) (time.Time, bool) {
	const prefix = "msg_"
	if !strings.HasPrefix(id, prefix) {
		return time.Time{}, false
	}
	rest := strings.TrimPrefix(id, prefix)
	separator := strings.LastIndexByte(rest, '_')
	if separator <= 0 {
		return time.Time{}, false
	}
	createdAt, err := time.Parse("20060102T150405.000", rest[:separator])
	if err != nil {
		return time.Time{}, false
	}
	return createdAt.UTC(), true
}

func prepareMessage(message llm.Message) llm.Message {
	message = llm.ClassifyUserMessage(message)
	if message.ID == "" {
		message.ID = newRecordID("msg_")
	}
	if message.Blocks == nil {
		message.Blocks = []llm.Block{}
	}
	return message
}
