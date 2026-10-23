package adapter

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"omac/internal/theme"
	"omac/internal/xdg"
)

func osascript(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("osascript: %s", s)
	}
	return s, nil
}

func asString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// CurrentWallpaper returns the picture of the main display.
func CurrentWallpaper() string {
	s, _ := osascript(`tell application "System Events" to get picture of desktop 1`)
	return s
}

func SetWallpaper(path string) error {
	_, err := osascript(`tell application "System Events" to tell every desktop to set picture to POSIX file ` + asString(path))
	return err
}

func DarkMode() (bool, error) {
	s, err := osascript(`tell application "System Events" to tell appearance preferences to get dark mode`)
	return s == "true", err
}

func SetDarkMode(dark bool) error {
	_, err := osascript(fmt.Sprintf(`tell application "System Events" to tell appearance preferences to set dark mode to %v`, dark))
	return err
}

// prepareWallpaper downloads the chosen background and converts formats the
// desktop does not take reliably (webp) to JPEG. Each apply gets a fresh
// file name, because macOS ignores a change to the same path.
func prepareWallpaper(t *theme.Theme, index int) (string, string, error) {
	bgs := t.Backgrounds()
	if len(bgs) == 0 {
		return "", "", fmt.Errorf("theme has no backgrounds")
	}
	rel := bgs[((index%len(bgs))+len(bgs))%len(bgs)]
	src, err := t.Fetch(rel)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(xdg.DataDir(), "wallpapers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "*"))
	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	stamp := time.Now().Format("150405")
	ext := strings.ToLower(filepath.Ext(rel))
	var dst string
	if ext == ".webp" {
		dst = filepath.Join(dir, fmt.Sprintf("%s-%s-%s.jpg", t.Name, base, stamp))
		if out, err := exec.Command("sips", "-s", "format", "jpeg", "-s", "formatOptions", "best", src, "--out", dst).CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("converting %s: %s", rel, strings.TrimSpace(string(out)))
		}
	} else {
		dst = filepath.Join(dir, fmt.Sprintf("%s-%s-%s%s", t.Name, base, stamp, ext))
		data, err := os.ReadFile(src)
		if err != nil {
			return "", "", err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", "", err
		}
	}
	// Keep recent pictures around: undo may point Spaces back at them.
	if len(old) > keepBackups {
		sort.Slice(old, func(i, j int) bool { return modTime(old[i]).Before(modTime(old[j])) })
		for _, o := range old[:len(old)-keepBackups] {
			os.Remove(o)
		}
	}
	return dst, rel, nil
}

func modTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
