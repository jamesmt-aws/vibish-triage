package pipeline

import (
	"log/slog"
	"sync"
)

// cwndController implements TCP-style slow start and congestion avoidance
// for controlling the number of in-flight LLM requests.
type cwndController struct {
	mu        sync.Mutex
	cwnd      int  // current window size (max in-flight)
	ssthresh  int  // slow-start threshold; above this, grow linearly
	successes int  // consecutive successes in current window
	maxCwnd   int  // ceiling
	sem       chan struct{}
}

func newCwndController(initial, max int) *cwndController {
	c := &cwndController{
		cwnd:     initial,
		ssthresh: max, // start in slow-start phase
		maxCwnd:  max,
		sem:      make(chan struct{}, max),
	}
	// Pre-fill semaphore to initial window
	for range initial {
		c.sem <- struct{}{}
	}
	return c
}

// acquire blocks until a slot is available.
func (c *cwndController) acquire() {
	<-c.sem
}

// onSuccess records a successful request and potentially grows the window.
func (c *cwndController) onSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.successes++

	if c.cwnd < c.ssthresh {
		// Slow-start: double window every cwnd successes (exponential growth)
		if c.successes >= c.cwnd {
			c.successes = 0
			newCwnd := min(c.cwnd*2, c.maxCwnd)
			c.grow(newCwnd)
		}
	} else {
		// Congestion avoidance: grow by 1 every cwnd successes (linear growth)
		if c.successes >= c.cwnd {
			c.successes = 0
			newCwnd := min(c.cwnd+1, c.maxCwnd)
			c.grow(newCwnd)
		}
	}

	// Return the slot
	c.sem <- struct{}{}
}

// onThrottle records a throttle and halves the window.
func (c *cwndController) onThrottle() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ssthresh = max(c.cwnd/2, 1)
	newCwnd := max(c.cwnd/2, 1)
	c.successes = 0

	if newCwnd < c.cwnd {
		// Shrink: drain excess tokens from semaphore
		drain := c.cwnd - newCwnd
		for range drain {
			select {
			case <-c.sem:
			default:
				// Token is in-flight, it won't be returned (caller calls onSuccess/onThrottle instead)
			}
		}
		c.cwnd = newCwnd
		slog.Info("cwnd shrink", "cwnd", c.cwnd, "ssthresh", c.ssthresh)
	}

	// Return this slot
	c.sem <- struct{}{}
}

// grow increases cwnd, adding new tokens to the semaphore.
func (c *cwndController) grow(newCwnd int) {
	if newCwnd <= c.cwnd {
		return
	}
	added := newCwnd - c.cwnd
	c.cwnd = newCwnd
	for range added {
		select {
		case c.sem <- struct{}{}:
		default:
		}
	}
	slog.Info("cwnd grow", "cwnd", c.cwnd, "ssthresh", c.ssthresh)
}
