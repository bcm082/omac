package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"omac/internal/xdg"
)

const keepBackups = 15

// Backup records the state of every file an apply touches so `omac undo`
// can put them back exactly, including deleting files that did not exist.
type Backup struct {
	Dir   string `json:"-"`
	Theme string `json:"theme"`
	// The theme omac had applied before this one, restored as current by undo.
	PrevTheme     string        `json:"prev_theme,omitempty"`
	PrevWallpaper int           `json:"prev_wallpaper_index,omitempty"`
	Created       time.Time     `json:"created"`
	Entries       []BackupEntry `json:"entries"`
	// System state that is not a file.
	PrevPicture string `json:"prev_wallpaper,omitempty"`
	PrevDark    *bool  `json:"prev_dark,omitempty"`
	seen        map[string]bool
}

type BackupEntry struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Copy    string `json:"copy,omitempty"`
}

func backupRoot() string { return filepath.Join(xdg.StateDir(), "backups") }

func NewBackup(themeName string) *Backup {
	dir := filepath.Join(backupRoot(), time.Now().Format("20060102-150405.000"))
	return &Backup{Dir: dir, Theme: themeName, Created: time.Now(), seen: map[string]bool{}}
}

// Save snapshots path before its first modification in this apply.
func (b *Backup) Save(path string) error {
	if b == nil || b.seen[path] {
		return nil
	}
	b.seen[path] = true
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		b.Entries = append(b.Entries, BackupEntry{Path: path})
		return b.Commit()
	}
	if err != nil {
		return err
	}
	cp := fmt.Sprintf("%03d-%s", len(b.Entries), filepath.Base(path))
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(b.Dir, cp), data, 0o600); err != nil {
		return err
	}
	b.Entries = append(b.Entries, BackupEntry{Path: path, Existed: true, Copy: cp})
	return b.Commit()
}

// Commit writes the manifest, or nothing when nothing changed. Save calls it
// after every snapshot so an interrupted apply can still be undone.
func (b *Backup) Commit() error {
	if b == nil || (len(b.Entries) == 0 && b.PrevPicture == "" && b.PrevDark == nil) {
		return nil
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(filepath.Join(b.Dir, "manifest.json"), data, 0o600); err != nil {
		return err
	}
	return nil
}

// Done finishes an apply: final manifest, then drop the oldest backups.
func (b *Backup) Done() error {
	if err := b.Commit(); err != nil {
		return err
	}
	prune()
	return nil
}

func listBackups() []string {
	dirs, _ := filepath.Glob(filepath.Join(backupRoot(), "*", "manifest.json"))
	sort.Strings(dirs)
	for i := range dirs {
		dirs[i] = filepath.Dir(dirs[i])
	}
	return dirs
}

func prune() {
	dirs := listBackups()
	for len(dirs) > keepBackups {
		os.RemoveAll(dirs[0])
		dirs = dirs[1:]
	}
}

// Undo restores the most recent backup and removes it. It returns the
// theme that was applied and the files restored.
func Undo() (*Backup, []string, error) {
	dirs := listBackups()
	if len(dirs) == 0 {
		return nil, nil, fmt.Errorf("nothing to undo")
	}
	dir := dirs[len(dirs)-1]
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, nil, err
	}
	var b Backup
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, nil, err
	}
	var restored []string
	for _, e := range b.Entries {
		if !e.Existed {
			if err := os.Remove(e.Path); err == nil {
				restored = append(restored, "removed "+xdg.Tilde(e.Path))
				// Drop folders omac created, stopping at the first non-empty one.
				for dir := filepath.Dir(e.Path); dir != xdg.Home() && os.Remove(dir) == nil; dir = filepath.Dir(dir) {
				}
			}
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Copy))
		if err != nil {
			return &b, restored, err
		}
		if err := xdg.WriteFileAtomic(e.Path, content); err != nil {
			return &b, restored, err
		}
		restored = append(restored, "restored "+xdg.Tilde(e.Path))
	}
	if b.PrevPicture != "" {
		if _, err := os.Stat(b.PrevPicture); err == nil && SetWallpaper(b.PrevPicture) == nil {
			restored = append(restored, "wallpaper "+xdg.Tilde(b.PrevPicture))
		}
	}
	if b.PrevDark != nil && SetDarkMode(*b.PrevDark) == nil {
		restored = append(restored, fmt.Sprintf("dark mode %v", *b.PrevDark))
	}
	return &b, restored, os.RemoveAll(dir)
}
