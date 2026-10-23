// Package scan finds installed apps that look themeable but have no adapter yet.
package scan

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"omac/internal/adapter"
	"omac/internal/xdg"
)

type Candidate struct {
	App      string
	Dir      string   // config directory or file
	Files    []string // files with evidence
	Evidence []string // "file:line: snippet"
	Score    int
	Brew     bool // also installed via Homebrew
}

var (
	themeKey  = regexp.MustCompile(`(?i)(^|[\s"'{,])(theme|color_?scheme|colou?rs?|palette|flavou?r|syntax_theme|accent)["']?\s*[:=]`)
	hexRe     = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	configExt = map[string]bool{".toml": true, ".json": true, ".jsonc": true, ".yml": true, ".yaml": true,
		".conf": true, ".ini": true, ".cfg": true, ".lua": true, ".kdl": true, ".rc": true, "": true}
	themeDirs = map[string]bool{"themes": true, "theme": true, "colors": true, "colorschemes": true, "flavors": true, "skins": true}
	// Never read these: they hold credentials or are huge caches.
	skipName = regexp.MustCompile(`(?i)(auth|credential|token|secret|oauth|cookie|session|history|\.log$|lock)`)
	skipApp  = regexp.MustCompile(`(?i)^(ssh|gnupg|aws|azure|kube|docker|password-store|netrc|npmrc|pypirc|git-credentials|1password|bitwarden|keychain.*|\.?omac|configstore|git|gh|\.?cache|.*\.(bak|backup|old)|com\.apple\..*|google|crashreporter|mobilesync|addressbook|callhistory.*|knowledge|caches)$`)
)

// Discover looks through ~/.config, ~/Library/Application Support,
// ~/Library/Preferences/<app>/ and ~/.<app> for config files that mention
// theme-ish keys or carry many hex colors, skipping apps that already have
// an adapter.
func Discover(adapters []*adapter.Adapter) []Candidate {
	owned := ownedApps(adapters)
	brew := brewList()
	found := map[string]*Candidate{}

	add := func(app, dir string) {
		key := strings.ToLower(strings.TrimPrefix(app, "."))
		if key == "" || owned[key] || skipApp.MatchString(key) {
			return
		}
		c := inspect(dir)
		if c == nil {
			return
		}
		c.App = strings.TrimPrefix(app, ".")
		c.Brew = brew[key]
		if prev, ok := found[key]; !ok || prev.Score < c.Score {
			found[key] = c
		}
	}

	home := xdg.Home()
	for _, root := range []string{filepath.Join(home, ".config"), filepath.Join(home, "Library", "Application Support"), filepath.Join(home, "Library", "Preferences")} {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.IsDir() || root == filepath.Join(home, ".config") {
				add(appName(e.Name()), filepath.Join(root, e.Name()))
			}
		}
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, ".") || n == ".config" || n == ".local" || n == ".cache" || n == "." || n == ".." {
			continue
		}
		add(appName(n), filepath.Join(home, n))
	}

	var out []Candidate
	for _, c := range found {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].App < out[j].App
	})
	return out
}

// appName turns ".tmux.conf" into "tmux" and "starship.toml" into "starship".
func appName(n string) string {
	n = strings.TrimPrefix(n, ".")
	for _, suf := range []string{".conf", ".toml", ".json", ".yml", ".yaml", "rc"} {
		if s, ok := strings.CutSuffix(n, suf); ok && s != "" {
			return s
		}
	}
	return n
}

func ownedApps(adapters []*adapter.Adapter) map[string]bool {
	owned := map[string]bool{}
	home := xdg.Home()
	for _, a := range adapters {
		owned[strings.ToLower(a.Name)] = true
		for _, b := range a.Detect.Bins {
			owned[strings.ToLower(b)] = true
		}
		for _, p := range a.OwnedPaths() {
			rel, err := filepath.Rel(home, p)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			parts := strings.Split(rel, string(filepath.Separator))
			switch {
			case len(parts) >= 2 && parts[0] == ".config":
				owned[strings.ToLower(appName(parts[1]))] = true
			case len(parts) >= 3 && parts[0] == "Library":
				owned[strings.ToLower(parts[2])] = true
			case len(parts) >= 1:
				owned[strings.ToLower(appName(parts[0]))] = true
			}
		}
	}
	return owned
}

