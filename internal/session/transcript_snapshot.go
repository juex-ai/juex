package session

import (
	"errors"
	"fmt"
	"os"
)

// ErrTranscriptChanged reports that canonical transcript state no longer
// matches the snapshot or resident index that an operation started from.
var ErrTranscriptChanged = errors.New("session: transcript changed outside the resident session")

type transcriptSnapshot struct {
	path        string
	file        *os.File
	fingerprint transcriptFingerprint
}

func openTranscriptSnapshot(path string) (*transcriptSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fingerprint, err := fingerprintFromOpenFile(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &transcriptSnapshot{path: path, file: file, fingerprint: fingerprint}, nil
}

func (snapshot *transcriptSnapshot) close() {
	if snapshot != nil && snapshot.file != nil {
		_ = snapshot.file.Close()
		snapshot.file = nil
	}
}

func (snapshot *transcriptSnapshot) verify() error {
	if snapshot == nil || snapshot.file == nil {
		return fmt.Errorf("%w: snapshot is closed", ErrTranscriptChanged)
	}
	openFingerprint, err := fingerprintFromOpenFile(snapshot.file)
	if err != nil {
		return fmt.Errorf("%w: inspect open file: %v", ErrTranscriptChanged, err)
	}
	pathFingerprint, err := fingerprintFromPath(snapshot.path)
	if err != nil {
		return fmt.Errorf("%w: inspect canonical path: %v", ErrTranscriptChanged, err)
	}
	if openFingerprint != snapshot.fingerprint || pathFingerprint != snapshot.fingerprint {
		return ErrTranscriptChanged
	}
	return nil
}
