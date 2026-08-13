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
	fileInfo    os.FileInfo
	fingerprint transcriptFingerprint
}

func openTranscriptSnapshot(path string) (*transcriptSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fileInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	fingerprint, err := fingerprintFromOpenFile(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &transcriptSnapshot{path: path, file: file, fileInfo: fileInfo, fingerprint: fingerprint}, nil
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
	openInfo, err := snapshot.file.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect open file: %v", ErrTranscriptChanged, err)
	}
	canonical, err := os.Open(snapshot.path)
	if err != nil {
		return fmt.Errorf("%w: open canonical path: %v", ErrTranscriptChanged, err)
	}
	defer canonical.Close()
	canonicalInfo, err := canonical.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect canonical path: %v", ErrTranscriptChanged, err)
	}
	if !sameTranscriptFile(snapshot.fileInfo, openInfo, canonicalInfo) {
		return ErrTranscriptChanged
	}
	openFingerprint, err := fingerprintFromOpenFile(snapshot.file)
	if err != nil {
		return fmt.Errorf("%w: fingerprint open file: %v", ErrTranscriptChanged, err)
	}
	canonicalFingerprint, err := fingerprintFromOpenFile(canonical)
	if err != nil {
		return fmt.Errorf("%w: fingerprint canonical path: %v", ErrTranscriptChanged, err)
	}
	if openFingerprint != snapshot.fingerprint || canonicalFingerprint != snapshot.fingerprint {
		return ErrTranscriptChanged
	}
	finalPathInfo, err := os.Stat(snapshot.path)
	if err != nil {
		return fmt.Errorf("%w: recheck canonical path: %v", ErrTranscriptChanged, err)
	}
	if !os.SameFile(canonicalInfo, finalPathInfo) {
		return ErrTranscriptChanged
	}
	return nil
}

func sameTranscriptFile(initial, open, canonical os.FileInfo) bool {
	return initial != nil && open != nil && canonical != nil &&
		os.SameFile(initial, open) && os.SameFile(initial, canonical)
}
