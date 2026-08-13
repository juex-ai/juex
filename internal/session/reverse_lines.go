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
	buf          reverseLineBuffer
}

type reverseLineBuffer struct {
	storage []byte
	start   int
	end     int
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
		buffered := r.buf.bytes()
		if newline := bytes.LastIndexByte(buffered, '\n'); newline >= 0 {
			line := buffered[newline+1:]
			r.buf.trimEnd(newline)
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
				r.buf.reset()
				return nil, io.EOF
			}
			if r.buf.len() == 0 {
				return nil, io.EOF
			}
			line := r.buf.bytes()
			r.buf.reset()
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
		r.buf.prepend(chunk)
		buffered = r.buf.bytes()
		if bytes.IndexByte(buffered, '\n') < 0 && r.lineTooLong(buffered) {
			return nil, errEventLineTooLong
		}
	}
}

func (r *reverseLineReader) lineTooLong(line []byte) bool {
	return r.maxLineBytes > 0 && len(line) > r.maxLineBytes
}

func (b *reverseLineBuffer) bytes() []byte {
	return b.storage[b.start:b.end]
}

func (b *reverseLineBuffer) len() int {
	return b.end - b.start
}

func (b *reverseLineBuffer) trimEnd(length int) {
	b.end = b.start + length
}

func (b *reverseLineBuffer) reset() {
	b.storage = nil
	b.start = 0
	b.end = 0
}

func (b *reverseLineBuffer) prepend(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if b.start >= len(chunk) {
		b.start -= len(chunk)
		copy(b.storage[b.start:], chunk)
		return
	}

	required := len(chunk) + b.len()
	capacity := max(required, max(len(b.storage)*2, reverseLineBlockBytes))
	grown := make([]byte, capacity)
	start := capacity - required
	copy(grown[start:], chunk)
	copy(grown[start+len(chunk):], b.bytes())
	b.storage = grown
	b.start = start
	b.end = capacity
}
