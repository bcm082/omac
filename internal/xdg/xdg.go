// Package xdg resolves the directories omac reads and writes.
package xdg

import (
	"os"
	"path/filepath"
	"strings"
)

func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return os.Getenv("HOME")
	}
	return h
}

// Expand resolves a leading ~ and any $VARS in p.
func Expand(p string) string {
	if p == "~" {
		return Home()
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(Home(), p[2:])
	}
	return os.ExpandEnv(p)
}

// Tilde shortens a path under $HOME for display.
func Tilde(p string) string {
	h := Home()
	if p == h {
		return "~"
	}
	if strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

func ConfigDir() string { return filepath.Join(Home(), ".config", "omac") }
func CacheDir() string  { return filepath.Join(Home(), ".cache", "omac") }
func StateDir() string  { return filepath.Join(Home(), ".local", "state", "omac") }
func DataDir() string   { return filepath.Join(Home(), ".local", "share", "omac") }

// GeneratedDir holds rendered files that configs reference by path.
func GeneratedDir() string { return filepath.Join(ConfigDir(), "generated") }

// WriteFileAtomic writes via a temp file and rename, keeping the mode of an
// existing file.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".omac-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
