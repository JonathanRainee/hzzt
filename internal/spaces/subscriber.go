package spaces

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/platform/events"
)

// DefaultSpaceName builds "{firstName}'s Space", keeping within the 40-char limit.
func DefaultSpaceName(fullName string) string {
	first := "My"
	if f := strings.Fields(fullName); len(f) > 0 {
		first = f[0]
	}
	const suffix = "'s Space"
	for utf8.RuneCountInString(first)+utf8.RuneCountInString(suffix) > 40 {
		_, size := utf8.DecodeLastRuneInString(first)
		first = first[:len(first)-size]
	}
	return first + suffix
}

// Subscribe wires the service to domain events: a new user gets a default space.
func (s *Service) Subscribe(bus *events.Bus) {
	events.On(bus, func(ctx context.Context, e auth.UserRegistered) error {
		_, err := s.Create(ctx, e.UserID, CreateInput{Name: DefaultSpaceName(e.Name)}, true)
		return err
	})
}
