// omac applies Omarchy themes to macOS apps.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"omac/internal/adapter"
	"omac/internal/app"
	"omac/internal/scan"
	"omac/internal/theme"
	"omac/internal/tui"
	"omac/internal/xdg"
)

const usage = `omac — Omarchy themes on macOS

Usage:
  omac                         open the TUI
  omac list                    list themes (● marks the applied one)
  omac apply <theme> [flags]   apply a theme
      --wallpaper N            which background to use (default 0)
      --only a,b               only these adapters
      --dry-run                show what would change
  omac current                 show the applied theme
  omac undo                    restore files and settings from the last apply
  omac scan                    show which apps can be themed
  omac scan --discover         look for themeable apps without an adapter
  omac scan --scaffold <app>   write a starter adapter for a discovered app
  omac refresh                 re-download the theme list

Adapters: built-in, plus ~/.config/omac/adapters/*.toml (same name overrides).
Templates: ~/.config/omac/templates/<name> overrides any built-in or Omarchy template.
`

func main() {
	if app.LoadConfig(); app.ConfigErr != nil {
		fmt.Fprintln(os.Stderr, "omac: warning:", app.ConfigErr, "(using defaults)")
	}
	if len(os.Args) < 2 {
		check(tui.Run())
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "list", "ls":
		cmdList()
	case "apply":
		cmdApply(args)
	case "current":
		s := app.LoadState()
		if s.Theme == "" {
			fmt.Println("no theme applied by omac yet")
			return
		}
		fmt.Printf("%s (wallpaper %d, applied %s)\n", s.Theme, s.Wallpaper, s.Applied.Format("2006-01-02 15:04"))
	case "undo", "revert":
		adapters := loadAdapters()
		name, restored, err := app.Undo(adapters)
		for _, r := range restored {
			fmt.Println("  " + r)
		}
		check(err)
		fmt.Printf("undid %s\n", name)
	case "scan":
		cmdScan(args)
	case "refresh":
		ix, err := theme.LoadIndex(true, app.LoadConfig().ExtraThemes)
		check(err)
		fmt.Printf("%d themes\n", len(ix.Themes))
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "omac:", err)
		os.Exit(1)
	}
}

func loadAdapters() []*adapter.Adapter {
	adapters, errs := adapter.Load()
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "adapter error:", e)
	}
	return adapters
}

func loadIndex() *theme.Index {
	ix, err := theme.LoadIndex(false, app.LoadConfig().ExtraThemes)
	check(err)
	return ix
}

func cmdList() {
	ix := loadIndex()
	ix.LoadPalettes()
	cur := app.LoadState().Theme
	fresh := app.TrackSeen(ix.Themes)
	for _, t := range ix.Themes {
		mark := " "
		if t.Name == cur {
			mark = "●"
		}
		mode := ""
		if p, err := t.Palette(); err == nil {
			mode = p["mode"]
		}
		extra := ""
		if fresh[t.Name] {
			extra += "  new"
		}
		if t.Extra {
			extra += "  (" + t.Repo + ")"
		}
		fmt.Printf("%s %-20s %-5s %2d wallpapers%s\n", mark, t.Name, mode, len(t.Backgrounds()), extra)
	}
}

func cmdApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	wall := fs.Int("wallpaper", 0, "")
	only := fs.String("only", "", "")
	dry := fs.Bool("dry-run", false, "")
	// Accept the theme name before or after the flags.
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	fs.Parse(args)
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" {
		check(fmt.Errorf("usage: omac apply <theme>"))
	}
	ix := loadIndex()
	t := ix.Find(name)
	if t == nil {
		check(fmt.Errorf("no theme %q (see omac list)", name))
	}
	opt := app.Options{Wallpaper: *wall, DryRun: *dry, Progress: printResult}
	if *only != "" {
		opt.Only = strings.Split(*only, ",")
	}
	_, err := app.Apply(ix, t, loadAdapters(), app.LoadConfig(), opt)
	check(err)
	if !*dry {
		fmt.Printf("\napplied %s — `omac undo` reverts it\n", t.Name)
	}
}

func printResult(r adapter.Result) {
	icon := map[adapter.Status]string{adapter.OK: "✓", adapter.Unchanged: "=", adapter.Skipped: "·", adapter.Warned: "!", adapter.Failed: "✗"}[r.Status]
	line := fmt.Sprintf("%s %-10s", icon, r.Adapter.Name)
	notes := r.Notes
	if r.Err != nil {
		notes = append(notes, r.Err.Error())
	}
	if len(notes) > 0 {
		line += "  " + strings.Join(notes, "; ")
	}
	fmt.Println(line)
}

func cmdScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	discover := fs.Bool("discover", false, "")
	scaffold := fs.String("scaffold", "", "")
	fs.Parse(args)
	adapters := loadAdapters()

	if *discover || *scaffold != "" {
		cands := scan.Discover(adapters)
		if *scaffold != "" {
			for _, c := range cands {
				if strings.EqualFold(c.App, *scaffold) {
					p, err := scan.Scaffold(c)
					check(err)
					fmt.Println("wrote", xdg.Tilde(p))
					return
				}
			}
			check(fmt.Errorf("%q is not among the discovered apps", *scaffold))
		}
		if len(cands) == 0 {
			fmt.Println("no unadapted themeable apps found")
			return
		}
		fmt.Println("Apps that look themeable but have no adapter (omac scan --scaffold <app>):")
		for _, c := range cands {
			brew := ""
			if c.Brew {
				brew = " [brew]"
			}
			fmt.Printf("\n%s%s  score %d  %s\n", c.App, brew, c.Score, xdg.Tilde(c.Dir))
			for i, e := range c.Evidence {
				if i == 4 {
					fmt.Printf("    … %d more\n", len(c.Evidence)-4)
					break
				}
				fmt.Println("    " + e)
			}
		}
		return
	}

	cfg := app.LoadConfig()
	fmt.Printf("%-10s %-8s %-10s %s\n", "APP", "SUPPORT", "STATUS", "DETECTED / DESCRIPTION")
	for _, a := range adapters {
		ok, via := a.Installed()
		status := "missing"
		if ok {
			status = "installed"
		}
		if cfg.IsDisabled(a.Name) {
			status = "disabled"
		}
		src := ""
		if a.Source != "built-in" {
			src = " [" + xdg.Tilde(a.Source) + "]"
		}
		detail := a.Description
		if ok && via != "always" {
			detail = via + " — " + detail
		}
		fmt.Printf("%-10s %-8s %-10s %s%s\n", a.Name, a.Support, status, detail, src)
	}
}
