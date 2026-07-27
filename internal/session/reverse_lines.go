package session

import (
	"bytes"
	"errors"
	"io"
	"os"
)

const reverseLineBlockBytes = 64 * 1024

type reverseLineReader struct {
	file   *os.File
	offset int64
	buf    []byte
}

func newReverseLineReader(file *os.File) (*reverseLineReader, error) {
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return &reverseLineReader{file: file, offset: st.Size()}, nil
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
			if len(line) > maxEventLineBytes {
				return nil, errEventLineTooLong
			}
			return line, nil
		}
		if r.offset == 0 {
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
			if len(line) > maxEventLineBytes {
				return nil, errEventLineTooLong
			}
			return line, nil
		}

		size := int64(reverseLineBlockBytes)
		if r.offset < size {
			size = r.offset
		}
		r.offset -= size
		chunk := make([]byte, int(size))
		n, err := r.file.ReadAt(chunk, r.offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		chunk = chunk[:n]
		combined := make([]byte, len(chunk)+len(r.buf))
		copy(combined, chunk)
		copy(combined[len(chunk):], r.buf)
		r.buf = combined
		if bytes.IndexByte(r.buf, '\n') < 0 && len(r.buf) > maxEventLineBytes {
			return nil, errEventLineTooLong
		}
	}
}
