package wallets

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Card style modes.
const (
	ModeSolid    = "SOLID"
	ModeGradient = "GRADIENT"
	ModeTemplate = "TEMPLATE"
)

// CardStyle is the canonical stored and returned form of a wallet's card style.
type CardStyle struct {
	Mode       string   `json:"mode" enums:"SOLID,GRADIENT,TEMPLATE"`
	Color      string   `json:"color,omitempty" example:"#4480EE"`
	Colors     []string `json:"colors,omitempty"`
	Direction  string   `json:"direction,omitempty" enums:"N,NE,E,SE,S,SW,W,NW"`
	PresetID   *string  `json:"presetId,omitempty"`
	TemplateID string   `json:"templateId,omitempty"`
}

// Preset is a ready-made gradient offered by the app.
type Preset struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Colors    []string `json:"colors"`
	Direction string   `json:"direction"`
}

// Template is a card background image; the images are frontend assets.
type Template struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Presets is the static preset catalog.
var Presets = []Preset{
	{"wallet-color", "Wallet color", []string{"#4480EE", "#0E76B7"}, "SE"},
	{"midnight", "Midnight", []string{"#0F2027", "#2C5364"}, "S"},
	{"ocean", "Ocean", []string{"#2193B0", "#6DD5ED"}, "E"},
	{"aurora", "Aurora", []string{"#00C9FF", "#92FE9D"}, "NE"},
	{"sunset", "Sunset", []string{"#FF512F", "#F09819"}, "SW"},
}

// Templates is the static template catalog.
var Templates = []Template{
	{"classic", "Classic"}, {"carbon", "Carbon"}, {"marble", "Marble"},
	{"paper", "Paper"}, {"waves", "Waves"}, {"grid", "Grid"},
}

// DefaultCardStyle is used when a wallet is created without one.
func DefaultCardStyle() CardStyle { return CardStyle{Mode: ModeSolid, Color: "#4480EE"} }

var (
	hexColor   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	directions = map[string]bool{"N": true, "NE": true, "E": true, "SE": true, "S": true, "SW": true, "W": true, "NW": true}
)

// One struct per mode; ParseCardStyle picks it from the "mode" discriminator.
type styleVariant interface {
	build() (CardStyle, map[string]any)
}

type solidStyle struct {
	Mode  string `json:"mode"`
	Color string `json:"color" validate:"required,max=16"`
}

type gradientStyle struct {
	Mode      string   `json:"mode"`
	Colors    []string `json:"colors" validate:"required,len=2,dive,max=16"`
	Direction string   `json:"direction" validate:"required,max=2"`
	PresetID  *string  `json:"presetId" validate:"omitempty,max=40"`
}

type templateStyle struct {
	Mode       string `json:"mode"`
	TemplateID string `json:"templateId" validate:"required,max=40"`
}

func (s *solidStyle) build() (CardStyle, map[string]any) {
	f := map[string]any{}
	if !hexColor.MatchString(s.Color) {
		f["color"] = "must be a hex color like #4480EE"
	}
	return CardStyle{Mode: ModeSolid, Color: strings.ToUpper(s.Color)}, f
}

func (s *gradientStyle) build() (CardStyle, map[string]any) {
	f := map[string]any{}
	cols := make([]string, len(s.Colors))
	for i, c := range s.Colors {
		if !hexColor.MatchString(c) {
			f["colors"] = "must be two hex colors like #4480EE"
		}
		cols[i] = strings.ToUpper(c)
	}
	if !directions[s.Direction] {
		f["direction"] = "must be one of: N NE E SE S SW W NW"
	}
	if s.PresetID != nil && !presetExists(*s.PresetID) {
		f["presetId"] = "is not a known preset"
	}
	return CardStyle{Mode: ModeGradient, Colors: cols, Direction: s.Direction, PresetID: s.PresetID}, f
}

func (s *templateStyle) build() (CardStyle, map[string]any) {
	f := map[string]any{}
	if !templateExists(s.TemplateID) {
		f["templateId"] = "is not a known template"
	}
	return CardStyle{Mode: ModeTemplate, TemplateID: s.TemplateID}, f
}

func presetExists(id string) bool {
	for _, p := range Presets {
		if p.ID == id {
			return true
		}
	}
	return false
}

func templateExists(id string) bool {
	for _, t := range Templates {
		if t.ID == id {
			return true
		}
	}
	return false
}

// ParseCardStyle decodes and validates a cardStyle JSON object in two passes:
// the "mode" field selects the concrete shape, which is then strictly decoded.
func ParseCardStyle(raw json.RawMessage) (CardStyle, error) {
	v, err := httpx.DecodeDiscriminatedBytes(raw, "mode", map[string]func() styleVariant{
		ModeSolid:    func() styleVariant { return &solidStyle{} },
		ModeGradient: func() styleVariant { return &gradientStyle{} },
		ModeTemplate: func() styleVariant { return &templateStyle{} },
	})
	if err != nil {
		return CardStyle{}, prefixFields(err)
	}
	cs, fields := v.build()
	if len(fields) > 0 {
		return CardStyle{}, apperror.Validation("Validation failed", prefixMap(fields))
	}
	return cs, nil
}

func prefixMap(f map[string]any) map[string]any {
	out := make(map[string]any, len(f))
	for k, v := range f {
		out["cardStyle."+k] = v
	}
	return out
}

func prefixFields(err error) error {
	ae, ok := apperror.As(err)
	if !ok || ae.Code != apperror.CodeValidationFailed {
		return err
	}
	if len(ae.Details) == 0 {
		return apperror.Validation("Validation failed", map[string]any{"cardStyle": ae.Message})
	}
	return apperror.Validation("Validation failed", prefixMap(ae.Details))
}
