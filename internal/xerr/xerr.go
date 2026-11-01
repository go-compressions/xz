// Package xerr holds the error values shared by the xz and lzma packages, so
// that both re-export the very same variables and errors.Is works across them.
package xerr

import (
	"errors"
	"fmt"
	"io"
)

var (
	// ErrCorrupt is the root of every error caused by malformed input.
	ErrCorrupt = errors.New("xz: corrupt input")

	// ErrChecksum reports an integrity check (CRC32, CRC64, SHA-256) that
	// does not match the decoded data. It is also an ErrCorrupt.
	ErrChecksum = fmt.Errorf("%w: integrity check mismatch", ErrCorrupt)

	// ErrTruncated reports input that ends inside a structure. It is an
	// ErrCorrupt and an io.ErrUnexpectedEOF.
	ErrTruncated = &truncated{}

	// ErrTooLarge reports that decoding would need a dictionary window
	// larger than the configured MaxDictionary. Nothing is allocated for
	// the refused growth.
	ErrTooLarge = errors.New("xz: dictionary window exceeds MaxDictionary")

	// ErrUnsupported reports a well-formed feature this decoder does not
	// implement (an unknown filter ID or an unknown integrity check).
	ErrUnsupported = errors.New("xz: unsupported feature")
)

type truncated struct{}

func (*truncated) Error() string { return "xz: input truncated" }

func (*truncated) Is(target error) bool {
	return target == ErrCorrupt || target == io.ErrUnexpectedEOF
}

// Corrupt returns an error that wraps ErrCorrupt with a description.
func Corrupt(format string, args ...any) error {
	return fmt.Errorf("xz: "+format+": %w", append(args, ErrCorrupt)...)
}

// Unsupported returns an error that wraps ErrUnsupported with a description.
func Unsupported(format string, args ...any) error {
	return fmt.Errorf("xz: "+format+": %w", append(args, ErrUnsupported)...)
}

// Truncate maps a short read to ErrTruncated and leaves other errors alone.
func Truncate(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return ErrTruncated
	}
	return err
}
