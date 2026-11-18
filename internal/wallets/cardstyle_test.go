package wallets

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

func fieldsOf(t *testing.T, err error) map[string]any {
	t.Helper()
	ae, ok := apperror.As(err)
	require.True(t, ok, "want an AppError, got %v", err)
	assert.Equal(t, apperror.CodeValidationFailed, ae.Code)
	return ae.Details
}

func TestParseCardStyleValid(t *testing.T) {
	cases := map[string]struct {
		in   string
		want CardStyle
	}{
		"solid, color upper-cased": {`{"mode":"SOLID","color":"#4480ee"}`, CardStyle{Mode: "SOLID", Color: "#4480EE"}},
		"gradient": {`{"mode":"GRADIENT","colors":["#4480EE","#0e76b7"],"direction":"SE"}`,
			CardStyle{Mode: "GRADIENT", Colors: []string{"#4480EE", "#0E76B7"}, Direction: "SE"}},
		"gradient with preset": {`{"mode":"GRADIENT","colors":["#4480EE","#0E76B7"],"direction":"N","presetId":"ocean"}`,
			CardStyle{Mode: "GRADIENT", Colors: []string{"#4480EE", "#0E76B7"}, Direction: "N", PresetID: strPtr("ocean")}},
		"template": {`{"mode":"TEMPLATE","templateId":"carbon"}`, CardStyle{Mode: "TEMPLATE", TemplateID: "carbon"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCardStyle(json.RawMessage(c.in))
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func strPtr(s string) *string { return &s }

func TestParseCardStyleInvalid(t *testing.T) {
	cases := map[string]struct{ in, field string }{
		"missing mode":        {`{"color":"#4480EE"}`, "cardStyle.mode"},
		"unknown mode":        {`{"mode":"PLAID"}`, "cardStyle.mode"},
		"not an object":       {`"red"`, "cardStyle"},
		"solid bad color":     {`{"mode":"SOLID","color":"red"}`, "cardStyle.color"},
		"solid short hex":     {`{"mode":"SOLID","color":"#fff"}`, "cardStyle.color"},
		"solid no color":      {`{"mode":"SOLID"}`, "cardStyle.color"},
		"solid extra field":   {`{"mode":"SOLID","color":"#4480EE","direction":"N"}`, "cardStyle"},
		"gradient one color":  {`{"mode":"GRADIENT","colors":["#4480EE"],"direction":"N"}`, "cardStyle.colors"},
		"gradient three":      {`{"mode":"GRADIENT","colors":["#4480EE","#4480EE","#4480EE"],"direction":"N"}`, "cardStyle.colors"},
		"gradient bad color":  {`{"mode":"GRADIENT","colors":["#4480EE","nope"],"direction":"N"}`, "cardStyle.colors"},
		"gradient bad dir":    {`{"mode":"GRADIENT","colors":["#4480EE","#0E76B7"],"direction":"UP"}`, "cardStyle.direction"},
		"gradient no dir":     {`{"mode":"GRADIENT","colors":["#4480EE","#0E76B7"]}`, "cardStyle.direction"},
		"gradient bad preset": {`{"mode":"GRADIENT","colors":["#4480EE","#0E76B7"],"direction":"N","presetId":"nope"}`, "cardStyle.presetId"},
		"template unknown id": {`{"mode":"TEMPLATE","templateId":"nope"}`, "cardStyle.templateId"},
		"template missing id": {`{"mode":"TEMPLATE"}`, "cardStyle.templateId"},
		"template with color": {`{"mode":"TEMPLATE","templateId":"carbon","color":"#4480EE"}`, "cardStyle"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCardStyle(json.RawMessage(c.in))
			require.Error(t, err)
			ae, ok := apperror.As(err)
			require.True(t, ok)
			assert.Equal(t, apperror.CodeValidationFailed, ae.Code)
			if c.field != "cardStyle" {
				assert.Contains(t, fieldsOf(t, err), c.field)
			}
		})
	}
}

func TestCatalogsAreConsistent(t *testing.T) {
	assert.Equal(t, CardStyle{Mode: "SOLID", Color: "#4480EE"}, DefaultCardStyle())
	seen := map[string]bool{}
	for _, p := range Presets {
		assert.False(t, seen[p.ID], p.ID)
		seen[p.ID] = true
		assert.Len(t, p.Colors, 2)
		assert.True(t, directions[p.Direction], p.ID)
		for _, c := range p.Colors {
			assert.Regexp(t, hexColor, c)
		}
		// every preset is accepted as a gradient
		b, _ := json.Marshal(CardStyle{Mode: "GRADIENT", Colors: p.Colors, Direction: p.Direction, PresetID: strPtr(p.ID)})
		_, err := ParseCardStyle(b)
		assert.NoError(t, err, p.ID)
	}
	for _, tp := range Templates {
		b, _ := json.Marshal(CardStyle{Mode: "TEMPLATE", TemplateID: tp.ID})
		_, err := ParseCardStyle(b)
		assert.NoError(t, err, tp.ID)
	}
}
