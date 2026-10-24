package server

import (
	"bytes"
	"sync"
)

// Broker fans out server-sent events to every connected browser.
// Slow subscribers drop frames rather than stall the loadgen.
type Broker struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan []byte]struct{}{}} }

func (b *Broker) Subscribe() (ch chan []byte, cancel func()) {
	ch = make(chan []byte, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish formats one SSE frame. Multi-line data is split into data: lines
// as the spec requires, which is what lets us stream HTML fragments to htmx.
func (b *Broker) Publish(event string, data []byte) {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteByte('\n')
	for _, line := range bytes.Split(data, []byte("\n")) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	frame := buf.Bytes()

	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- frame:
		default: // drop for this subscriber
		}
	}
}

func (b *Broker) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
