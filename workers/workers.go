// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package workers

import (
	"sync"
)

// Limit number of concurrent goroutines with resetable error
// Ensure minimal overhead for parallel ops
type Workers struct {
	count int
	queue chan *Job

	// tracking state
	lock              sync.RWMutex
	shouldShutdown    bool
	triggeredShutdown bool

	// single job execution
	err   error // requires lock
	sg    sync.WaitGroup
	tasks chan func() error

	// shutdown coordination
	ackShutdown    chan struct{}
	stopWorkers    chan struct{}
	stoppedWorkers chan struct{}
}

// Goroutines allocate a minimum of 2KB of memory, we can save this by reusing
// the context. This is especially useful if the goroutine stack is expanded
// during use.
//
// Current size: https://github.com/golang/go/blob/fa463cc96d797c218be4e218723f83be47e814c8/src/runtime/stack.go#L74-L75
// Backstory: https://medium.com/a-journey-with-go/go-how-does-the-goroutine-stack-size-evolve-447fc02085e5
func New(workers int, maxJobs int) *Workers {
	w := &Workers{
		count: workers,
		queue: make(chan *Job, maxJobs),

		tasks:          make(chan func() error),
		ackShutdown:    make(chan struct{}),
		stopWorkers:    make(chan struct{}),
		stoppedWorkers: make(chan struct{}),
	}
	w.processQueue()
	for i := 0; i < workers; i++ {
		w.startWorker()
	}
	return w
}

func (w *Workers) processQueue() {
	go func() {
		for j := range w.queue {
			// Don't do work if should shutdown
			w.lock.Lock()
			shouldShutdown := w.shouldShutdown
			w.lock.Unlock()
			if shouldShutdown {
				j.result <- ErrShutdown
				continue
			}

			// Process tasks
			for t := range j.tasks {
				w.sg.Add(1)
				w.tasks <- t
			}
			w.sg.Wait()

			// Send result to queue and reset err
			w.lock.Lock()
			close(j.completed)
			j.result <- w.err
			w.err = nil
			w.lock.Unlock()
		}

		// Ensure stop returns
		w.lock.Lock()
		if w.shouldShutdown && !w.triggeredShutdown {
			w.triggeredShutdown = true
			close(w.ackShutdown)
		}
		w.lock.Unlock()
	}()
}

func (w *Workers) startWorker() {
	go func() {
		for {
			select {
			case <-w.stopWorkers:
				w.stoppedWorkers <- struct{}{}
				return
			case j := <-w.tasks:
				// Check if we should even do the work
				w.lock.RLock()
				err := w.err
				w.lock.RUnlock()
				if err != nil {
					w.sg.Done()
					return
				}

				// Attempt to process the job
				if err := j(); err != nil {
					w.lock.Lock()
					if w.err == nil {
						w.err = err
					}
					w.lock.Unlock()
				}
				w.sg.Done()
			}
		}
	}()
}

func (w *Workers) Stop() {
	w.lock.Lock()
	w.shouldShutdown = true
	w.lock.Unlock()
	close(w.queue)

	// Wait for scheduler to return
	<-w.ackShutdown
	close(w.stopWorkers)

	// Wait for all workers to return
	for i := 0; i < w.count; i++ {
		<-w.stoppedWorkers
	}
}

type Job struct {
	tasks     chan func() error
	completed chan struct{}
	result    chan error
}

func (j *Job) Go(f func() error) {
	j.tasks <- f
}

func (j *Job) Done(f func()) {
	close(j.tasks)
	if f != nil {
		// Callback when completed (useful for tracing)
		go func() {
			<-j.completed
			f()
		}()
	}
}

func (j *Job) Wait() error {
	return <-j.result
}

// If you don't want to block, make sure taskBacklog is greater than all
// possible tasks you'll add.
func (w *Workers) NewJob(taskBacklog int) (*Job, error) {
	w.lock.Lock()
	shouldShutdown := w.shouldShutdown
	w.lock.Unlock()
	if shouldShutdown {
		return nil, ErrShutdown
	}
	j := &Job{
		tasks:     make(chan func() error, taskBacklog),
		completed: make(chan struct{}),
		result:    make(chan error, 1),
	}
	w.queue <- j
	return j, nil
}
