package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentLogHeartbeat is a single runtime log entry carried by a heartbeat.
// IDs remain stable while an entry is retried, allowing the control plane to
// safely de-duplicate heartbeats whose responses were lost.
type AgentLogHeartbeat struct {
	ID         string    `json:"id"`
	Level      string    `json:"level"`
	Component  string    `json:"component"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

// LogBuffer is a bounded in-memory queue. It never performs disk or network
// I/O on forwarding goroutines; when full, the oldest entry is discarded.
type LogBuffer struct {
	mu       sync.Mutex
	capacity int
	prefix   string
	sequence uint64
	entries  []AgentLogHeartbeat
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 1000
	}
	random := make([]byte, 8)
	prefix := ""
	if _, err := rand.Read(random); err != nil {
		prefix = fmt.Sprintf("%x", time.Now().UnixNano())
	} else {
		prefix = hex.EncodeToString(random)
	}
	return &LogBuffer{capacity: capacity, prefix: prefix}
}

// Logger mirrors a message to the local service log and adds it to the
// centralized-log queue. The component should be a short stable name such as
// "agent" or "forward".
func (buffer *LogBuffer) Logger(component string, local func(string, ...interface{})) func(string, ...interface{}) {
	component = strings.TrimSpace(component)
	if component == "" {
		component = "agent"
	}
	return func(format string, arguments ...interface{}) {
		if local != nil {
			local(format, arguments...)
		}
		if buffer == nil {
			return
		}
		message := fmt.Sprintf(format, arguments...)
		if len(message) > 2000 {
			message = message[:2000]
		}
		buffer.append(AgentLogHeartbeat{
			ID:    fmt.Sprintf("%s-%d", buffer.prefix, atomic.AddUint64(&buffer.sequence, 1)),
			Level: inferLogLevel(message), Component: component, Message: message, OccurredAt: time.Now().UTC(),
		})
	}
}

func (buffer *LogBuffer) append(entry AgentLogHeartbeat) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(buffer.entries) == buffer.capacity {
		copy(buffer.entries, buffer.entries[1:])
		buffer.entries[len(buffer.entries)-1] = entry
		return
	}
	buffer.entries = append(buffer.entries, entry)
}

func (buffer *LogBuffer) Pending(limit int) []AgentLogHeartbeat {
	if buffer == nil || limit < 1 {
		return nil
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if limit > len(buffer.entries) {
		limit = len(buffer.entries)
	}
	return append([]AgentLogHeartbeat(nil), buffer.entries[:limit]...)
}

func (buffer *LogBuffer) Acknowledge(entries []AgentLogHeartbeat) {
	if buffer == nil || len(entries) == 0 {
		return
	}
	acknowledged := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		acknowledged[entry.ID] = struct{}{}
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	retained := buffer.entries[:0]
	for _, entry := range buffer.entries {
		if _, ok := acknowledged[entry.ID]; !ok {
			retained = append(retained, entry)
		}
	}
	buffer.entries = retained
}

func inferLogLevel(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "failed") || strings.Contains(lower, "failure") || strings.Contains(lower, "error") || strings.Contains(lower, "rejected") {
		return "error"
	}
	if strings.Contains(lower, "warning") || strings.Contains(lower, "retry") || strings.Contains(lower, "dropped") {
		return "warning"
	}
	return "info"
}
