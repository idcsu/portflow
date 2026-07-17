package forward

import (
	"errors"
	"sync"
	"time"
)

const (
	minimumBandwidthBurst = 4 * 1024
	maximumBandwidthBurst = 256 * 1024
)

var errBandwidthLimiterClosed = errors.New("bandwidth limiter is closed")

// bandwidthLimiter is shared by every protocol, connection, and direction of a
// rule. Wait calls are serialized so the configured rate is an aggregate rule
// limit rather than a per-connection limit.
type bandwidthLimiter struct {
	queue sync.Mutex
	mu    sync.Mutex

	kbps       uint64
	rate       float64
	capacity   float64
	tokens     float64
	lastRefill time.Time
	notify     chan struct{}
	closed     bool
}

func newBandwidthLimiter(kbps uint64) *bandwidthLimiter {
	limiter := &bandwidthLimiter{notify: make(chan struct{}), lastRefill: time.Now()}
	limiter.updateLocked(kbps)
	limiter.tokens = limiter.capacity
	return limiter
}

func (limiter *bandwidthLimiter) Update(kbps uint64) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.closed || limiter.kbps == kbps {
		return
	}
	limiter.refillLocked(time.Now())
	limiter.updateLocked(kbps)
	if limiter.tokens > limiter.capacity {
		limiter.tokens = limiter.capacity
	}
	close(limiter.notify)
	limiter.notify = make(chan struct{})
}

func (limiter *bandwidthLimiter) Close() {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.closed {
		return
	}
	limiter.closed = true
	close(limiter.notify)
}

func (limiter *bandwidthLimiter) Wait(done <-chan struct{}, byteCount int) error {
	if limiter == nil || byteCount <= 0 {
		return nil
	}
	limiter.queue.Lock()
	defer limiter.queue.Unlock()

	remaining := float64(byteCount)
	for remaining > 0 {
		limiter.mu.Lock()
		if limiter.closed {
			limiter.mu.Unlock()
			return errBandwidthLimiterClosed
		}
		limiter.refillLocked(time.Now())
		if limiter.rate == 0 {
			limiter.mu.Unlock()
			return nil
		}
		if limiter.tokens > 0 {
			take := limiter.tokens
			if take > remaining {
				take = remaining
			}
			limiter.tokens -= take
			remaining -= take
		}
		if remaining == 0 {
			limiter.mu.Unlock()
			return nil
		}
		amount := remaining
		if amount > limiter.capacity {
			amount = limiter.capacity
		}
		wait := time.Duration(amount / limiter.rate * float64(time.Second))
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		notify := limiter.notify
		limiter.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-notify:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return errBandwidthLimiterClosed
		}
	}
	return nil
}

func (limiter *bandwidthLimiter) rateKbps() uint64 {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.kbps
}

func (limiter *bandwidthLimiter) maxChunkSize(byteCount int) int {
	if limiter == nil || byteCount <= 0 {
		return byteCount
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.rate == 0 || float64(byteCount) <= limiter.capacity {
		return byteCount
	}
	return int(limiter.capacity)
}

func (limiter *bandwidthLimiter) refillLocked(now time.Time) {
	if limiter.rate > 0 && now.After(limiter.lastRefill) {
		limiter.tokens += now.Sub(limiter.lastRefill).Seconds() * limiter.rate
		if limiter.tokens > limiter.capacity {
			limiter.tokens = limiter.capacity
		}
	}
	limiter.lastRefill = now
}

func (limiter *bandwidthLimiter) updateLocked(kbps uint64) {
	limiter.kbps = kbps
	limiter.rate = float64(kbps) * 1000 / 8
	limiter.capacity = limiter.rate / 4
	if limiter.capacity < minimumBandwidthBurst {
		limiter.capacity = minimumBandwidthBurst
	}
	if limiter.capacity > maximumBandwidthBurst {
		limiter.capacity = maximumBandwidthBurst
	}
}
