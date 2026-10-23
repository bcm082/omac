// Package adapter describes how each app gets themed and applies it.
//
// An adapter is a TOML file: a detect rule plus ordered steps. Built-in
// adapters are embedded from assets/adapters; files in
// ~/.config/omac/adapters override them by name or add new apps.
package adapter

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"omac/assets"
	"omac/internal/xdg"
)

type Adapter struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Support     string `toml:"support"` // "full" or "partial"
	Order       int    `toml:"order"`   // lower runs first; wallpaper/appearance go early
	Hint        string `toml:"hint"`    // shown after a successful apply
	Detect      Detect `toml:"detect"`
	Steps       []Step `toml:"steps"`

	Source string `toml:"-"` // "built-in" or the user file path
}

// Detect decides whether the app is installed. Any match counts.
type Detect struct {
	Always bool     `toml:"always"`
	Bins   []string `toml:"bins"`  // executables on PATH
	Apps   []string `toml:"apps"`  // bundles in /Applications or ~/Applications
	Paths  []string `toml:"paths"` // files or directories
}

// Step is one action. Kind selects which fields apply:
//
//	write       render Template (or inline Content) to Target; ThemeFile, when
//	            the theme ships it, is copied verbatim instead
//	set         set Key=Value (or every entry of Values) in File, keeping the
//	            rest of the file; Format is toml|json|yaml|kv (from extension)
//	block       keep Content between "omac:<ID>" markers in File
//	cmd         run Run with /bin/sh
//	wallpaper   set the desktop picture from the theme's backgrounds
//	appearance  switch macOS light/dark to match the theme
//
// TargetsCmd makes write/set run once per line the command prints, with
// {{ target_dir }} set to that line.
type Step struct {
	Kind string `toml:"kind"`

	Template  string `toml:"template"`
	ThemeFile string `toml:"theme_file"`
	Content   string `toml:"content"`
	Target    string `toml:"target"`

	File     string         `toml:"file"`
	Files    []string       `toml:"files"` // first that exists, else the first
	Format   string         `toml:"format"`
	Key      string         `toml:"key"`
	Value    any            `toml:"value"`
	Values   map[string]any `toml:"values"`
	ID       string         `toml:"id"`
	Position string         `toml:"position"` // block: "bottom" (default) or "top"
	Comment  string         `toml:"comment"`  // block comment leader, default "#"

	Run         string `toml:"run"`
	IgnoreError bool   `toml:"ignore_error"`
	Reload      bool   `toml:"reload"` // also run after `omac undo`; must not use {{ tokens }}

	TargetsCmd string `toml:"targets_cmd"`
	Optional   bool   `toml:"optional"` // a failure here only warns
}

func (a *Adapter) Partial() bool { return a.Support == "partial" }

// Load returns built-in adapters merged with the user's, sorted by order then name.
func Load() ([]*Adapter, []error) {
	byName := map[string]*Adapter{}
	var errs []error

	entries, _ := fs.ReadDir(assets.FS, "adapters")
	for _, e := range entries {
		data, err := fs.ReadFile(assets.FS, "adapters/"+e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		a, err := parse(data, "built-in")
		if err != nil {
			errs = append(errs, fmt.Errorf("built-in %s: %w", e.Name(), err))
			continue
		}
		byName[a.Name] = a
	}

	userDir := filepath.Join(xdg.ConfigDir(), "adapters")
	files, _ := filepath.Glob(filepath.Join(userDir, "*.toml"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		a, err := parse(data, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", xdg.Tilde(f), err))
			continue
		}
		byName[a.Name] = a
	}

	out := make([]*Adapter, 0, len(byName))
	for _, a := range byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out, errs
}

var validKinds = map[string]bool{"write": true, "set": true, "block": true, "cmd": true, "wallpaper": true, "appearance": true}

func parse(data []byte, source string) (*Adapter, error) {
	var a Adapter
	md, err := toml.Decode(string(data), &a)
	if err != nil {
		return nil, err
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		// Tables decoded into `any` (value/values) are reported as undecoded.
		if len(k) >= 2 && k[0] == "steps" && (k[1] == "value" || k[1] == "values") {
			continue
		}
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown keys: %s", strings.Join(unknown, ", "))
	}
	if a.Name == "" {
		return nil, fmt.Errorf("missing name")
	}
	if a.Support == "" {
		a.Support = "full"
	}
	if a.Order == 0 {
		a.Order = 50
	}
	for i, s := range a.Steps {
		if !validKinds[s.Kind] {
			return nil, fmt.Errorf("step %d: unknown kind %q", i+1, s.Kind)
		}
	}
	a.Source = source
	return &a, nil
}

// Installed reports whether the app is present and what matched.
func (a *Adapter) Installed() (bool, string) {
	d := a.Detect
	if d.Always {
		return true, "always"
	}
	for _, b := range d.Bins {
		if p := LookPath(b); p != "" {
			return true, xdg.Tilde(p)
		}
	}
	for _, app := range d.Apps {
		for _, dir := range []string{"/Applications", filepath.Join(xdg.Home(), "Applications"), "/System/Applications"} {
			p := filepath.Join(dir, app)
			if _, err := os.Stat(p); err == nil {
				return true, xdg.Tilde(p)
			}
		}
	}
	for _, p := range d.Paths {
		if _, err := os.Stat(xdg.Expand(p)); err == nil {
			return true, p
		}
	}
	return false, ""
}

// LookPath searches PATH plus the usual Homebrew and user bin directories,
// since a TUI launched from a launcher may have a thin PATH.
func LookPath(name string) string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin", filepath.Join(xdg.Home(), ".local/bin"),
		filepath.Join(xdg.Home(), ".cargo/bin"), filepath.Join(xdg.Home(), "go/bin"))
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// OwnedPaths lists every path an adapter detects or edits; the discover
// scanner uses it to skip apps that already have an adapter.
func (a *Adapter) OwnedPaths() []string {
	var out []string
	for _, p := range a.Detect.Paths {
		out = append(out, xdg.Expand(p))
	}
	for _, s := range a.Steps {
		for _, p := range append([]string{s.File, s.Target}, s.Files...) {
			if p != "" && !strings.Contains(p, "{{") {
				out = append(out, xdg.Expand(p))
			}
		}
	}
	return out
}
