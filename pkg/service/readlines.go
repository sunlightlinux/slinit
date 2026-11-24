package service

import (
	"bufio"
	"io"
)

// readLines calls fn with each line read from r, newline stripped, until
// r ends or fn returns false. A line longer than max is passed on in
// pieces of max bytes rather than ending the loop.
//
// It replaces bufio.Scanner on pipes a service writes to. Scanner stops
// for good on a line longer than its buffer (ErrTooLong), and the
// reader then stopped draining the pipe: the service blocked in
// write(2) once the pipe filled, or got SIGPIPE when the read end was
// closed behind it. A service's output is not ours to refuse.
func readLines(r io.Reader, max int, fn func(line []byte) bool) {
	br := bufio.NewReaderSize(r, max)
	for {
		line, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			line = line[:len(line)-1]
		case err == bufio.ErrBufferFull:
			// A piece of an over-long line: hand it on and keep going.
		case len(line) == 0:
			return
		}
		if !fn(line) {
			return
		}
		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}
