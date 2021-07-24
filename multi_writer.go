package main

import (
	"io"
	"sync"
)

func (t *multiWriter) Remove(writers ...io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.writers) - 1; i > 0; i-- {
		for _, v := range writers {
			if t.writers[i] == v {
				t.writers = append(t.writers[:i], t.writers[i+1:]...)
				break
			}
		}
	}
}
func (t *multiWriter) Append(writers ...io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writers = append(t.writers, writers...)
}

type multiWriter struct {
	writers []io.Writer
	mu      sync.Mutex
}

func (t *multiWriter) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, w := range t.writers {
		n, err = w.Write(p)
		if err != nil {
			return
		}
		if n != len(p) {
			err = io.ErrShortWrite
			return
		}
	}
	return len(p), nil
}

// ConcurrentWrite will write to all writers concurrently, however this may be bad because it spawns potentially hundreds of goroutines a second.
// Source: https://gist.github.com/alexmullins/1c6cc6dc38a8e83ed2f6
func (t *multiWriter) ConcurrentWrite(p []byte) (n int, err error) {
	type data struct {
		n   int
		err error
	}

	results := make(chan data)

	for _, w := range t.writers {
		go func(wr io.Writer, p []byte, ch chan data) {
			n, err = wr.Write(p)
			if err != nil {
				ch <- data{n, err}
				return
			}
			if n != len(p) {
				ch <- data{n, io.ErrShortWrite}
				return
			}
			ch <- data{n, nil} //completed ok
		}(w, p, results)
	}

	for range t.writers {
		d := <-results
		if d.err != nil {
			return d.n, d.err
		}
	}
	return len(p), nil
}

// MultiWriter creates a writer that duplicates its writes to all the
// provided writers, similar to the Unix tee(1) command.
func MultiWriter(writers ...io.Writer) io.Writer {
	w := make([]io.Writer, len(writers))
	copy(w, writers)
	return &multiWriter{writers: w}
}
