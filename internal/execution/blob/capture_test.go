//go:build linux || darwin

package blob

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestCapturePublishesOnlyCompleteImmutableBytes(t *testing.T) {
	store, _ := testStore(t)
	id := uuid.NewString()
	data := bytes.Repeat([]byte{255, 0, 3, 17}, (9<<20)/4)
	status, err := store.Capture(id, func(out io.Writer) error { _, err := io.Copy(out, bytes.NewReader(data)); return err })
	if err != nil || !status.Ready || status.Manifest.Size != int64(len(data)) || status.Manifest.SHA256 != execprotocol.FileDigest(data) {
		t.Fatal(status, err)
	}
	page, err := store.Read(id, int64(len(data)-8), 8)
	if err != nil || !bytes.Equal(page.Data, data[len(data)-8:]) {
		t.Fatal(page, err)
	}
	if _, err := store.Capture(id, func(io.Writer) error { t.Fatal("repeated capture reopened source"); return nil }); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal(err)
	}
	failed := uuid.NewString()
	if _, err := store.Capture(failed, func(out io.Writer) error { _, _ = out.Write([]byte("partial")); return io.ErrUnexpectedEOF }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if _, err := store.Read(failed, 0, 100); err == nil {
		t.Fatal("partial capture exposed")
	}
}

func TestCaptureWriterCannotExceedReservationOrHideFailure(t *testing.T) {
	var data bytes.Buffer
	writer := &captureWriter{writer: &data, limit: 4}
	if _, err := writer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("5")); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal(err)
	}
	if _, err := writer.Write(nil); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal("write cleared previous failure", err)
	}
	if data.String() != "1234" {
		t.Fatal("exceeded reserved bytes")
	}
}
