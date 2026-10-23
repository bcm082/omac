// Package app ties config, state, themes and adapters together for the CLI and TUI.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"omac/internal/adapter"
	"omac/internal/theme"
	"omac/internal/xdg"
)

// Config lives in ~/.config/omac/config.toml.
type Config struct {
	// Extra theme repos ("owner/repo") whose root holds a colors.toml,
	// such as community Omarchy themes.
	ExtraThemes []string `toml:"extra_themes"`
	// Adapters that apply skips.
	Disabled []string `toml:"disabled"`
}

func configPath() string { return filepath.Join(xdg.ConfigDir(), "config.toml") }

// ConfigErr holds the parse error of the last LoadConfig, so the CLI and TUI
// can warn instead of silently ignoring disabled adapters.
var ConfigErr error

func LoadConfig() Config {
	var c Config
	ConfigErr = nil
	if _, err := toml.DecodeFile(configPath(), &c); err != nil && !os.IsNotExist(err) {
		ConfigErr = fmt.Errorf("%s: %w", xdg.Tilde(configPath()), err)
	}
	return c
}

const configHeader = `# omac configuration
#
# extra_themes: GitHub repos ("owner/repo") with a colors.toml at the root,
#               e.g. community Omarchy themes.
# disabled:     adapters that "omac apply" skips (toggle with space in the
#               Apps tab of the TUI).
`

func (c Config) Save() error {
	f, err := os.CreateTemp(xdg.ConfigDir(), ".config-*")
	if err != nil {
		if err := os.MkdirAll(xdg.ConfigDir(), 0o755); err != nil {
			return err
		}
		if f, err = os.CreateTemp(xdg.ConfigDir(), ".config-*"); err != nil {
			return err
		}
	}
	defer os.Remove(f.Name())
	f.WriteString(configHeader + "\n")
	if c.ExtraThemes == nil {
		c.ExtraThemes = []string{}
	}
	if c.Disabled == nil {
		c.Disabled = []string{}
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(f.Name(), configPath())
}

func (c Config) IsDisabled(name string) bool {
	for _, d := range c.Disabled {
		if d == name {
			return true
		}
	}
	return false
}

func (c *Config) Toggle(name string) {
	for i, d := range c.Disabled {
		if d == name {
			c.Disabled = append(c.Disabled[:i], c.Disabled[i+1:]...)
			return
		}
	}
	c.Disabled = append(c.Disabled, name)
	sort.Strings(c.Disabled)
}

// State remembers the applied theme.
type State struct {
	Theme     string    `toml:"theme"`
	Wallpaper int       `toml:"wallpaper"`
	Applied   time.Time `toml:"applied"`
}

func statePath() string { return filepath.Join(xdg.StateDir(), "current.toml") }

func LoadState() State {
	var s State
	_, _ = toml.DecodeFile(statePath(), &s)
	return s
}

func (s State) Save() error {
	if err := os.MkdirAll(xdg.StateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.Create(statePath())
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(s)
}

// Options narrow an apply.
type Options struct {
	Wallpaper int
	Only      []string // adapter names; empty = all enabled
	DryRun    bool
	Progress  func(adapter.Result)
}

// Apply themes every enabled, installed app and records a backup for undo.
func Apply(ix *theme.Index, t *theme.Theme, adapters []*adapter.Adapter, cfg Config, opt Options) ([]adapter.Result, error) {
	ctx, err := adapter.NewContext(ix, t, opt.Wallpaper, opt.DryRun)
	if err != nil {
		return nil, err
	}
	if ctx.Backup != nil {
		prev := LoadState()
		ctx.Backup.PrevTheme, ctx.Backup.PrevWallpaper = prev.Theme, prev.Wallpaper
	}
	only := map[string]bool{}
	for _, o := range opt.Only {
		only[o] = true
	}
	var results []adapter.Result
	for _, a := range adapters {
		var r adapter.Result
		switch {
		case len(only) > 0 && !only[a.Name]:
			continue
		case len(only) == 0 && cfg.IsDisabled(a.Name):
			r = adapter.Result{Adapter: a, Status: adapter.Skipped, Notes: []string{"disabled"}}
		default:
			r = adapter.Apply(ctx, a)
		}
		results = append(results, r)
		if opt.Progress != nil {
			opt.Progress(r)
		}
	}
	if !opt.DryRun {
		if err := ctx.Backup.Done(); err != nil {
			return results, err
		}
		_ = State{Theme: t.Name, Wallpaper: opt.Wallpaper, Applied: time.Now()}.Save()
	}
	return results, nil
}

// Undo restores the files and system settings from the last apply.
func Undo(adapters []*adapter.Adapter) (string, []string, error) {
	b, restored, err := adapter.Undo()
	if b == nil {
		return "", restored, err
	}
	if err == nil {
		adapter.RunReloads(adapters)
		if b.PrevTheme != "" {
			_ = State{Theme: b.PrevTheme, Wallpaper: b.PrevWallpaper, Applied: time.Now()}.Save()
		} else {
			_ = os.Remove(statePath())
		}
	}
	return b.Theme, restored, err
}

// How long a theme that appeared upstream keeps its "new" badge.
const newFor = 7 * 24 * time.Hour

type seenFile struct {
	Themes map[string]time.Time `toml:"themes"`
}

// TrackSeen records when each theme was first listed and returns the ones
// that appeared within the last week. The first run records everything as a
// baseline, so only themes Omarchy adds later count as new.
func TrackSeen(themes []*theme.Theme) map[string]bool {
	path := filepath.Join(xdg.StateDir(), "seen.toml")
	var f seenFile
	_, err := toml.DecodeFile(path, &f)
	baseline := err != nil
	if f.Themes == nil {
		f.Themes = map[string]time.Time{}
	}
	now := time.Now()
	changed := false
	fresh := map[string]bool{}
	for _, t := range themes {
		first, ok := f.Themes[t.Name]
		if !ok {
			first = now
			if baseline {
				first = time.Unix(0, 0)
			}
			f.Themes[t.Name] = first
			changed = true
		}
		if now.Sub(first) < newFor {
			fresh[t.Name] = true
		}
	}
	if changed {
		if err := os.MkdirAll(xdg.StateDir(), 0o755); err == nil {
			if out, err := os.Create(path); err == nil {
				_ = toml.NewEncoder(out).Encode(f)
				out.Close()
			}
		}
	}
	return fresh
}
