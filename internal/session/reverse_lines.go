package session

import (
	"bytes"
	"errors"
	"io"
	"os"
)

const reverseLineBlockBytes = 64 * 1024

type reverseLineReader struct {
	file         *os.File
	offset       int64
	floor        int64
	floorAligned bool
	maxLineBytes int
	buf          []byte
}

func newReverseLineReader(file *os.File) (*reverseLineReader, error) {
	return newBoundedReverseLineReader(file, 0)
}

func newUncappedReverseLineReaderAt(file *os.File, floor int64) (*reverseLineReader, error) {
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if floor < 0 || floor > st.Size() {
		return nil, errors.New("session: invalid reverse-line floor")
	}
	return &reverseLineReader{file: file, offset: st.Size(), floor: floor, floorAligned: true}, nil
}

// newBoundedReverseLineReader reads at most maxBytes from the end of file.
// A non-positive limit keeps the original unbounded behavior.
func newBoundedReverseLineReader(file *os.File, maxBytes int64) (*reverseLineReader, error) {
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	floor := int64(0)
	if maxBytes > 0 && st.Size() > maxBytes {
		floor = st.Size() - maxBytes
	}
	return &reverseLineReader{file: file, offset: st.Size(), floor: floor, maxLineBytes: maxEventLineBytes}, nil
}

func (r *reverseLineReader) next() ([]byte, error) {
	for {
		if newline := bytes.LastIndexByte(r.buf, '\n'); newline >= 0 {
			line := r.buf[newline+1:]
			r.buf = r.buf[:newline]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 {
				continue
			}
			if r.lineTooLong(line) {
				return nil, errEventLineTooLong
			}
			return line, nil
		}
		if r.offset == r.floor {
			// A bounded scan can begin in the middle of a line. Discard that
			// partial prefix instead of treating it as an event.
			if r.floor > 0 && !r.floorAligned {
				r.buf = nil
				return nil, io.EOF
			}
			if len(r.buf) == 0 {
				return nil, io.EOF
			}
			line := r.buf
			r.buf = nil
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 {
				return nil, io.EOF
			}
			if r.lineTooLong(line) {
				return nil, errEventLineTooLong
			}
			return line, nil
		}

		size := int64(reverseLineBlockBytes)
		if available := r.offset - r.floor; available < size {
			size = available
		}
		r.offset -= size
		chunk := make([]byte, int(size))
		n, err := r.file.ReadAt(chunk, r.offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		chunk = chunk[:n]
		r.buf = prependReverseLineChunk(r.buf, chunk)
		if bytes.IndexByte(r.buf, '\n') < 0 && r.lineTooLong(r.buf) {
			return nil, errEventLineTooLong
		}
	}
}

func (r *reverseLineReader) lineTooLong(line []byte) bool {
	return r.maxLineBytes > 0 && len(line) > r.maxLineBytes
}

func prependReverseLineChunk(buf, chunk []byte) []byte {
	required := len(chunk) + len(buf)
	if cap(buf) < required {
		capacity := max(required, max(cap(buf)*2, reverseLineBlockBytes))
		grown := make([]byte, len(buf), capacity)
		copy(grown, buf)
		buf = grown
	}
	oldLength := len(buf)
	buf = buf[:required]
	copy(buf[len(chunk):], buf[:oldLength])
	copy(buf, chunk)
	return buf
}