// inspect scores a directory (two levels deep) or a single file.
func inspect(path string) *Candidate {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	c := &Candidate{Dir: path}
	if !fi.IsDir() {
		inspectFile(c, path)
	} else {
		files := 0
		filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			depth := strings.Count(strings.TrimPrefix(p, path), string(filepath.Separator))
			if d.IsDir() {
				if p != path && themeDirs[strings.ToLower(d.Name())] {
					c.Score += 3
					c.Evidence = append(c.Evidence, xdg.Tilde(p)+"/  (theme folder)")
				}
				if depth >= 2 || strings.HasPrefix(d.Name(), ".") && p != path || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if files > 40 {
				return filepath.SkipAll
			}
			files++
			inspectFile(c, p)
			return nil
		})
	}
	if c.Score < 2 {
		return nil
	}
	return c
}

func inspectFile(c *Candidate, p string) {
	name := filepath.Base(p)
	if skipName.MatchString(name) || !configExt[strings.ToLower(filepath.Ext(name))] {
		return
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Size() > 256<<10 || fi.Size() == 0 {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	line, hexes, hits := 0, 0, 0
	for sc.Scan() {
		line++
		t := sc.Text()
		if strings.IndexByte(t, 0) >= 0 {
			return // binary
		}
		if themeKey.MatchString(t) && hits < 3 {
			hits++
			snippet := strings.TrimSpace(t)
			if len(snippet) > 70 {
				snippet = snippet[:70] + "…"
			}
			c.Evidence = append(c.Evidence, fmt.Sprintf("%s:%d: %s", xdg.Tilde(p), line, snippet))
		}
		hexes += len(hexRe.FindAllString(t, -1))
	}
	score := hits * 2
	if hexes >= 4 {
		score += 2
		c.Evidence = append(c.Evidence, fmt.Sprintf("%s: %d hex colors", xdg.Tilde(p), hexes))
	}
	if score > 0 {
		c.Score += score
		c.Files = append(c.Files, p)
	}
}

func brewList() map[string]bool {
	out := map[string]bool{}
	brew := adapter.LookPath("brew")
	if brew == "" {
		return out
	}
	for _, args := range [][]string{{"list", "--formula", "-1"}, {"list", "--cask", "-1"}} {
		b, err := exec.Command(brew, args...).Output()
		if err != nil {
			continue
		}
		for _, n := range strings.Fields(string(b)) {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

// Scaffold writes a starter adapter for a candidate into
// ~/.config/omac/adapters and returns its path.
func Scaffold(c Candidate) (string, error) {
	dir := filepath.Join(xdg.ConfigDir(), "adapters")
	path := filepath.Join(dir, strings.ToLower(c.App)+".toml")
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("%s already exists", xdg.Tilde(path))
	}
	file := c.Dir
	if len(c.Files) > 0 {
		file = c.Files[0]
	}
	format := ""
	switch strings.ToLower(filepath.Ext(file)) {
	case ".toml", ".json", ".jsonc", ".yml", ".yaml":
	default:
		format = "\nformat = \"kv\"    # key = value lines; or toml | json | yaml"
	}
	var ev strings.Builder
	for _, e := range c.Evidence {
		ev.WriteString("#   " + e + "\n")
	}
	content := fmt.Sprintf(`# Starter adapter generated by "omac scan --discover".
# Edit the steps, then check it with "omac scan" and "omac apply <theme> --only %[1]s --dry-run".
#
# What the scanner found:
%[2]s
name = %[1]q
description = "TODO: what gets themed"
support = "partial"

[detect]
paths = [%[3]q]
bins = [%[1]q]

# Option A: point an existing setting at a palette color.
[[steps]]
kind = "set"
file = %[4]q%[5]s
key = "theme"          # dotted paths work: "ui.colors.accent"
value = "{{ accent }}" # any colors.toml name; also {{ name_strip }}, {{ name_rgb }}, {{ mix a b 30%% }}

# Option B: render a whole theme file from a template you put in
# ~/.config/omac/templates/%[1]s.tpl, then select it with a "set" step.
# [[steps]]
# kind = "write"
# template = "%[1]s.tpl"
# target = "%[6]s/themes/omarchy.toml"

# Option C: run a command (reload, CLI setter…).
# [[steps]]
# kind = "cmd"
# run = "%[1]s --reload"
# ignore_error = true
`, c.App, ev.String(), xdg.Tilde(c.Dir), xdg.Tilde(file), format, xdg.Tilde(c.Dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(content), 0o644)
}
