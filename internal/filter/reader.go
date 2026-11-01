package filter

import "io"

// BufferSize is the size of the buffer a BCJ reader allocates on first use.
const BufferSize = 32 << 10

// Simple turns a Code (a BCJ filter) into an io.Reader. Bytes the filter
// cannot decide yet are held back; at the end of the input the held back tail
// passes through unchanged, as in liblzma's simple_coder.c.
type Simple struct {
	src  io.Reader
	code Code
	now  uint32

	buf      []byte
	rd       int // handed out up to here
	filtered int // buf[rd:filtered] is final
	have     int // valid bytes in buf
	eof      bool
	srcErr   error
	err      error
}

// NewSimple returns a reader that applies code to what src yields, starting
// at stream position start.
func NewSimple(src io.Reader, code Code, start uint32) *Simple {
	return &Simple{src: src, code: code, now: start}
}

func (s *Simple) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.buf == nil {
		s.buf = make([]byte, BufferSize)
	}
	for {
		if s.rd < s.filtered {
			n := copy(p, s.buf[s.rd:s.filtered])
			s.rd += n
			return n, nil
		}
		if s.err != nil {
			return 0, s.err
		}
		// Everything final was handed out: keep the undecided tail, refill.
		s.have = copy(s.buf, s.buf[s.filtered:s.have])
		s.rd, s.filtered = 0, 0
		if !s.eof && s.srcErr == nil {
			n, err := s.src.Read(s.buf[s.have:])
			s.have += n
			if err == io.EOF {
				s.eof = true
			} else if err != nil {
				s.srcErr = err
			}
		}
		done := s.code(s.now, s.buf[:s.have])
		s.now += uint32(done)
		s.filtered = done
		if s.eof {
			s.filtered = s.have
		}
		if s.filtered == 0 {
			switch {
			case s.eof:
				s.err = io.EOF
			case s.srcErr != nil:
				s.err = s.srcErr
			}
		}
	}
}

// Delta is the Delta filter decoder (xz-file-format.txt, section 5.3.3).
type Delta struct {
	src      io.Reader
	distance int
	pos      uint8
	history  [256]byte
}

// NewDelta returns a reader that undoes the Delta filter with the given
// distance in 1..256.
func NewDelta(src io.Reader, distance int) *Delta {
	return &Delta{src: src, distance: distance}
}

func (d *Delta) Read(p []byte) (int, error) {
	n, err := d.src.Read(p)
	for i := 0; i < n; i++ {
		p[i] += d.history[uint8(d.distance)+d.pos]
		d.history[d.pos] = p[i]
		d.pos--
	}
	return n, err
}
