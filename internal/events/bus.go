// Package events provides a tiny in-process publish/subscribe bus used to fan
// build updates out to SSE clients.
package events

import "sync"

// Event is one message delivered to subscribers.
type Event struct {
	ID   uint64 `json:"id"`
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Bus broadcasts events to every current subscriber. Slow subscribers are
// dropped rather than allowed to block build workers.
type Bus struct {
	mu   sync.Mutex
	next uint64
	subs map[chan Event]struct{}
}

// NewBus creates an empty bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a subscriber. The returned cancel function must be
// called to release resources; it is safe to call more than once.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 16
	}
	ch := make(chan Event, buffer)

	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			close(ch)
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish delivers an event to all subscribers without blocking.
func (b *Bus) Publish(eventType string, data any) {
	b.mu.Lock()
	b.next++
	event := Event{ID: b.next, Type: eventType, Data: data}
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
			// Subscriber is too slow; drop this event for that client.
		}
	}
	b.mu.Unlock()
}

// Subscribers reports the current number of connected clients (useful in logs).
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
