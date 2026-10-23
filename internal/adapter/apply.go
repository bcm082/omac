package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"omac/assets"
	"omac/internal/theme"
	"omac/internal/xdg"
)

// Context carries everything an apply needs.
type Context struct {
	Index     *theme.Index
	Theme     *theme.Theme
	Palette   theme.Palette
	Vars      map[string]string
	Wallpaper int // index into Theme.Backgrounds()
	Backup    *Backup
	DryRun    bool
}

type Status int

const (
	OK Status = iota
	Unchanged
	Skipped // not installed or disabled
	Warned  // applied, but an optional step failed
	Failed
)

func (s Status) String() string {
	return [...]string{"ok", "unchanged", "skipped", "warning", "failed"}[s]
}

type Result struct {
	Adapter *Adapter
	Status  Status
	Notes   []string
	Err     error
}

func NewContext(ix *theme.Index, t *theme.Theme, wallpaper int, dryRun bool) (*Context, error) {
	p, err := t.Palette()
	if err != nil {
		return nil, err
	}
	vars := map[string]string{
		"home":       xdg.Home(),
		"theme_name": t.Name,
		"omac_dir":   xdg.ConfigDir(),
		"generated":  xdg.GeneratedDir(),
	}
	vars["chroma_style"] = chromaStyle(t.Name, p["mode"])
	c := &Context{Index: ix, Theme: t, Palette: p, Vars: vars, Wallpaper: wallpaper, DryRun: dryRun}
	if !dryRun {
		c.Backup = NewBackup(t.Name)
	}
	return c, nil
}

// Apply runs every step of one adapter.
func Apply(c *Context, a *Adapter) Result {
	r := Result{Adapter: a, Status: Unchanged}
	if ok, _ := a.Installed(); !ok {
		r.Status = Skipped
		r.Notes = append(r.Notes, "not installed")
		return r
	}
	for i, s := range a.Steps {
		// A reload only matters when an earlier step changed something.
		if s.Kind == "cmd" && s.Reload && r.Status == Unchanged {
			continue
		}
		changed, notes, err := runStep(c, s)
		r.Notes = append(r.Notes, notes...)
		if err != nil {
			if s.Optional {
				r.Status = Warned
				r.Notes = append(r.Notes, fmt.Sprintf("step %d (%s): %v", i+1, s.Kind, err))
				continue
			}
			r.Status, r.Err = Failed, fmt.Errorf("step %d (%s): %w", i+1, s.Kind, err)
			return r
		}
		if changed && r.Status == Unchanged {
			r.Status = OK
		}
	}
	if a.Hint != "" && r.Status == OK {
		r.Notes = append(r.Notes, a.Hint)
	}
	return r
}

func (c *Context) render(s string) (string, []string) {
	return theme.Render(s, c.Palette, c.Vars)
}

func runStep(c *Context, s Step) (bool, []string, error) {
	if s.TargetsCmd == "" {
		return runOne(c, s)
	}
	out, err := shell(c, s.TargetsCmd)
	if err != nil {
		return false, nil, fmt.Errorf("targets_cmd: %w", err)
	}
	var changed bool
	var notes []string
	for _, dir := range strings.Split(strings.TrimSpace(out), "\n") {
		if dir = strings.TrimSpace(dir); dir == "" {
			continue
		}
		c.Vars["target_dir"] = dir
		ch, n, err := runOne(c, s)
		notes = append(notes, n...)
		if err != nil {
			return changed, notes, err
		}
		changed = changed || ch
	}
	delete(c.Vars, "target_dir")
	if out == "" {
		notes = append(notes, "no targets found")
	}
	return changed, notes, nil
}

func runOne(c *Context, s Step) (bool, []string, error) {
	switch s.Kind {
	case "write":
		return stepWrite(c, s)
	case "set":
		return stepSet(c, s)
	case "block":
		file, _ := c.render(xdg.Expand(s.File))
		content, unknown := c.render(s.Content)
		src, _ := os.ReadFile(file)
		return writeIfChanged(c, file, []byte(setBlock(string(src), s.ID, content, s.Position, s.Comment)), unknown)
	case "cmd":
		if c.DryRun {
			cmd, _ := c.render(s.Run)
			return false, []string{"would run: " + cmd}, nil
		}
		_, err := shell(c, s.Run)
		if err != nil && s.IgnoreError {
			return true, nil, nil
		}
		return err == nil, nil, err
	case "wallpaper":
		if c.DryRun {
			return false, []string{"would set the wallpaper"}, nil
		}
		path, rel, err := prepareWallpaper(c.Theme, c.Wallpaper)
		if err != nil {
			return false, nil, err
		}
		if c.Backup != nil && c.Backup.PrevPicture == "" {
			c.Backup.PrevPicture = CurrentWallpaper()
			if err := c.Backup.Save(wallpaperStore()); err != nil {
				return false, nil, err
			}
		}
		if err := SetWallpaper(path); err != nil {
			return false, nil, err
		}
		notes := []string{filepath.Base(rel)}
		if err := setWallpaperAllSpaces(c, path); err != nil {
			notes = append(notes, "current Space only: "+err.Error())
		} else {
			notes = append(notes, "all Spaces")
		}
		return true, notes, nil
	case "appearance":
		want := c.Palette["mode"] != "light"
		cur, err := DarkMode()
		if err == nil && cur == want {
			return false, nil, nil
		}
		if c.DryRun {
			return false, []string{"would switch to " + c.Palette["mode"] + " mode"}, nil
		}
		if c.Backup != nil && err == nil {
			c.Backup.PrevDark = &cur
		}
		return true, []string{c.Palette["mode"] + " mode"}, SetDarkMode(want)
	}
	return false, nil, fmt.Errorf("unknown step kind %q", s.Kind)
}

