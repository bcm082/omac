// Package assets embeds the built-in adapters and templates.
package assets

import "embed"

//go:embed adapters/*.toml templates/*
var FS embed.FS
