package agent

import (
	"sync"
	"testing"
)

func TestLogBufferIsBoundedAndAcknowledgesByID(t *testing.T) {
	buffer := NewLogBuffer(2)
	logger := buffer.Logger("forward", nil)
	logger("first")
	logger("retry connection")
	logger("connection failed")
	pending := buffer.Pending(10)
	if len(pending) != 2 || pending[0].Message != "retry connection" || pending[0].Level != "warning" || pending[1].Level != "error" {
		t.Fatalf("unexpected pending logs: %#v", pending)
	}
	buffer.Acknowledge(pending[:1])
	remaining := buffer.Pending(10)
	if len(remaining) != 1 || remaining[0].ID != pending[1].ID {
		t.Fatalf("unexpected remaining logs: %#v", remaining)
	}
}

func TestLogBufferConcurrentWritersRemainBounded(t *testing.T) {
	buffer := NewLogBuffer(100)
	logger := buffer.Logger("agent", nil)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 100; index++ {
				logger("message %d", index)
			}
		}()
	}
	wait.Wait()
	if pending := buffer.Pending(1000); len(pending) != 100 {
		t.Fatalf("buffer length=%d, want 100", len(pending))
	}
}
