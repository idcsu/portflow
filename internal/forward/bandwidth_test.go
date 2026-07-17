package forward

import (
	"errors"
	"testing"
	"time"
)

func TestBandwidthLimiterEnforcesAggregateRate(t *testing.T) {
	limiter := newBandwidthLimiter(80) // 10,000 bytes per second.
	defer limiter.Close()
	started := time.Now()
	if err := limiter.Wait(nil, 8*1024); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	// The initial 4 KiB burst is immediate; the remainder must be paced.
	if elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("unexpected pacing duration %s", elapsed)
	}
}

func TestBandwidthLimiterHotUpdateWakesWaiter(t *testing.T) {
	limiter := newBandwidthLimiter(8)
	defer limiter.Close()
	if err := limiter.Wait(nil, minimumBandwidthBurst); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- limiter.Wait(nil, minimumBandwidthBurst) }()
	time.Sleep(50 * time.Millisecond)
	limiter.Update(8000)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rate update did not wake queued traffic")
	}
}

func TestBandwidthLimiterCloseAndCancellationWakeWaiters(t *testing.T) {
	for _, test := range []struct {
		name string
		wake func(*bandwidthLimiter, chan struct{})
	}{
		{name: "close", wake: func(limiter *bandwidthLimiter, _ chan struct{}) { limiter.Close() }},
		{name: "cancel", wake: func(_ *bandwidthLimiter, done chan struct{}) { close(done) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			limiter := newBandwidthLimiter(8)
			defer limiter.Close()
			if err := limiter.Wait(nil, minimumBandwidthBurst); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			result := make(chan error, 1)
			go func() { result <- limiter.Wait(done, minimumBandwidthBurst) }()
			time.Sleep(20 * time.Millisecond)
			test.wake(limiter, done)
			select {
			case err := <-result:
				if !errors.Is(err, errBandwidthLimiterClosed) {
					t.Fatalf("unexpected error %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked wait was not released")
			}
		})
	}
}
