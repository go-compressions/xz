// Package lzmacore is the LZMA decoder shared by the xz and lzma packages.
//
// It is a port of the reference decoder in Igor Pavlov's LZMA SDK
// (DOC/lzma-specification.txt, CPP/7zip/Bundles/LzmaSpec/LzmaSpec.cpp, public
// domain) with one deliberate departure: the dictionary window is allocated
// lazily. See the package README for the argument that output is unchanged.
package lzmacore

import "github.com/go-compressions/xz/internal/xerr"

// InitialWindow is the size of the first window allocation (or the whole
// dictionary when that is smaller). A header may declare up to 4 GiB; what
// is allocated follows the data actually decoded.
const InitialWindow = 16 << 10

// window is the LZMA "OutWindow" of the reference decoder: a circular buffer
// of the last size bytes. Unlike the reference it starts small and doubles
// as pos advances until it reaches size, and only then wraps. Before it is
// full every valid distance points into data already written
// (CheckDistance in the reference is dist <= Pos || IsFull), so the
// bytes read back are the same as with a window allocated whole.
//
// buf[rd:pos] is decoded output not yet handed to the caller.
type window struct {
	buf   []byte
	pos   int    // next write index
	rd    int    // output handed out up to here
	size  uint64 // dictionary size: the wrap length
	max   uint64 // ceiling on len(buf)
	full  bool   // the buffer has wrapped at least once (IsFull)
	total uint32 // TotalPos: bytes written since the last dictionary reset
}

// reset prepares the window for a new dictionary of the given size. A buffer
// from a previous use is kept (and cut to size) so that consecutive blocks do
// not reallocate.
func (w *window) reset(size, max uint64) {
	w.size, w.max = size, max
	w.pos, w.rd, w.full, w.total = 0, 0, false, 0
	if c := cap(w.buf); c > 0 {
		n := uint64(c)
		if n > size {
			n = size
		}
		w.buf = w.buf[:n]
		return
	}
	n := uint64(InitialWindow)
	if n > size {
		n = size
	}
	if n > max {
		n = max
	}
	w.buf = make([]byte, n)
}

// resetDict is the LZMA2 dictionary reset: history is forgotten, memory kept.
func (w *window) resetDict() {
	w.pos, w.rd, w.full, w.total = 0, 0, false, 0
}

// advance is called when pos has reached len(buf) and a byte has to be
// written. It wraps when the window has reached the dictionary size, and
// otherwise doubles it. Growth beyond max fails before anything is allocated.
// The caller guarantees rd == pos when the buffer is at the dictionary size.
func (w *window) advance() error {
	n := len(w.buf)
	if uint64(n) == w.size {
		w.pos, w.rd, w.full = 0, 0, true
		return nil
	}
	next := uint64(n) * 2
	if next > w.size {
		next = w.size
	}
	if next > w.max {
		next = w.max
	}
	if next <= uint64(n) {
		return xerr.ErrTooLarge
	}
	nb := make([]byte, next)
	copy(nb, w.buf)
	w.buf = nb
	return nil
}

// prev returns the byte before pos (GetByte(1)); the caller has checked that
// the window is not empty.
func (w *window) prev() byte {
	if w.pos > 0 {
		return w.buf[w.pos-1]
	}
	return w.buf[len(w.buf)-1]
}

// at returns the byte dist positions back (GetByte(dist), dist >= 1).
func (w *window) at(dist int) byte {
	i := w.pos - dist
	if i < 0 {
		i += len(w.buf)
	}
	return w.buf[i]
}

func (w *window) empty() bool { return w.pos == 0 && !w.full }

// copyMatch appends n bytes copied from dist bytes back; n fits in the buffer.
func (w *window) copyMatch(dist, n int) {
	pos := w.pos
	src := pos - dist
	buf := w.buf
	switch {
	case src >= 0 && dist >= n:
		copy(buf[pos:pos+n], buf[src:src+n])
	case src >= 0:
		// Overlapping run: the copy must see its own output.
		for i := 0; i < n; i++ {
			buf[pos+i] = buf[src+i]
		}
	default:
		src += len(buf)
		for i := 0; i < n; i++ {
			buf[pos+i] = buf[src]
			src++
			if src == len(buf) {
				src = 0
			}
		}
	}
	w.pos += n
	w.total += uint32(n)
}
