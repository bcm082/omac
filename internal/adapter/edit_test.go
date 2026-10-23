package adapter

import (
	"strings"
	"testing"
)

func TestSetTOMLTopLevelSkipsMultilineStrings(t *testing.T) {
	src := `"$schema" = 'x'

format = """
[](fg:#363a4d)\
[os]
$character"""

add_newline = true

# OS segment
[os]
disabled = false
`
	got := setTOML(src, "palette", "omarchy")
	want := `"$schema" = 'x'

format = """
[](fg:#363a4d)\
[os]
$character"""

add_newline = true
palette = "omarchy"

# OS segment
[os]
disabled = false
`
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	// Setting it again replaces in place.
	if again := setTOML(got, "palette", "other"); strings.Count(again, "palette =") != 1 || !strings.Contains(again, `palette = "other"`) {
		t.Fatalf("replace failed:\n%s", again)
	}
}

func TestSetTOMLTables(t *testing.T) {
	src := "[theme]\n# [theme.custom]\nname = \"catppuccin\"\n\n[terminal]\nx = 1\n"
	got := setTOML(src, "theme.name", "terminal")
	if !strings.Contains(got, "name = \"terminal\"") || strings.Contains(got, "catppuccin\"") {
		t.Fatalf("in-table replace failed:\n%s", got)
	}
	got = setTOML(got, "theme.custom.accent", "#fff")
	if !strings.HasSuffix(got, "[theme.custom]\naccent = \"#fff\"\n") {
		t.Fatalf("new table append failed:\n%s", got)
	}
	got = setTOML(got, "theme.custom.red", "#f00")
	if !strings.Contains(got, "accent = \"#fff\"\nred = \"#f00\"") {
		t.Fatalf("insert into existing table failed:\n%s", got)
	}
	got = setTOML(got, "terminal.y", int64(2))
	if !strings.Contains(got, "x = 1\ny = 2\n") {
		t.Fatalf("insert in middle table failed:\n%s", got)
	}
	if got := setTOML("", "tui.theme", "omarchy"); got != "[tui]\ntheme = \"omarchy\"\n" {
		t.Fatalf("empty file: %q", got)
	}
	if got := setTOML("color_theme = \"Default\"\ntheme_background = true\n", "color_theme", "omarchy"); got != "color_theme = \"omarchy\"\ntheme_background = true\n" {
		t.Fatalf("btop-style: %q", got)
	}
}

func TestSetJSONKeepsCommentsAndOrder(t *testing.T) {
	src := "// Zed settings\n{\n  // font\n  \"buffer_font_size\": 15,\n  \"theme\": \"One Dark\"\n}\n"
	out, err := setJSON([]byte(src), []string{"theme", "dark"}, "Omarchy")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "// Zed settings") || !strings.Contains(s, "// font") || !strings.Contains(s, `"dark": "Omarchy"`) {
		t.Fatalf("got:\n%s", s)
	}

	plain := "{\n  \"theme\": \"dark\",\n  \"model\": \"x\"\n}\n"
	out, err = setJSON([]byte(plain), []string{"theme"}, "custom:omarchy")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "{\n  \"theme\": \"custom:omarchy\",\n  \"model\": \"x\"\n}\n" {
		t.Fatalf("plain json: %q", out)
	}

	out, err = setJSON(nil, []string{"display", "color", "keys"}, "38;2;1;2;3")
	if err != nil || !strings.Contains(string(out), `"keys": "38;2;1;2;3"`) {
		t.Fatalf("new file: %q %v", out, err)
	}
}

func TestSetYAML(t *testing.T) {
	src := "# style name or JSON path\nstyle: \"auto\"\nmouse: false\n"
	out, err := setYAML([]byte(src), []string{"style"}, "/x/glow.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# style name") || !strings.Contains(string(out), "style: /x/glow.json") || !strings.Contains(string(out), "mouse: false") {
		t.Fatalf("got:\n%s", out)
	}
	out, err = setYAML(nil, []string{"gui", "theme", "activeBorderColor"}, []any{"#fff", "bold"})
	if err != nil || !strings.Contains(string(out), "activeBorderColor:\n      - '#fff'\n      - bold") {
		t.Fatalf("got:\n%s %v", out, err)
	}
}

func TestSetKVAndBlock(t *testing.T) {
	src := "font-size = 14\ntheme = dark:Nord,light:Catppuccin Latte\n"
	if got := setKV(src, "theme", "omarchy"); got != "font-size = 14\ntheme = omarchy\n" {
		t.Fatalf("kv: %q", got)
	}
	b := setBlock("set -g mouse on\n", "theme", "source-file x", "", "#")
	b2 := setBlock(b, "theme", "source-file y", "", "#")
	if strings.Count(b2, "omac:theme (managed") != 1 || !strings.Contains(b2, "source-file y") || strings.Contains(b2, "source-file x") {
		t.Fatalf("block:\n%s", b2)
	}
}

func TestSetJSONCLayout(t *testing.T) {
	src := "// c\n{\n  \"a\": 1,\n  \"theme\": {\n    \"mode\": \"dark\",\n    \"dark\": \"One Dark\"\n  }\n}\n"
	out, err := setJSON([]byte(src), []string{"theme", "light"}, "Omarchy")
	if err != nil {
		t.Fatal(err)
	}
	want := "// c\n{\n  \"a\": 1,\n  \"theme\": {\n    \"mode\": \"dark\",\n    \"dark\": \"One Dark\",\n    \"light\": \"Omarchy\"\n  }\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
	out, err = setJSON(out, []string{"ui", "custom", "x"}, map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	want = "// c\n{\n  \"a\": 1,\n  \"theme\": {\n    \"mode\": \"dark\",\n    \"dark\": \"One Dark\",\n    \"light\": \"Omarchy\"\n  },\n  \"ui\": {\n    \"custom\": {\n      \"x\": {\n        \"k\": \"v\"\n      }\n    }\n  }\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestSetBlockIdempotentMidFile(t *testing.T) {
	src := "a\n\nrun tpm\n"
	once := setBlock(src, "theme", "source-file x", "", "#")
	once += "after\n"
	if twice := setBlock(once, "theme", "source-file x", "", "#"); twice != once {
		t.Fatalf("not idempotent:\n%q\n%q", once, twice)
	}
}
