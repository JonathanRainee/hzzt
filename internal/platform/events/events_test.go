package events

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type ping struct{ N int }

func (ping) EventName() string { return "test.ping" }

func TestPublishDeliversToAllHandlersEvenIfOneFails(t *testing.T) {
	b := NewBus(zerolog.Nop())
	var got []int
	On(b, func(_ context.Context, e ping) error { return errors.New("boom") })
	On(b, func(_ context.Context, e ping) error { got = append(got, e.N); return nil })

	b.Publish(context.Background(), ping{N: 7})
	assert.Equal(t, []int{7}, got)
}

func TestPublishWithoutSubscribersIsNoop(t *testing.T) {
	NewBus(zerolog.Nop()).Publish(context.Background(), ping{})
}
