package theme

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Palette maps Omarchy color names (background, accent, bright_red, …) to values.
type Palette map[string]string

var (
	keyChars   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	valueChars = regexp.MustCompile(`^[A-Za-z0-9#(),._+/% -]*$`)
	hexColor   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// ParseColors reads a colors.toml the same forgiving way omarchy-theme-color does.
func ParseColors(data []byte) Palette {
	p := Palette{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.NewReplacer(`"`, "", `'`, "", " ", "", "\t", "").Replace(key)
		if key == "" || strings.HasPrefix(key, "#") || !keyChars.MatchString(key) {
			continue
		}
		if i := strings.IndexAny(value, `"'`); i >= 0 {
			value = value[i+1:]
			if j := strings.IndexAny(value, `"'`); j >= 0 {
				value = value[:j]
			}
		} else {
			value = strings.TrimSpace(value)
		}
		if !valueChars.MatchString(value) {
			continue
		}
		p[key] = value
	}
	return p
}

// Resolve fills in aliases and derived shades, mirroring omarchy-theme-color
// so templates render exactly as they do on Omarchy.
//
// Ported from Omarchy's bin/omarchy-theme-color and the mix function in
// bin/omarchy-theme-set-templates (https://github.com/basecamp/omarchy),
// Copyright (c) Basecamp, used under the MIT License.
func Resolve(raw Palette, lightModeFile bool) Palette {
	c := Palette{}
	for k, v := range raw {
		c[k] = v
	}
	alias := func(key, fallback string) {
		if c[key] == "" {
			c[key] = c[fallback]
		}
	}
	or := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}

	legacyPalette := map[string]string{
		"background": "bg", "dark_background": "dark_bg", "darker_background": "darker_bg",
		"lighter_background": "lighter_bg", "foreground": "fg", "dark_foreground": "dark_fg",
		"light_foreground": "light_fg", "bright_foreground": "bright_fg",
	}
	for k, v := range legacyPalette {
		alias(k, v)
	}

	alias("background", "color0")
	alias("foreground", "color7")
	if c["background"] != "" {
		c["color0"] = c["background"]
	}
	if c["foreground"] != "" {
		c["color7"] = c["foreground"]
	}

	for k, v := range map[string]string{
		"red": "color1", "green": "color2", "yellow": "color3", "blue": "color4",
		"magenta": "color5", "cyan": "color6", "bright_red": "color9", "bright_green": "color10",
		"bright_yellow": "color11", "bright_blue": "color12", "bright_magenta": "color13", "bright_cyan": "color14",
	} {
		alias(k, v)
	}
	alias("magenta", "purple")
	alias("bright_magenta", "bright_purple")

	if c["light_foreground"] == "" {
		c["light_foreground"] = or(c["color7"], c["foreground"])
	}
	if c["bright_foreground"] == "" {
		c["bright_foreground"] = or(c["color15"], c["foreground"])
	}
	c["cursor"] = c["bright_foreground"]
	if c["lighter_background"] == "" {
		c["lighter_background"] = or(c["color0"], c["background"])
	}
	if c["dark_foreground"] == "" {
		c["dark_foreground"] = or(c["color8"], c["foreground"])
	}
	if c["muted"] == "" {
		c["muted"] = or(c["color8"], c["dark_foreground"])
	}
	if c["selection"] == "" {
		c["selection"] = or(c["selection_background"], c["color8"], c["color0"], c["background"])
	}
	alias("selection_background", "selection")
	alias("selection_foreground", "bright_foreground")
	alias("orange", "yellow")

	derive := func(key, from, with string, amount float64) {
		if c[key] == "" && hexColor.MatchString(c[from]) {
			c[key] = Mix(c[from], with, amount)
		}
	}
	derive("brown", "orange", "#000000", 0.5)
	derive("dark_background", "background", "#000000", 0.25)
	derive("darker_background", "background", "#000000", 0.5)
	for _, k := range []string{"red", "yellow", "green", "cyan", "blue", "magenta"} {
		derive("bright_"+k, k, "#ffffff", 0.2)
	}
	alias("purple", "magenta")
	alias("bright_purple", "bright_magenta")

	ansi := []string{"background", "red", "green", "yellow", "blue", "magenta", "cyan", "foreground",
		"muted", "bright_red", "bright_green", "bright_yellow", "bright_blue", "bright_magenta", "bright_cyan", "bright_foreground"}
	for i, k := range ansi {
		alias(fmt.Sprintf("color%d", i), k)
	}
	for k, v := range legacyPalette {
		if c[k] != "" {
			c[v] = c[k]
		}
	}

	if c["mode"] == "" {
		c["mode"] = c["theme_type"]
	}
	if c["mode"] == "" {
		switch {
		case lightModeFile:
			c["mode"] = "light"
		case hexColor.MatchString(c["background"]):
			r, g, b := rgb(c["background"])
			if r+g+b > 382 {
				c["mode"] = "light"
			} else {
				c["mode"] = "dark"
			}
		default:
			c["mode"] = "dark"
		}
	}
	c["theme_type"] = c["mode"]
	return c
}

func rgb(hex string) (int, int, int) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#")[:6], 16, 32)
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

// Mix blends a toward b by amount (0..1), rounding like Omarchy's awk.
func Mix(a, b string, amount float64) string {
	amount = math.Max(0, math.Min(1, amount))
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	ch := func(x, y int) int { return int(float64(x)*(1-amount) + float64(y)*amount + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", ch(ar, br), ch(ag, bg), ch(ab, bb))
}

// parseAmount accepts 35%, 0.35 or 35 (treated as a percentage when > 1).
func parseAmount(s string) (float64, bool) {
	pct := strings.HasSuffix(s, "%")
	f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	if err != nil {
		return 0, false
	}
	if pct || f > 1 {
		f /= 100
	}
	return f, true
}
