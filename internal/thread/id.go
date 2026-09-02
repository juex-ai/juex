package thread

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	MainID    = "0"
	MainAlias = "main"
)

const workerAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

var ErrInvalidID = errors.New("thread: invalid id")

func ValidID(id string) bool {
	if id == MainID {
		return true
	}
	if len(id) != 6 {
		return false
	}
	for _, ch := range id {
		if !strings.ContainsRune(workerAlphabet, ch) {
			return false
		}
	}
	return true
}

func ValidWorkerID(id string) bool {
	return id != MainID && ValidID(id)
}

func newWorkerID(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	var raw [6]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return "", fmt.Errorf("thread: generate worker id: %w", err)
	}
	var id [6]byte
	for i := range raw {
		id[i] = workerAlphabet[int(raw[i])&31]
	}
	return string(id[:]), nil
}

func DefaultWorkerAlias(id string) string {
	return "worker_#" + id
}
