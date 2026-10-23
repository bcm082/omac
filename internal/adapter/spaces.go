package adapter

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"howett.net/plist"

	"omac/internal/xdg"
)

// The System Events / NSWorkspace APIs only change the wallpaper of the
// current Space. macOS keeps every Space's choice in this store, so after
// setting the picture we write it into every Desktop entry; restarting
// WallpaperAgent (a reload step in wallpaper.toml) makes it take effect.
func wallpaperStore() string {
	return filepath.Join(xdg.Home(), "Library", "Application Support", "com.apple.wallpaper", "Store", "Index.plist")
}

func setWallpaperAllSpaces(c *Context, image string) error {
	store := wallpaperStore()
	data, err := os.ReadFile(store)
	if err != nil {
		return err
	}
	var root map[string]any
	format, err := plist.Unmarshal(data, &root)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(store), err)
	}

	u := url.URL{Scheme: "file", Path: image}
	config, err := plist.Marshal(map[string]any{
		"type": "imageFile",
		"url":  map[string]any{"relative": u.String()},
	}, plist.BinaryFormat)
	if err != nil {
		return err
	}

	now := time.Now()
	n := 0
	setDesktop := func(entry any) {
		e, ok := entry.(map[string]any)
		if !ok {
			return
		}
		desk, _ := e["Desktop"].(map[string]any)
		if desk == nil {
			desk = map[string]any{}
			e["Desktop"] = desk
		}
		content, _ := desk["Content"].(map[string]any)
		if content == nil {
			content = map[string]any{"Shuffle": "$null", "EncodedOptionValues": "$null"}
			desk["Content"] = content
		}
		// EncodedOptionValues (fill/crop placement) is left as each Space had it.
		content["Choices"] = []any{map[string]any{
			"Provider":      "com.apple.wallpaper.choice.image",
			"Files":         []any{},
			"Configuration": config,
		}}
		desk["LastSet"] = now
		desk["LastUse"] = now
		e["Type"] = "individual"
		n++
	}
	eachValue := func(m any, fn func(any)) {
		if mm, ok := m.(map[string]any); ok {
			for _, v := range mm {
				fn(v)
			}
		}
	}

	setDesktop(root["SystemDefault"])
	eachValue(root["Displays"], setDesktop)
	eachValue(root["Spaces"], func(space any) {
		s, ok := space.(map[string]any)
		if !ok {
			return
		}
		setDesktop(s["Default"])
		eachValue(s["Displays"], setDesktop)
	})
	if n == 0 {
		return fmt.Errorf("no desktop entries in %s", filepath.Base(store))
	}

	var buf bytes.Buffer
	enc := plist.NewEncoderForFormat(&buf, format)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := c.Backup.Save(store); err != nil {
		return err
	}
	return xdg.WriteFileAtomic(store, buf.Bytes())
}
