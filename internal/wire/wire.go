package wire

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const MaxMessage = 16 << 20
const (
	Input byte = iota
	Output
	Stderr
	Resize
	Exit
	Signal
	End
	Progress
	Failure
)

func ReadJSON(r *bufio.Reader, out any) error {
	b := make([]byte, 0, 4096)
	for {
		part, e := r.ReadSlice('\n')
		if len(b)+len(part) > MaxMessage {
			return fmt.Errorf("RPC message exceeds limit")
		}
		b = append(b, part...)
		if e == bufio.ErrBufferFull {
			continue
		}
		if e != nil {
			return e
		}
		return json.Unmarshal(b, out)
	}
}
func WriteJSON(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > MaxMessage {
		return fmt.Errorf("RPC message exceeds limit")
	}
	b = append(b, '\n')
	_, e = w.Write(b)
	return e
}

type Writer struct {
	W  io.Writer
	mu sync.Mutex
}

func (w *Writer) Frame(k byte, b []byte) error {
	if len(b) > MaxMessage {
		return fmt.Errorf("frame exceeds limit")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var h [5]byte
	h[0] = k
	binary.BigEndian.PutUint32(h[1:], uint32(len(b)))
	if _, e := w.W.Write(h[:]); e != nil {
		return e
	}
	_, e := w.W.Write(b)
	return e
}
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return 0, nil, e
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > MaxMessage {
		return 0, nil, fmt.Errorf("frame exceeds limit")
	}
	b := make([]byte, n)
	_, e := io.ReadFull(r, b)
	return h[0], b, e
}

type FrameWriter struct {
	Writer *Writer
	Kind   byte
}

func (w FrameWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n := min(len(b), 64<<10)
		if e := w.Writer.Frame(w.Kind, b[:n]); e != nil {
			return total, e
		}
		total += n
		b = b[n:]
	}
	return total, nil
}

type InputReader struct {
	R        io.Reader
	buffer   []byte
	ended    bool
	OnResize func([]byte)
	OnSignal func([]byte)
}

func (r *InputReader) Read(b []byte) (int, error) {
	for len(r.buffer) == 0 {
		if r.ended {
			return 0, io.EOF
		}
		k, p, e := ReadFrame(r.R)
		if e != nil {
			return 0, e
		}
		switch k {
		case Input:
			r.buffer = p
		case End:
			r.ended = true
		case Resize:
			if r.OnResize != nil {
				r.OnResize(p)
			}
		case Signal:
			if r.OnSignal != nil {
				r.OnSignal(p)
			}
		default:
			return 0, fmt.Errorf("unexpected input frame")
		}
	}
	n := copy(b, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}

type StreamSeeker struct {
	R    io.Reader
	Base int64
	pos  int64
	set  bool
}

func (s *StreamSeeker) Seek(off int64, whence int) (int64, error) {
	if !s.set && whence == io.SeekStart && off == s.Base {
		s.set = true
		s.pos = off
		return off, nil
	}
	if (whence == io.SeekCurrent && off == 0) || (whence == io.SeekStart && off == s.pos) {
		return s.pos, nil
	}
	return s.pos, fmt.Errorf("stream cannot seek backwards; retry using the persisted upload ID")
}
func (s *StreamSeeker) Read(b []byte) (int, error) {
	n, e := s.R.Read(b)
	s.pos += int64(n)
	return n, e
}
