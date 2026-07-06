package e2b

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// idleTracker bounds a streaming HTTP request without imposing a total deadline.
//
// It mirrors the E2B Python SDK, where the request timeout bounds only the
// initial handshake and the transfer body is governed by a per-chunk idle
// timeout: a slow-but-progressing transfer is allowed, while a stalled one is
// aborted. A single timer cancels the request context when no I/O progress
// happens within the active window. The window starts at the handshake timeout
// and is switched to the body idle window once the response headers arrive.
//
// A zero (or negative) window disables the timeout entirely.
type idleTracker struct {
	cancel context.CancelFunc
	idle   time.Duration // body idle window; <= 0 disables body timeouts

	mu    sync.Mutex
	timer *time.Timer
	done  bool

	fired atomic.Bool // set when this tracker's timer cancelled the request
}

func newIdleTracker(cancel context.CancelFunc, idle time.Duration) *idleTracker {
	return &idleTracker{cancel: cancel, idle: idle}
}

// arm (re)starts the timer with d; d <= 0 stops it (no timeout). Safe to call
// concurrently and after stop (a stopped tracker ignores arming).
func (t *idleTracker) arm(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	if d <= 0 {
		if t.timer != nil {
			t.timer.Stop()
		}
		return
	}
	if t.timer == nil {
		t.timer = time.AfterFunc(d, func() {
			t.fired.Store(true)
			t.cancel()
		})
		return
	}
	t.timer.Reset(d)
}

// reset arms the body idle window; called before a body read/write to start
// timing a wait for I/O progress.
func (t *idleTracker) reset() { t.arm(t.idle) }

// pause stops the timer without ending the tracker, so it can be re-armed on the
// next read. Used to exclude time the caller spends between reads (a slow
// consumer must not trip the wire-only idle timeout).
func (t *idleTracker) pause() { t.arm(0) }

// stop halts the timer permanently. Further arm/reset calls are no-ops.
func (t *idleTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done = true
	if t.timer != nil {
		t.timer.Stop()
	}
}

// timedOut reports whether this tracker's timer (not the caller's context)
// cancelled the request.
func (t *idleTracker) timedOut() bool { return t.fired.Load() }

// wrapRequestBody resets the idle window on each read so a stalled upload trips
// the timer while a progressing one keeps it alive. It wraps the request body
// after http.NewRequestWithContext has inspected the original reader, so
// Content-Length/GetBody detection is preserved. Closing it does not stop the
// tracker — the response body still has to be read.
func (t *idleTracker) wrapRequestBody(rc io.ReadCloser) io.ReadCloser {
	if rc == nil {
		return nil
	}
	return &idleRequestBody{tracker: t, rc: rc}
}

// wrapResponseBody resets the idle window on each read and stops the timer when
// the body is closed, translating a timer-induced cancellation into a timeout
// error.
func (t *idleTracker) wrapResponseBody(rc io.ReadCloser) io.ReadCloser {
	return &idleResponseBody{tracker: t, rc: rc}
}

type idleRequestBody struct {
	tracker *idleTracker
	rc      io.ReadCloser
}

func (r *idleRequestBody) Read(p []byte) (int, error) {
	r.tracker.reset()
	return r.rc.Read(p)
}

func (r *idleRequestBody) Close() error { return r.rc.Close() }

type idleResponseBody struct {
	tracker *idleTracker
	rc      io.ReadCloser
}

func (r *idleResponseBody) Read(p []byte) (int, error) {
	// Time only the wire wait: arm before the read, then pause immediately after
	// so the caller's processing between reads does not count toward the idle
	// timeout (parity with httpx's wire-only read timeout).
	r.tracker.reset()
	n, err := r.rc.Read(p)
	r.tracker.pause()
	if err != nil && err != io.EOF && r.tracker.timedOut() {
		return n, formatRequestTimeout()
	}
	return n, err
}

func (r *idleResponseBody) Close() error {
	err := r.rc.Close()
	r.tracker.stop()
	r.tracker.cancel()
	return err
}
