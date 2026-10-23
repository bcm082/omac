package theme

import (
	"fmt"
	"regexp"
	"strings"
)

var tokenRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// Render substitutes Omarchy template tokens:
//
//	{{ key }} {{ key_strip }} {{ key_rgb }} {{ mix[_strip|_rgb] a b 35% }}
//	{{ gradient_start key [fallback] }} {{ shell_gradient key [fallback] }}
//
// plus omac's {{ key_sgr }} ("38;2;r;g;b") and any extra vars (home,
// theme_name, …). Unknown tokens are left untouched and reported, so a
// foreign syntax such as glamour's {{.text}} passes through.
func Render(tpl string, p Palette, vars map[string]string) (string, []string) {
	var unknown []string
	out := tokenRe.ReplaceAllStringFunc(tpl, func(tok string) string {
		inner := tokenRe.FindStringSubmatch(tok)[1]
		if v, ok := resolveToken(inner, p, vars); ok {
			return v
		}
		if !strings.HasPrefix(inner, ".") {
			unknown = append(unknown, tok)
		}
		return tok
	})
	return out, unknown
}

func resolveToken(inner string, p Palette, vars map[string]string) (string, bool) {
	f := strings.Fields(inner)
	if len(f) == 0 {
		return "", false
	}
	switch f[0] {
	case "mix", "mix_strip", "mix_rgb":
		if len(f) != 4 || !hexColor.MatchString(p[f[1]]) || !hexColor.MatchString(p[f[2]]) {
			return "", false
		}
		amt, ok := parseAmount(f[3])
		if !ok {
			return "", false
		}
		return format(Mix(p[f[1]], p[f[2]], amt), strings.TrimPrefix(f[0], "mix"))
	case "gradient_start", "shell_gradient", "hypr_gradient":
		if len(f) < 2 {
			return "", false
		}
		spec := p[f[1]]
		if spec == "" && len(f) > 2 {
			spec = p[f[2]]
			if spec == "" {
				spec = f[2]
			}
		}
		var colors []string
		for _, part := range strings.Fields(spec) {
			if strings.HasSuffix(part, "deg") {
				continue
			}
			if v, ok := p[part]; ok {
				part = v
			}
			colors = append(colors, part)
		}
		if len(colors) == 0 {
			return spec, true
		}
		if f[0] == "gradient_start" {
			return colors[0], true
		}
		return strings.Join(colors, " "), true
	}
	if len(f) != 1 {
		return "", false
	}
	key := f[0]
	if v, ok := vars[key]; ok {
		return v, true
	}
	if v, ok := p[key]; ok {
		return v, true
	}
	for _, suffix := range []string{"_strip", "_rgb", "_sgr"} {
		if base, ok := strings.CutSuffix(key, suffix); ok {
			if v, ok := p[base]; ok {
				return format(v, suffix)
			}
		}
	}
	return "", false
}

func format(v, suffix string) (string, bool) {
	switch suffix {
	case "":
		return v, true
	case "_strip":
		return strings.TrimPrefix(v, "#"), true
	case "_rgb", "_sgr":
		if !hexColor.MatchString(v) {
			return "", false
		}
		r, g, b := rgb(v)
		if suffix == "_sgr" {
			return fmt.Sprintf("38;2;%d;%d;%d", r, g, b), true
		}
		return fmt.Sprintf("%d,%d,%d", r, g, b), true
	}
	return "", false
}

// RenderValue renders every string inside a decoded TOML value.
func RenderValue(v any, p Palette, vars map[string]string) (any, []string) {
	var unknown []string
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case string:
			s, u := Render(t, p, vars)
			unknown = append(unknown, u...)
			return s
		case []any:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = walk(e)
			}
			return out
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				out[k] = walk(e)
			}
			return out
		}
		return v
	}
	return walk(v), unknown
}
