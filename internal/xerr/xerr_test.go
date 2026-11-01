package xerr

import (
	"errors"
	"io"
	"testing"
)

func TestErrors(t *testing.T) {
	if !errors.Is(ErrTruncated, ErrCorrupt) || !errors.Is(ErrTruncated, io.ErrUnexpectedEOF) {
		t.Fatal("ErrTruncated must be ErrCorrupt and io.ErrUnexpectedEOF")
	}
	if errors.Is(ErrTruncated, io.EOF) || errors.Is(ErrTruncated, ErrTooLarge) {
		t.Fatal("ErrTruncated matches too much")
	}
	if ErrTruncated.Error() == "" {
		t.Fatal("empty message")
	}
	if !errors.Is(ErrChecksum, ErrCorrupt) {
		t.Fatal("ErrChecksum must be ErrCorrupt")
	}
	if err := Corrupt("bad %d", 7); !errors.Is(err, ErrCorrupt) || err.Error() != "xz: bad 7: xz: corrupt input" {
		t.Fatal(err)
	}
	if err := Unsupported("filter %x", 9); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if Truncate(io.EOF) != ErrTruncated || Truncate(io.ErrUnexpectedEOF) != ErrTruncated {
		t.Fatal("Truncate must map both EOFs")
	}
	other := errors.New("other")
	if Truncate(other) != other {
		t.Fatal("Truncate must keep other errors")
	}
}
