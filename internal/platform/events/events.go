// Package events is a tiny synchronous in-process pub/sub used to decouple
// domain packages (e.g. "user registered" → "create default space").
package events

import (
	"context"
	"sync"

	"github.com/rs/zerolog"
)

// Event is anything publishable. Name is the topic, e.g. "user.registered".
type Event interface{ EventName() string }

// Handler processes an event. Errors are logged, never returned to the publisher.
type Handler func(ctx context.Context, e Event) error

// Bus dispatches events to subscribers in registration order.
type Bus struct {
	log      zerolog.Logger
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// NewBus creates an empty bus.
func NewBus(log zerolog.Logger) *Bus {
	return &Bus{log: log, handlers: map[string][]Handler{}}
}

// Subscribe registers h for the named event.
func (b *Bus) Subscribe(name string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[name] = append(b.handlers[name], h)
}

// Publish runs every handler for e synchronously. A failing handler is
// logged and does not stop the others or fail the caller.
func (b *Bus) Publish(ctx context.Context, e Event) {
	b.mu.RLock()
	hs := append([]Handler(nil), b.handlers[e.EventName()]...)
	b.mu.RUnlock()
	for _, h := range hs {
		if err := h(ctx, e); err != nil {
			b.log.Error().Err(err).Str("event", e.EventName()).Msg("event handler failed")
		}
	}
}

// On subscribes a typed handler; events of other types are ignored.
func On[T Event](b *Bus, h func(ctx context.Context, e T) error) {
	var zero T
	b.Subscribe(zero.EventName(), func(ctx context.Context, e Event) error {
		if te, ok := e.(T); ok {
			return h(ctx, te)
		}
		return nil
	})
}
