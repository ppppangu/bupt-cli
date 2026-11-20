package cli

import (
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type borrowedTerminal struct {
	file   *os.File
	wake   *os.File
	mu     sync.Mutex
	closed bool
}

// A stream's copy goroutine must release stdin before tcell resumes. A pipe
// wakes its poll without closing the user's terminal or changing fd flags.
func borrowTerminalInput(in io.Reader) (io.Reader, func(), error) {
	if !isTerminal(in) {
		return in, func() {}, nil
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	t := &borrowedTerminal{file: in.(*os.File), wake: read}
	return t, func() {
		_ = write.Close()
		t.mu.Lock()
		defer t.mu.Unlock()
		t.closed = true
		_ = read.Close()
	}, nil
}

func (t *borrowedTerminal) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, io.EOF
	}
	fds := []unix.PollFd{
		{Fd: int32(t.file.Fd()), Events: unix.POLLIN},
		{Fd: int32(t.wake.Fd()), Events: unix.POLLIN},
	}
	for {
		_, err := unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if fds[1].Revents != 0 {
			return 0, io.EOF
		}
		return t.file.Read(p)
	}
}
