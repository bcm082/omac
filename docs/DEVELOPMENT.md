# omac — developer notes

How omac works, why it is built the way it is, and how to change it safely.
For day-to-day usage see [README.md](../README.md).

- **What:** a Go TUI/CLI that takes themes from the
  [Omarchy](https://github.com/basecamp/omarchy) repo (Linux/Hyprland) and
  applies them to macOS apps.
- **Where:** source in `~/omac`, binary at `~/.local/bin/omac` (`make install`).
- **Stack:** Go 1.27 (Homebrew), Bubble Tea v1 / Lip Gloss v1, BurntSushi/toml,
  tailscale/hujson, yaml.v3, howett.net/plist.
- **Status:** working; built and maintained for one Mac. MIT licensed.
  Repo: https://github.com/bcm082/omac

## Contents

1. [Mental model](#mental-model)
2. [Code layout](#code-layout)
3. [How an apply runs](#how-an-apply-runs)
4. [Omarchy theme format](#omarchy-theme-format)
5. [Adapters](#adapters)
6. [Per-app notes](#per-app-notes)
7. [Backups and undo](#backups-and-undo)
8. [Discover scanner](#discover-scanner)
9. [Testing safely](#testing-safely)
10. [Files omac owns](#files-omac-owns)
11. [Gotchas](#gotchas)
12. [Ideas / not done](#ideas--not-done)

## Mental model

```
GitHub basecamp/omarchy                omac                       your Mac
 themes/<name>/colors.toml   ─┐
 themes/<name>/backgrounds/*  ├─▶ index + blob cache ─▶ palette ─┐
 themes/<name>/neovim.lua …   │   (~/.cache/omac)       (resolved)│
 default/themed/*.tpl        ─┘                                    ▼
                                    adapters (TOML) ── steps ──▶ config files,
 assets/templates/*.tpl ────────────────────────────────────────▶ wallpaper,
 ~/.config/omac/{adapters,templates}/  (user overrides)            dark mode, reloads
```

- A **theme** is mostly one file, `colors.toml`, with about 30 named colors.
  Omarchy builds every app config from it with templates. omac copies that
  approach.
- An **adapter** is a TOML file per app. It has a `detect` rule (is the app
  installed?) and ordered `steps` (write a file, set a key, run a command…).
- Adding an app should never need Go code. It needs one adapter TOML and
  maybe one template.

## Code layout

| Path | Role |
|---|---|
| `main.go` | CLI: `list`, `apply`, `current`, `undo`, `scan [--discover\|--scaffold]`, `refresh`; no args opens the TUI |
| `internal/theme/source.go` | GitHub index (one tree API call per repo, cached 24h in `~/.cache/omac/index.json`), raw file downloads into a blob cache keyed by git SHA (`~/.cache/omac/blobs/`), `extra_themes` repos, `gh auth token` / `$GITHUB_TOKEN` for rate limits |
| `internal/theme/colors.go` | `colors.toml` parser and `Resolve()`, a port of Omarchy's `bin/omarchy-theme-color` (aliases, legacy `colorN`, derived shades, mode detection) |
| `internal/theme/render.go` | Template engine for Omarchy's `{{ … }}` syntax plus omac extras |
| `internal/adapter/adapter.go` | Adapter/Step types, loading (embedded + user dir), `Installed()` detection, `LookPath` with Homebrew paths |
| `internal/adapter/apply.go` | Step runner (`write`, `set`, `block`, `cmd`, `wallpaper`, `appearance`), `targets_cmd`, template lookup, `chromaStyle()` |
| `internal/adapter/edit.go` | Editors that preserve the rest of the file: TOML (line based), JSON/JSONC (hujson), YAML (yaml.v3 nodes), `kv` (Ghostty style), managed blocks |
| `internal/adapter/backup.go` | Per-apply snapshots and `Undo()` |
| `internal/adapter/macos.go` | osascript helpers (wallpaper, dark mode), wallpaper download + webp→jpg via `sips` |
| `internal/adapter/spaces.go` | Writes the wallpaper into **every Space** in the macOS wallpaper store |
| `internal/app/app.go` | Config (`~/.config/omac/config.toml`), state (`~/.local/state/omac/current.toml`), `Apply()` orchestration, `Undo()`, `TrackSeen()` (new-theme badges, `seen.toml`) |
| `internal/scan/discover.go` | Finds apps that look themeable but have no adapter; `Scaffold()` writes a starter adapter |
| `internal/tui/tui.go` | Bubble Tea UI: Themes / Apps / Discover tabs, wallpaper picker, new-theme badges, live apply progress, results, undo |
| `assets/adapters/*.toml` | 26 built-in adapters (embedded with `go:embed`) |
| `assets/templates/*.tpl` | Templates for apps Omarchy does not cover |

## How an apply runs

1. `theme.LoadIndex` gets the theme list (cache or GitHub).
2. `adapter.NewContext` resolves the palette and sets the template vars:
   `home`, `theme_name`, `omac_dir`, `generated` (`~/.config/omac/generated`),
   `chroma_style`.
3. `app.Apply` walks the adapters in `order` (wallpaper=1, macos=2,
   ghostty=10, everything else 50, ties by name). It skips disabled adapters
   (`config.toml`), and `--only` limits the run to the named ones.
4. `adapter.Apply` checks `Installed()` (not installed → skipped), then runs
   the steps:
   - File writes go through `writeIfChanged`: same content → nothing happens.
     Otherwise it snapshots the file, then writes atomically (temp file +
     rename, keeping file mode).
   - `cmd` steps with `reload = true` run **only if an earlier step in that
     adapter changed something**, so a re-apply doesn't restart apps.
5. The backup manifest is written after every snapshot. At the end, state is
   saved and old backups are pruned (keeps 15).

Statuses: `✓` changed, `=` unchanged, `·` skipped (missing or disabled),
`!` an optional step failed, `✗` failed.

## Omarchy theme format

Verified against the Omarchy repo on 2026-10-23 (default branch is
`quattro`, not `main`). The index reads the branch name from the API.

- `themes/<name>/colors.toml` holds named colors: `mode`, `accent`,
  `selection`, `muted`, `background`, `dark_background`, `darker_background`,
  `lighter_background`, `foreground`, `dark_foreground`, `light_foreground`,
  `bright_foreground`, `red yellow orange green cyan blue magenta brown`,
  `bright_*`. Older themes may use `color0..15` or short names (`bg`, `fg`);
  `Resolve()` handles all of these. Derived keys: `cursor`,
  `selection_background/foreground`, `theme_type`, `purple`.
- `themes/<name>/backgrounds/*` holds the wallpapers (mostly `.webp`).
- Some themes ship ready-made app files (`neovim.lua` in 15 of 23, a few
  `btop.theme`, `hyprland.lua`, `chromium.theme`). Omarchy's rule: **a
  shipped file wins over the template**. omac does the same through
  `theme_file =`.
- `default/themed/*.tpl` are Omarchy's templates (ghostty, btop, claude,
  neovim (aether.nvim), kitty, alacritty, helix, obsidian, vscode, …).
  Adapters reference them as `template = "omarchy:<name>.tpl"`; they are
  downloaded and cached like theme files.
- Omarchy's renderer: `bin/omarchy-theme-set-templates`. Color resolution:
  `bin/omarchy-theme-color`. Re-read both if templates start rendering
  wrong after an Omarchy update.

Template tokens (`render.go`):

| Token | Output |
|---|---|
| `{{ key }}` | value, e.g. `#7aa2f7` |
| `{{ key_strip }}` | `7aa2f7` |
| `{{ key_rgb }}` | `122,162,247` |
| `{{ key_sgr }}` | `38;2;122;162;247` (omac extra, for fastfetch) |
| `{{ mix a b 35% }}` (`mix_strip`, `mix_rgb`) | blended hex, rounded like Omarchy's awk |
| `gradient_start`, `shell_gradient`, `hypr_gradient` | basic support only |
| `{{ home }}` `{{ generated }}` `{{ theme_name }}` `{{ chroma_style }}` `{{ target_dir }}` | omac vars |

Unknown tokens are left as they are and reported as "unresolved" in the
results, except `{{.x}}` (Go-template syntax that glamour's JSON uses).

## Adapters

The full schema is in `internal/adapter/adapter.go` (comment on `Step`) and
in the README. Lookup rules:

- Adapters: `assets/adapters/*.toml`, then `~/.config/omac/adapters/*.toml`.
  The **same `name` overrides** the built-in one.
- Templates: `~/.config/omac/templates/<basename>` always wins, then
  `omarchy:` → Omarchy repo, else `assets/templates/`.
- Unknown keys in an adapter fail loudly, except under `value`/`values`
  (decoded into `any`).

Step kinds in short:

| Kind | Key fields | Notes |
|---|---|---|
| `write` | `template` / `content`, `theme_file`, `target` | `target` may use `{{ vars }}` |
| `set` | `file` or `files` (first that exists), `key`+`value` or `values`, `format` | Format comes from the extension, `kv` otherwise; dotted keys create tables/objects |
| `block` | `file`, `id`, `content`, `position`, `comment` | `# >>> omac:<id> … >>>` / `# <<< omac:<id> <<<`; the same id in two adapters is fine (zsh and fzf share `shell`) |
| `cmd` | `run`, `ignore_error`, `reload` | Runs with `/bin/sh -c`, gets `$OMAC_THEME` / `$OMAC_MODE`; `reload` commands also run after undo and must not use tokens |
| `wallpaper`, `appearance` | — | macOS built-ins |

`targets_cmd` (on write/set) runs the step once per line printed, with
`{{ target_dir }}` set to that line. It's used for Obsidian vaults and
superfile's config dir. **The command must exit 0** (end loops with
`; true`).

**Adding an app:** run `omac scan --discover`, then
`omac scan --scaffold <app>`, then edit `~/.config/omac/adapters/<app>.toml`.
Test with `omac apply <theme> --only <app> --dry-run`, then for real. If
it should become built-in, move the file into `assets/adapters/` (and the
template into `assets/templates/`), then `make install`.

## Per-app notes

What each app reads, and how the format was confirmed:

| App | Mechanism | Reload |
|---|---|---|
| wallpaper | osascript for the current Space + rewrite every Space in `~/Library/Application Support/com.apple.wallpaper/Store/Index.plist` | `killall WallpaperAgent` |
| macos | System Events `appearance preferences` → dark mode from `mode` (needs Automation permission once) | instant |
| ghostty | `~/.config/ghostty/themes/omarchy` + `theme = omarchy` (kv) | `pkill -USR2 -x ghostty` (Ghostty ≥1.2 reloads on SIGUSR2; the string is in the binary) |
| neovim | theme's `neovim.lua`, else Omarchy's aether.nvim template → `~/.config/nvim/lua/plugins/colorscheme.lua` | restart nvim |
| btop | `~/.config/btop/themes/omarchy.theme` + `color_theme` in `btop.conf` | restart (no safe signal: an unhandled one would kill btop) |
| claude | `~/.claude/themes/omarchy.json` + `"theme": "custom:omarchy"` in `~/.claude/settings.json` (copied from `omarchy-theme-set-claude`) | hot reload |
| opencode | `~/.config/opencode/themes/omarchy.json` + `theme` in `opencode.json` | restart |
| zed | `~/.config/zed/themes/omarchy.json` (schema v0.2.0) + `theme.{mode,dark,light}` in JSONC settings | live |
| tmux | `~/.config/omac/generated/tmux.conf` sourced from a block in `~/.tmux.conf`. The template is one opinionated status layout (session pill, window list, host on the right) in theme colors; override it in `~/.config/omac/templates/tmux.conf.tpl` | `tmux source-file` if a server is running |
| lazygit | `gui.theme.*` in `~/Library/Application Support/lazygit/config.yml` (macOS path; `~/.config/lazygit` as fallback) | restart |
| starship | `palette = "omarchy"` + `[palettes.omarchy]` | next prompt |
| herdr | `[theme.custom]` tokens (list taken from `src/config/theme.rs` in herdrdev/herdr) | `herdr server reload-config` |
| yazi | flavor at `~/.config/yazi/flavors/omarchy.yazi/{flavor.toml,tmtheme.xml}` + `[flavor] dark/light` in `theme.toml` (keys from yazi's `preset/theme-dark.toml`, v26.9) | restart |
| zsh | `~/.config/omac/shell/zsh-highlight.zsh` (ZSH_HIGHLIGHT_STYLES + autosuggest), sourced by the `shell` block at the end of `~/.zshrc` | `exec zsh` |
| fzf | `~/.config/omac/generated/fzfrc` through `FZF_DEFAULT_OPTS_FILE` (exported by `~/.config/omac/shell/fzf.zsh`) | every new fzf run |
| codex | `~/.codex/themes/omarchy.tmTheme` + `[tui] theme` (custom .tmTheme support found in the codex 0.164 binary). Syntax colors only | restart |
| gemini | `ui.customThemes.Omarchy` + `ui.theme` in `~/.gemini/settings.json` | restart |
| glow | glamour JSON at `generated/glow.json` + `style:` in `~/Library/Preferences/glow/glow.yml` | next run |
| fastfetch | `display.color.{keys,title,separator}` in `~/.config/fastfetch/config.jsonc` (created) | next run |
| cliamp | `~/.config/cliamp/themes/omac.toml` + `theme = "omac"` (the name `omarchy` might clash with a built-in; `default` is reserved) | restart / press `t` |
| superfile | `<cfgdir>/theme/omarchy.toml` + `theme` in `<cfgdir>/config.toml`, cfgdir = `~/Library/Application Support/superfile` (or `~/.config/superfile`). `code_syntax_highlight` takes a Chroma style name → `{{ chroma_style }}` | restart spf |
| obsidian | Omarchy's obsidian.css into every vault in `~/Library/Application Support/obsidian/obsidian.json` | select "Omarchy" once per vault |
| kitty, alacritty, helix, fish | adapters ready, apps not installed here | — |

Things that just follow Ghostty's 16 ANSI colors and need no adapter: most
TUIs, Codex's UI, starship segments that use named colors.

## Backups and undo

- `~/.local/state/omac/backups/<timestamp>/` holds copies of every file as
  it was before its **first** change in that apply, plus `manifest.json`
  (path, existed?, copy name, previous wallpaper path, previous dark mode,
  previous omac theme/wallpaper index).
- `omac undo` restores the newest backup:
  - copies files back and deletes files that didn't exist before (plus the
    empty folders omac created)
  - restores wallpaper and dark mode
  - runs every `reload = true` command
  - sets the "current" theme back to the previous one
  - then deletes that backup, so undo can be repeated back to the start.
- The wallpaper store plist is backed up **before** osascript touches it.
- Wallpaper images live in `~/.local/share/omac/wallpapers/`, keeping the
  newest 15 so older undo steps still point at real files. Each apply uses
  a new file name, because macOS ignores a picture change to the same path.

## New themes and wallpapers

- **New themes:** nothing is hard-coded. Any folder under `themes/` in the
  Omarchy repo with a `colors.toml` is a theme. The index refreshes every
  24h, on `r` in the TUI, or with `omac refresh`.
  `app.TrackSeen` records when each theme was first listed in
  `~/.local/state/omac/seen.toml`. The first run sets the baseline (epoch
  timestamps), so only later additions get the **new** badge, which lasts 7
  days (`newFor`). To test it, delete a line from `seen.toml`; keys are
  indented, so match with `^\s*`.
- **Wallpapers:** `Theme.Backgrounds()` (sorted file names). In the TUI,
  `w` opens the picker (`viewPicker`/`keyPicker`), `[`/`]` cycle, and
  `quickLook()` kills the previous `qlmanage` window first. The chosen index
  is passed to the apply and saved in state.

## Discover scanner

`omac scan --discover` / TUI tab 3:

- Walks `~/.config/*`, `~/Library/Application Support/*`,
  `~/Library/Preferences/*` and `~/.<app>`, two levels deep, up to 40 files
  per app, only files under 256 KB with config-like extensions.
- Scores theme-like keys (`theme`, `colorscheme`, `palette`, `flavor`,
  `accent` …), files with 4+ hex colors, and folders named
  `themes/colors/flavors/skins`.
- Skips apps that already have an adapter (by name, bin or owned paths),
  credential folders (`.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker`, password
  managers…) and files named like auth/token/secret/session/history.

## Testing safely

Never test against the real `$HOME` first. The pattern used during
development:

```sh
SP=/tmp/omac-test                    # any scratch dir
S=$SP/home; B=$SP/bin
mkdir -p $S/.cache && ln -s ~/.cache/omac $S/.cache/omac   # reuse the theme cache
# copy only the configs under test into $S (same relative paths), then cp -R $S $S.orig
# stub commands that would touch live apps:
mkdir -p $B; for c in pkill tmux herdr; do printf '#!/bin/sh\necho "%s $*" >> %s/calls.log\n' $c $B > $B/$c; chmod +x $B/$c; done

HOME=$S PATH=$B:$PATH omac apply tokyo-night --only <adapters> [--dry-run]
diff -ru $S.orig $S                  # edits should be minimal
HOME=$S PATH=$B:$PATH omac undo && diff -rq $S.orig $S   # should be identical
```

Exclude these from sandbox runs, because they always reach the real system:

- `wallpaper` and `macos` (osascript is global)
- `obsidian` (vault paths are absolute)

Checking output with the real tools:

```sh
ghostty +validate-config --config-file=<theme file>
tmux -L t -f /dev/null new-session -d \; source-file <file> \; kill-server
zsh -n <file>; plutil -lint <tmTheme>; glow -s <glow.json> README.md
FZF_DEFAULT_OPTS_FILE=<fzfrc> fzf --filter x </dev/null
python3 -c 'import tomllib,json …'   # TOML / JSON syntax
```

To try the TUI headless (bubbletea needs a real terminal), launch it from a
script inside `tmux -L omactui new-session -d -x 150 -y 40 <script>`, then
drive it with `send-keys` and read it with `capture-pane -p` (`-e` keeps
colors). Wrap env setup in a script: PATH has entries with spaces.

Sweep every theme for unresolved tokens:

```sh
for t in $(omac list | awk '{print ($1=="●")?$2:$1}'); do
  HOME=$S omac apply $t --only <adapters> | grep -E '✗|unresolved'; done
```

Unit tests (`go test ./...`) cover the editors (TOML multi-line strings,
table insertion, JSONC layout, YAML, kv, block idempotence) and the
renderer/resolver.

## Files omac owns

Personal, machine-specific notes (what was changed by hand on a given Mac)
go in `docs/LOCAL.md`, which git ignores.

Files and folders omac owns:

```
~/.config/omac/config.toml           extra_themes, disabled
~/.config/omac/adapters/             user adapters (override built-ins by name)
~/.config/omac/templates/            user templates (override by basename)
~/.config/omac/generated/            tmux.conf, fzfrc, glow.json
~/.config/omac/shell/*.zsh           sourced by the "shell" block in ~/.zshrc
~/.cache/omac/                       index.json + blobs/ (safe to delete)
~/.local/state/omac/current.toml     applied theme + wallpaper index
~/.local/state/omac/seen.toml        first-seen time per theme ("new" badges)
~/.local/state/omac/backups/         undo history
~/.local/share/omac/wallpapers/      converted wallpapers
```

## Gotchas

- **Bubble Tea batches fast key presses** into one message (`"jjj"`).
  `tui.key()` splits multi-rune messages. Keep that if you rework input.
- **Regex `\s*$` in multi-line mode eats newlines.** That made managed
  blocks re-write themselves on every apply. Use `[ \t]*$`.
- **hujson `Format()` restyles the whole file** (tabs, trailing commas).
  Don't use it. `layoutMember()` indents only the new member. Plain JSON
  files are re-indented with `json.Indent` using the file's own indent.
- **BurntSushi/toml marks keys decoded into `any` as "undecoded".** The
  unknown-key check exempts `steps.value*`.
- **The TOML editor is line based** and tracks `"""`/`'''` strings, so the
  `[` lines inside starship's multi-line `format` aren't treated as tables.
  Inline dotted keys inside a table (`a.b = 1` under `[x]`) aren't
  understood. Use table paths instead.
- **The macOS wallpaper store** (`Index.plist`) is undocumented. It has
  entries under `SystemDefault`, `Displays/<uuid>`,
  `Spaces/<uuid>/Default`, `Spaces/<uuid>/Displays/<uuid>`, each holding a
  `Desktop.Content.Choices[0].Configuration` binary plist
  `{type: imageFile, url: {relative: file://…}}`. Placement lives in
  `EncodedOptionValues` (left alone). WallpaperAgent reads it only at
  start. The Go round trip was confirmed lossless with Python's plistlib.
- **Don't send btop signals**: an unhandled signal would terminate it.
- **The Ghostty `theme =` set by omac** replaces a `dark:…,light:…` pair.
- **`omac apply --only x` still records state as the applied theme.**
- **The anonymous GitHub API allows 60 requests/hour.** The index needs 2
  per refresh; `gh auth token` is used when available.

## Ideas / not done

- Warp adapter (`~/.warp/themes/*.yaml` + `theme =` in
  `~/.warp/settings.toml`); use `omac scan --scaffold warp` as a start.
- Run `:colorscheme` in open nvim instances (sockets under `$TMPDIR/nvim.*`),
  instead of asking for a restart.
- Light/dark-aware Ghostty: write both `omarchy` and a paired theme, and
  keep the `dark:…,light:…` form.
- Inline wallpaper/preview images in the TUI (Kitty graphics protocol works
  in Ghostty). Today `p`/`P` open Quick Look.
- Undo for fish (universal vars aren't a file).
- Make the tmux template less opinionated (e.g. only colors, no layout).
- A Homebrew tap if it is ever shared.
