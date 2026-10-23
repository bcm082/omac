package theme

import "testing"

func TestResolveAndRender(t *testing.T) {
	p := Resolve(ParseColors([]byte(`mode = "dark"
accent = "#7aa2f7"
background = "#1a1b26"
foreground = "#a9b1d6"
red = "#f7768e"
yellow = "#e0af68"
`)), false)
	if p["cursor"] != "#a9b1d6" || p["selection_background"] == "" || p["color1"] != "#f7768e" || p["orange"] != "#e0af68" {
		t.Fatalf("resolve: %v", p)
	}
	if p["dark_background"] != Mix("#1a1b26", "#000000", 0.25) {
		t.Fatalf("derived dark_background %s", p["dark_background"])
	}
	out, unknown := Render("{{ accent }} {{accent_strip}} {{ accent_rgb }} {{ accent_sgr }} {{ mix background accent 50% }} {{ home }} {{.text}} {{ nope }}", p, map[string]string{"home": "/h"})
	want := "#7aa2f7 7aa2f7 122,162,247 38;2;122;162;247 " + Mix("#1a1b26", "#7aa2f7", 0.5) + " /h {{.text}} {{ nope }}"
	if out != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
	if len(unknown) != 1 || unknown[0] != "{{ nope }}" {
		t.Fatalf("unknown: %v", unknown)
	}
}

func TestModeFromLuminance(t *testing.T) {
	if p := Resolve(Palette{"background": "#f2f2f2"}, false); p["mode"] != "light" {
		t.Fatal("expected light")
	}
}
