# omac

Apply [Omarchy](https://github.com/basecamp/omarchy) themes to macOS: wallpaper,
light/dark mode, Ghostty, Neovim, btop, Claude Code, opencode, Zed, tmux,
lazygit, starship, herdr, yazi, zsh, fzf, Codex, Gemini, glow, fastfetch,
cliamp, Obsidian (plus kitty, Alacritty, Helix and fish when installed).

Themes come straight from the Omarchy repo on GitHub. Each theme's
`colors.toml` is rendered through Omarchy's own templates where they exist
(`default/themed/*.tpl`), and through omac's built-in templates for apps
Omarchy doesn't cover.

Working on omac itself? Start with [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).

## Use

```sh
make install          # builds ~/.local/bin/omac
omac                  # TUI: pick a theme, enter to apply, u to undo
omac apply tokyo-night [--wallpaper 2] [--only ghostty,btop] [--dry-run]
omac undo             # restore every file + wallpaper + dark mode from the last apply
omac scan             # which apps can be themed, installed or not
omac scan --discover  # themeable-looking apps that have no adapter yet
omac scan --scaffold superfile   # write a starter adapter for one of them
```

TUI keys: `↑↓/jk` move, `enter` apply, `d` dry run, `w` wallpaper picker
(`enter` choose, `a` choose + apply, `space` Quick Look), `[`/`]` cycle
wallpapers, `p` Quick Look the wallpaper, `P` the theme preview, `n` next
new theme, `/` filter, `u` undo, `r` refresh themes, `1/2/3` or `tab` switch
Themes / Apps / Discover.

The theme list comes from Omarchy's repo and refreshes daily (or `r` /
`omac refresh`). Themes Omarchy adds later get a **new** badge for a week,
in the TUI and in `omac list`. In Apps,
`space` enables/disables an adapter. In Discover, `enter` writes a starter adapter.

## Wallpaper on every Space

macOS only offers an API for the current Space, so after setting the picture
omac also writes it into every Space in
`~/Library/Application Support/com.apple.wallpaper/Store/Index.plist` (keeping
each Space's fit/crop option) and restarts `WallpaperAgent`. The store is
backed up like any other file, so `omac undo` restores the previous pictures.

## Starship

Segments in `~/.config/starship.toml` use palette names (`seg_os`, `seg_dir`,
`seg_git`, `seg_lang`, `seg_text`, `ok`, `err`, `vi`, `accent`). omac fills
`[palettes.omarchy]` and selects it; `[palettes.original]` keeps the colors
from before omac.

## Safety

Every apply snapshots each file before its first change (and the current
wallpaper and appearance) under `~/.local/state/omac/backups/`; `omac undo`
puts them back, deletes files omac created, and reloads Ghostty/tmux/herdr.
Config edits change only the keys involved: TOML is edited line by line,
JSON/JSONC through hujson (comments kept), YAML through yaml.v3 nodes, and
shell/tmux config through marked blocks (`# >>> omac:<id> >>>`).

## Adding an app

Drop a TOML file in `~/.config/omac/adapters/` (same name as a built-in
overrides it). `assets/adapters/` has 26 examples.

```toml
name = "myapp"
description = "What gets themed"
support = "full"          # or "partial"
hint = "Restart myapp."   # shown after apply

[detect]                  # any match = installed
bins  = ["myapp"]
apps  = ["MyApp.app"]
paths = ["~/.config/myapp"]

[[steps]]                 # render a template to a file
kind = "write"
template = "myapp.tpl"    # ~/.config/omac/templates/, built-in, or "omarchy:<name>.tpl"
theme_file = "myapp.conf" # optional: use this file verbatim if the theme ships it
target = "~/.config/myapp/themes/omarchy.toml"

[[steps]]                 # set keys in a config, keeping everything else
kind = "set"
file = "~/.config/myapp/config.toml"   # format from extension, or format = toml|json|yaml|kv
values = { "ui.theme" = "omarchy", "ui.accent" = "{{ accent }}" }

[[steps]]                 # reload command; reload = true also runs it after undo
kind = "cmd"
run = "pkill -USR1 -x myapp"
ignore_error = true
reload = true
```

Other kinds: `block` (managed block in a text file: `file`, `id`, `content`),
`wallpaper`, `appearance`. `targets_cmd` runs a write/set step once per
directory the command prints, available as `{{ target_dir }}`.

### Template tokens

Any `colors.toml` name (`background`, `foreground`, `accent`, `selection`,
`muted`, `red` … `bright_magenta`, `dark_background`, `lighter_background`,
`cursor`, `mode` …) with Omarchy's derived fallbacks, plus:

| token | example |
|---|---|
| `{{ accent }}` | `#7aa2f7` |
| `{{ accent_strip }}` | `7aa2f7` |
| `{{ accent_rgb }}` | `122,162,247` |
| `{{ accent_sgr }}` | `38;2;122;162;247` |
| `{{ mix background accent 20% }}` | blended hex (`mix_strip`, `mix_rgb` too) |
| `{{ home }}` `{{ generated }}` `{{ theme_name }}` | paths and the theme name |

## Config

`~/.config/omac/config.toml`:

```toml
extra_themes = ["someone/omarchy-cool-theme"]  # community theme repos
disabled = ["obsidian"]                        # adapters to skip
```

## Credits and license

omac is MIT licensed (see [LICENSE](LICENSE)).

Themes, wallpapers and the `omarchy:` templates come from
[Omarchy](https://github.com/basecamp/omarchy) by Basecamp (MIT). omac
downloads them at run time and doesn't bundle them. The color resolution in
`internal/theme/colors.go` is a port of Omarchy's `omarchy-theme-color`.
Wallpapers belong to their respective artists. omac is not affiliated with
Basecamp or Omarchy.
