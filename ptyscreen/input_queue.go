package ptyscreen

import (
	"io"
	"sync"
)

// inputQueue holds what the program has not read yet. It has no bound, because
// nothing but a script's own keys and text can fill it.
type inputQueue struct {
	mu      sync.Mutex
	arrived sync.Cond
	pending []byte
	closed  bool
}

func newInputQueue() *inputQueue {
	queue := &inputQueue{}
	queue.arrived.L = &queue.mu
	return queue
}

func (q *inputQueue) add(input []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, input...)
	q.arrived.Signal()
}

func (q *inputQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.arrived.Signal()
}

// writeTo ends once the queue is closed and empty, or a write fails, which means
// the program has gone.
func (q *inputQueue) writeTo(destination io.Writer) {
	for {
		q.mu.Lock()
		for len(q.pending) == 0 && !q.closed {
			q.arrived.Wait()
		}
		next := q.pending
		q.pending = nil
		q.mu.Unlock()
		if len(next) == 0 {
			return
		}
		if _, err := destination.Write(next); err != nil {
			return
		}
	}
}