func stepWrite(c *Context, s Step) (bool, []string, error) {
	target, _ := c.render(xdg.Expand(s.Target))
	var content string
	var unknown []string
	switch {
	case s.ThemeFile != "" && c.Theme.Has(s.ThemeFile):
		data, err := c.Theme.Read(s.ThemeFile)
		if err != nil {
			return false, nil, err
		}
		content = data
	case s.Content != "":
		content, unknown = c.render(s.Content)
	default:
		tpl, err := LoadTemplate(c.Index, s.Template)
		if err != nil {
			return false, nil, err
		}
		content, unknown = c.render(tpl)
	}
	return writeIfChanged(c, target, []byte(content), unknown)
}

func stepSet(c *Context, s Step) (bool, []string, error) {
	file := pickFile(s)
	file, _ = c.render(file)
	format := formatFor(s, file)
	values := map[string]any{}
	for k, v := range s.Values {
		values[k] = v
	}
	if s.Key != "" {
		values[s.Key] = s.Value
	}
	if len(values) == 0 {
		return false, nil, fmt.Errorf("set needs key/value or values")
	}
	src, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return false, nil, err
	}
	out := src
	var unknown []string
	for _, k := range sortedKeys(values) {
		v, u := theme.RenderValue(values[k], c.Palette, c.Vars)
		unknown = append(unknown, u...)
		if out, err = setKey(format, out, k, v); err != nil {
			return false, nil, fmt.Errorf("%s: %w", xdg.Tilde(file), err)
		}
	}
	return writeIfChanged(c, file, out, unknown)
}

func pickFile(s Step) string {
	if s.File != "" {
		return xdg.Expand(s.File)
	}
	for _, f := range s.Files {
		if _, err := os.Stat(xdg.Expand(f)); err == nil {
			return xdg.Expand(f)
		}
	}
	if len(s.Files) > 0 {
		return xdg.Expand(s.Files[0])
	}
	return ""
}

func writeIfChanged(c *Context, path string, data []byte, unknown []string) (bool, []string, error) {
	var notes []string
	if len(unknown) > 0 {
		notes = append(notes, fmt.Sprintf("%s: unresolved %s", filepath.Base(path), strings.Join(dedupe(unknown), " ")))
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, notes, nil
	}
	if c.DryRun {
		return true, append(notes, "would write "+xdg.Tilde(path)), nil
	}
	if err := c.Backup.Save(path); err != nil {
		return false, notes, fmt.Errorf("backup %s: %w", path, err)
	}
	if err := xdg.WriteFileAtomic(path, data); err != nil {
		return false, notes, err
	}
	return true, notes, nil
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func shell(c *Context, script string) (string, error) {
	script, _ = c.render(script)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+os.Getenv("PATH")+":/opt/homebrew/bin:/usr/local/bin:"+filepath.Join(xdg.Home(), ".local/bin"),
		"OMAC_THEME="+c.Theme.Name, "OMAC_MODE="+c.Palette["mode"])
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("%s", msg)
	}
	return out.String(), nil
}

// LoadTemplate resolves a template name. A file of the same base name in
// ~/.config/omac/templates always wins; "omarchy:<name>" comes from
// Omarchy's default/themed folder; anything else is a built-in.
func LoadTemplate(ix *theme.Index, name string) (string, error) {
	base := strings.TrimPrefix(name, "omarchy:")
	if data, err := os.ReadFile(filepath.Join(xdg.ConfigDir(), "templates", base)); err == nil {
		return string(data), nil
	}
	if strings.HasPrefix(name, "omarchy:") {
		return ix.Template(base)
	}
	data, err := fs.ReadFile(assets.FS, "templates/"+base)
	if err != nil {
		return "", fmt.Errorf("template %q not found", name)
	}
	return string(data), nil
}

// RunReloads re-runs the reload commands of installed adapters, so apps pick
// up files that undo just restored.
func RunReloads(adapters []*Adapter) {
	for _, a := range adapters {
		if ok, _ := a.Installed(); !ok {
			continue
		}
		for _, s := range a.Steps {
			if s.Kind == "cmd" && s.Reload {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = exec.CommandContext(ctx, "/bin/sh", "-c", s.Run).Run()
				cancel()
			}
		}
	}
}

// chromaStyle picks the Chroma syntax style (used by superfile, glow and other
// Go TUIs) closest to an Omarchy theme, falling back on light/dark.
func chromaStyle(themeName, mode string) string {
	known := map[string]string{
		"catppuccin":       "catppuccin-mocha",
		"catppuccin-latte": "catppuccin-latte",
		"everforest":       "evergarden",
		"gruvbox":          "gruvbox",
		"nord":             "nord",
		"rose-pine":        "rose-pine-dawn",
		"tokyo-night":      "tokyonight-night",
	}
	if s, ok := known[themeName]; ok {
		return s
	}
	if mode == "light" {
		return "github"
	}
	return "github-dark"
}
