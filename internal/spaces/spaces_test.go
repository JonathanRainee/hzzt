package spaces

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewInviteCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := NewInviteCode()
		require.NoError(t, err)
		require.Len(t, c, 8)
		assert.Equal(t, strings.ToUpper(c), c)
		assert.False(t, strings.ContainsAny(c, "01OI"), c)
		seen[c] = true
	}
	assert.Greater(t, len(seen), 1990, "codes should be effectively unique")
}

func TestDefaultSpaceName(t *testing.T) {
	cases := map[string]string{
		"Jane":         "Jane's Space",
		"  Jane  Doe ": "Jane's Space",
		"":             "My's Space",
		"日本語":          "日本語's Space",
	}
	for in, want := range cases {
		assert.Equal(t, want, DefaultSpaceName(in), in)
	}
	long := DefaultSpaceName(strings.Repeat("x", 60))
	assert.LessOrEqual(t, utf8.RuneCountInString(long), 40)
	assert.True(t, strings.HasSuffix(long, "'s Space"))
}
