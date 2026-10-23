// Package tui is the interactive theme picker.
package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"omac/internal/adapter"
	"omac/internal/app"
	"omac/internal/scan"
	"omac/internal/theme"
	"omac/internal/xdg"
)

type tab int

const (
	tabThemes tab = iota
	tabApps
	tabDiscover
)

type phase int

const (
	phaseLoading phase = iota
	phaseReady
	phaseApplying
	phaseResults
)

type model struct {
	ix       *theme.Index
	adapters []*adapter.Adapter
	loadErrs []error
	cfg      app.Config
	state    app.State

	tab   tab
	phase phase
	spin  spinner.Model
	w, h  int
	err   error
	flash string

	// themes
	filtered  []*theme.Theme
	cursor    int
	offset    int
	filter    string
	filtering bool
	wallpaper map[string]int
	fresh     map[string]bool // themes that appeared upstream in the last week

	// wallpaper picker (Themes tab, w)
	picking    bool
	pickCursor int

	// apply / undo results
	resultsTitle string
	results      []adapter.Result
	lines        []string // free-form result lines (undo)
	progress     chan adapter.Result

	// apps tab
	appCursor int
	installed map[string]string // name -> detected via ("" = missing)

	// discover tab
	cands      []scan.Candidate
	candCursor int
	scanned    bool
	scanning   bool
}

type loadedMsg struct {
	fresh    map[string]bool
	ix       *theme.Index
	adapters []*adapter.Adapter
	errs     []error
	err      error
}
type resultMsg adapter.Result
type applyDoneMsg struct{ err error }
type undoMsg struct {
	name  string
	lines []string
	err   error
}
type scanMsg []scan.Candidate

func Run() error {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	m := &model{spin: s, wallpaper: map[string]int{}, cfg: app.LoadConfig(), state: app.LoadState()}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func load(force bool) tea.Cmd {
	return func() tea.Msg {
		cfg := app.LoadConfig()
		ix, err := theme.LoadIndex(force, cfg.ExtraThemes)
		if err != nil {
			return loadedMsg{err: err}
		}
		ix.LoadPalettes()
		adapters, errs := adapter.Load()
		return loadedMsg{ix: ix, adapters: adapters, errs: errs, fresh: app.TrackSeen(ix.Themes)}
	}
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.spin.Tick, load(false)) }

func (m *model) selected() *theme.Theme {
	if m.cursor < len(m.filtered) {
		return m.filtered[m.cursor]
	}
	return nil
}

func (m *model) applyFilter() {
	m.filtered = m.filtered[:0]
	for _, t := range m.ix.Themes {
		if strings.Contains(t.Name, strings.ToLower(m.filter)) {
			m.filtered = append(m.filtered, t)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m *model) detect() {
	m.installed = map[string]string{}
	for _, a := range m.adapters {
		if ok, via := a.Installed(); ok {
			m.installed[a.Name] = via
		}
	}
}

func (m *model) startApply(dry bool) tea.Cmd {
	t := m.selected()
	if t == nil {
		return nil
	}
	m.phase = phaseApplying
	m.results, m.lines = nil, nil
	verb := "Applied"
	if dry {
		verb = "Dry run of"
	}
	m.resultsTitle = fmt.Sprintf("%s %s", verb, t.Name)
	ch := make(chan adapter.Result)
	m.progress = ch
	ix, adapters, cfg, wall := m.ix, m.adapters, m.cfg, m.wallpaper[t.Name]
	go func() {
		_, err := app.Apply(ix, t, adapters, cfg, app.Options{Wallpaper: wall, DryRun: dry, Progress: func(r adapter.Result) { ch <- r }})
		if err != nil {
			ch <- adapter.Result{Status: adapter.Failed, Err: err}
		}
		close(ch)
	}()
	return m.waitProgress()
}

func (m *model) waitProgress() tea.Cmd {
	ch := m.progress
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return applyDoneMsg{}
		}
		return resultMsg(r)
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.ix, m.adapters, m.loadErrs, m.fresh = msg.ix, msg.adapters, msg.errs, msg.fresh
		m.filtered = append([]*theme.Theme(nil), m.ix.Themes...)
		m.detect()
		m.phase = phaseReady
		if m.state.Theme != "" {
			for i, t := range m.filtered {
				if t.Name == m.state.Theme {
					m.cursor = i
					m.wallpaper[t.Name] = m.state.Wallpaper
				}
			}
		}
		return m, nil
	case resultMsg:
		r := adapter.Result(msg)
		if r.Adapter == nil {
			m.err = r.Err
		} else {
			m.results = append(m.results, r)
		}
		return m, m.waitProgress()
	case applyDoneMsg:
		m.phase = phaseResults
		m.state = app.LoadState()
		return m, nil
	case undoMsg:
		m.phase = phaseResults
		m.results = nil
		m.lines = msg.lines
		m.resultsTitle = "Undid " + msg.name
		if msg.err != nil {
			m.lines = append(m.lines, "error: "+msg.err.Error())
		}
		m.state = app.LoadState()
		return m, nil
	case scanMsg:
		m.cands, m.scanning, m.scanned = msg, false, true
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Fast typing or key repeat delivers several runes in one message.
	if k.Type == tea.KeyRunes && len(k.Runes) > 1 && !m.filtering && !k.Paste {
		var cmds []tea.Cmd
		for _, r := range k.Runes {
			_, cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.phase {
	case phaseLoading:
		if s == "q" {
			return m, tea.Quit
		}
		return m, nil
	case phaseApplying:
		return m, nil
	case phaseResults:
		m.phase = phaseReady
		m.detect()
		return m, nil
	}

	if m.filtering {
		switch s {
		case "esc":
			m.filtering, m.filter = false, ""
		case "enter":
			m.filtering = false
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
			}
		default:
			if len(k.Runes) > 0 {
				m.filter += string(k.Runes)
			}
		}
		m.applyFilter()
		return m, nil
	}

	m.flash = ""
	if m.picking {
		return m.keyPicker(s)
	}
	switch s {
	case "q":
		return m, tea.Quit
	case "tab", "right", "l":
		return m, m.switchTab((m.tab + 1) % 3)
	case "shift+tab", "left", "h":
		return m, m.switchTab((m.tab + 2) % 3)
	case "1":
		return m, m.switchTab(tabThemes)
	case "2":
		return m, m.switchTab(tabApps)
	case "3":
		return m, m.switchTab(tabDiscover)
	case "u":
		m.phase = phaseApplying
		m.resultsTitle = "Undoing"
		adapters := m.adapters
		return m, func() tea.Msg {
			name, lines, err := app.Undo(adapters)
			return undoMsg{name, lines, err}
		}
	}

	switch m.tab {
	case tabThemes:
		return m.keyThemes(s)
	case tabApps:
		return m.keyApps(s)
	case tabDiscover:
		return m.keyDiscover(s)
	}
	return m, nil
}

func (m *model) switchTab(t tab) tea.Cmd {
	m.tab = t
	if t == tabDiscover && !m.scanned && !m.scanning {
		m.scanning = true
		adapters := m.adapters
		return func() tea.Msg { return scanMsg(scan.Discover(adapters)) }
	}
	return nil
}

func (m *model) keyThemes(s string) (tea.Model, tea.Cmd) {
	t := m.selected()
	switch s {
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(m.filtered)-1, m.cursor+1)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.filtered) - 1
	case "/":
		m.filtering = true
	case "esc":
		m.filter = ""
		m.applyFilter()
	case "enter":
		return m, m.startApply(false)
	case "d":
		return m, m.startApply(true)
	case "w":
		if t != nil {
			if n := len(t.Backgrounds()); n > 0 {
				m.picking = true
				m.pickCursor = m.wallpaper[t.Name] % n
			} else {
				m.flash = t.Name + " has no wallpapers"
			}
		}
	case "]", "[":
		if t != nil {
			if n := len(t.Backgrounds()); n > 0 {
				step := 1
				if s == "[" {
					step = n - 1
				}
				m.wallpaper[t.Name] = (m.wallpaper[t.Name] + step) % n
			}
		}
	case "n":
		for i := 1; i <= len(m.filtered); i++ {
			j := (m.cursor + i) % len(m.filtered)
			if m.fresh[m.filtered[j].Name] {
				m.cursor = j
				break
			}
		}
	case "p":
		if t != nil {
			if bgs := t.Backgrounds(); len(bgs) > 0 {
				return m, m.quickLook(t, bgs[m.wallpaper[t.Name]%len(bgs)])
			}
		}
	case "P":
		if t != nil && t.Has("preview.png") {
			return m, m.quickLook(t, "preview.png")
		}
	case "r":
		m.phase = phaseLoading
		return m, tea.Batch(m.spin.Tick, load(true))
	}
	return m, nil
}

// quickLook downloads a theme file and shows it in a Quick Look window,
// replacing the previous one so browsing doesn't pile up windows.
func (m *model) quickLook(t *theme.Theme, rel string) tea.Cmd {
	m.flash = "opening " + filepath.Base(rel) + "…"
	return func() tea.Msg {
		if p, err := t.Fetch(rel); err == nil {
			_ = exec.Command("pkill", "-x", "qlmanage").Run()
			_ = exec.Command("qlmanage", "-p", p).Start()
		}
		return nil
	}
}

func (m *model) keyPicker(s string) (tea.Model, tea.Cmd) {
	t := m.selected()
	if t == nil {
		m.picking = false
		return m, nil
	}
	bgs := t.Backgrounds()
	switch s {
	case "up", "k":
		m.pickCursor = max(0, m.pickCursor-1)
	case "down", "j":
		m.pickCursor = min(len(bgs)-1, m.pickCursor+1)
	case "g", "home":
		m.pickCursor = 0
	case "G", "end":
		m.pickCursor = len(bgs) - 1
	case "esc", "w", "q":
		m.picking = false
	case "enter":
		m.wallpaper[t.Name] = m.pickCursor
		m.picking = false
	case "a":
		m.wallpaper[t.Name] = m.pickCursor
		m.picking = false
		return m, m.startApply(false)
	case " ", "p":
		if m.pickCursor < len(bgs) {
			return m, m.quickLook(t, bgs[m.pickCursor])
		}
	}
	return m, nil
}

func (m *model) keyApps(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "up", "k":
		m.appCursor = max(0, m.appCursor-1)
	case "down", "j":
		m.appCursor = min(len(m.adapters)-1, m.appCursor+1)
	case " ", "x":
		a := m.adapters[m.appCursor]
		m.cfg.Toggle(a.Name)
		if err := m.cfg.Save(); err != nil {
			m.flash = "saving config: " + err.Error()
		}
	}
	return m, nil
}

func (m *model) keyDiscover(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "up", "k":
		m.candCursor = max(0, m.candCursor-1)
	case "down", "j":
		m.candCursor = min(len(m.cands)-1, m.candCursor+1)
	case "r":
		m.scanned = false
		return m, m.switchTab(tabDiscover)
	case "enter", "n":
		if m.candCursor < len(m.cands) {
			p, err := scan.Scaffold(m.cands[m.candCursor])
			if err != nil {
				m.flash = err.Error()
			} else {
				m.flash = "wrote " + xdg.Tilde(p) + " — edit it, then it shows up in Apps"
				m.adapters, m.loadErrs = adapter.Load()
				m.detect()
			}
		}
	}
	return m, nil
}

// ---------- view ----------

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#5a4fcf", Dark: "#9d8cff"}
	cDim    = lipgloss.AdaptiveColor{Light: "#777777", Dark: "#7a7a8c"}
	cOK     = lipgloss.Color("#4fbf73")
	cWarn   = lipgloss.Color("#e0af3c")
	cErr    = lipgloss.Color("#e05561")

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sSel    = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sTabOn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(cAccent).Padding(0, 1)
	sTabOff = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cDim).Padding(0, 1)
)

func (m *model) View() string {
	if m.err != nil {
		return fmt.Sprintf("\n  %s %v\n\n  press q to quit\n", lipgloss.NewStyle().Foreground(cErr).Render("error:"), m.err)
	}
	if m.phase == phaseLoading {
		return fmt.Sprintf("\n  %s loading Omarchy themes…\n", m.spin.View())
	}
	if m.w == 0 {
		return ""
	}

	header := m.header()
	footer := m.footer()
	bodyH := max(5, m.h-lipgloss.Height(header)-lipgloss.Height(footer))

	var body string
	switch {
	case m.phase == phaseApplying || m.phase == phaseResults:
		body = m.viewResults(bodyH)
	case m.tab == tabThemes:
		body = m.viewThemes(bodyH)
	case m.tab == tabApps:
		body = m.viewApps(bodyH)
	default:
		body = m.viewDiscover(bodyH)
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *model) header() string {
	tabs := []string{"1 Themes", "2 Apps", "3 Discover"}
	var parts []string
	for i, t := range tabs {
		if tab(i) == m.tab {
			parts = append(parts, sTabOn.Render(t))
		} else {
			parts = append(parts, sTabOff.Render(t))
		}
	}
	cur := "none"
	if m.state.Theme != "" {
		cur = m.state.Theme
	}
	right := sDim.Render("current: ") + sSel.Render(cur)
	left := sTitle.Render(" omac ") + strings.Join(parts, " ")
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return left + strings.Repeat(" ", gap) + right + "\n"
}

func (m *model) footer() string {
	var keys string
	switch {
	case m.phase == phaseResults:
		keys = "any key: back"
	case m.phase == phaseApplying:
		keys = m.spin.View() + " working…"
	case m.filtering:
		keys = "filter: " + m.filter + "█   enter: keep  esc: clear"
	case m.picking:
		keys = "↑↓ move  enter choose  a choose + apply  space/p Quick Look  esc back"
	case m.tab == tabThemes:
		keys = "↑↓ move  enter apply  d dry-run  w wallpapers  [/] cycle  p/P Quick Look  n next new  / filter  u undo  r refresh  q quit"
	case m.tab == tabApps:
		keys = "↑↓ move  space enable/disable  tab switch  u undo  q quit"
	default:
		keys = "↑↓ move  enter write starter adapter  r rescan  q quit"
	}
	out := sDim.Render(" " + keys)
	if m.flash != "" {
		out = " " + lipgloss.NewStyle().Foreground(cWarn).Render(m.flash) + "\n" + out
	}
	if app.ConfigErr != nil && m.phase == phaseReady {
		out = " " + lipgloss.NewStyle().Foreground(cErr).Render(app.ConfigErr.Error()) + "\n" + out
	}
	if len(m.loadErrs) > 0 && m.phase == phaseReady {
		out = " " + lipgloss.NewStyle().Foreground(cErr).Render(fmt.Sprintf("adapter error: %v", m.loadErrs[0])) + "\n" + out
	}
	return out
}

func swatch(hex string) string {
	if hex == "" {
		return "  "
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("██")
}

func (m *model) viewThemes(h int) string {
	listW := 36
	// keep the cursor visible
	visible := h - 1
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	var rows []string
	for i := m.offset; i < len(m.filtered) && i < m.offset+visible; i++ {
		t := m.filtered[i]
		mark := "  "
		if t.Name == m.state.Theme {
			mark = lipgloss.NewStyle().Foreground(cOK).Render("● ")
		}
		name := fmt.Sprintf("%-18.18s", t.Name)
		if i == m.cursor {
			name = sSel.Render("▸" + name[:17])
		}
		sw := ""
		if p, err := t.Palette(); err == nil {
			for _, k := range []string{"background", "accent", "red", "green", "yellow", "blue", "magenta"} {
				sw += lipgloss.NewStyle().Foreground(lipgloss.Color(p[k])).Render("▆")
			}
		}
		badge := ""
		if m.fresh[t.Name] {
			badge = " " + lipgloss.NewStyle().Foreground(cOK).Bold(true).Render("new")
		}
		rows = append(rows, mark+name+" "+sw+badge)
	}
	if len(m.filtered) == 0 {
		rows = append(rows, sDim.Render("  no theme matches"))
	}
	list := lipgloss.NewStyle().Width(listW).Render(strings.Join(rows, "\n"))
	var preview string
	if m.picking {
		preview = m.viewPicker(max(30, m.w-listW-2), h)
	} else {
		preview = m.preview(max(30, m.w-listW-2), h)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, list, preview)
}

func (m *model) preview(w, h int) string {
	t := m.selected()
	if t == nil {
		return ""
	}
	p, err := t.Palette()
	if err != nil {
		return sBox.Width(w - 4).Render("could not load colors: " + err.Error())
	}
	bg := lipgloss.Color(p["background"])
	seg := func(color, text string, bold bool) string {
		return lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(p[color])).Bold(bold).Render(text)
	}
	cardW := min(w-2, 64)
	line := func(parts ...string) string {
		s := strings.Join(parts, "")
		pad := max(0, cardW-lipgloss.Width(s))
		return s + lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", pad))
	}
	card := []string{
		line(),
		line(seg("accent", "  ~/code/omac", true), seg("muted", " on ", false), seg("magenta", " main", false), seg("green", " ❯ ", true), seg("foreground", "omac apply "+t.Name, false)),
		line(),
		line(seg("magenta", "  func ", false), seg("blue", "render", false), seg("foreground", "(p ", false), seg("yellow", "Palette", false), seg("foreground", ") ", false), seg("yellow", "string", false), seg("foreground", " {", false)),
		line(seg("dark_foreground", "      // mix the accent into the background", false)),
		line(seg("foreground", "      return ", false), seg("blue", "Mix", false), seg("foreground", "(p.bg, ", false), seg("green", "\"accent\"", false), seg("foreground", ", ", false), seg("orange", "0.35", false), seg("foreground", ")", false)),
		line(seg("foreground", "  }", false)),
		line(),
		line(seg("green", "  ✓ passed", false), seg("yellow", "   ! warning", false), seg("red", "   ✗ failed", false), seg("cyan", "   ℹ info", false)),
		line(),
	}

	var ansi, bright string
	for _, k := range []string{"background", "red", "green", "yellow", "blue", "magenta", "cyan", "foreground"} {
		ansi += swatch(p[k]) + " "
	}
	for _, k := range []string{"muted", "bright_red", "bright_green", "bright_yellow", "bright_blue", "bright_magenta", "bright_cyan", "bright_foreground"} {
		bright += swatch(p[k]) + " "
	}
	named := ""
	for _, k := range []string{"accent", "selection", "dark_background", "lighter_background"} {
		named += swatch(p[k]) + sDim.Render(" "+strings.ReplaceAll(k, "_background", "_bg")+"  ")
	}

	bgs := t.Backgrounds()
	wall := sDim.Render("no wallpapers")
	if len(bgs) > 0 {
		i := m.wallpaper[t.Name] % len(bgs)
		wall = fmt.Sprintf("%s %d/%d  %s  %s", sDim.Render("wallpaper"), i+1, len(bgs), filepath.Base(bgs[i]), sDim.Render("(w to choose)"))
	}
	src := t.Repo
	meta := fmt.Sprintf("%s  %s  %s", sTitle.Render(t.Name), sDim.Render(p["mode"]), sDim.Render(src))
	if m.fresh[t.Name] {
		meta += "  " + lipgloss.NewStyle().Foreground(cOK).Bold(true).Render("new in Omarchy")
	}

	installed := 0
	for _, a := range m.adapters {
		if m.installed[a.Name] != "" && !m.cfg.IsDisabled(a.Name) {
			installed++
		}
	}

	out := []string{
		meta,
		"",
		strings.Join(card, "\n"),
		"",
		ansi + sDim.Render(" normal"),
		bright + sDim.Render(" bright"),
		named,
		"",
		wall,
		sDim.Render(fmt.Sprintf("enter applies to %d apps (see Apps tab)", installed)),
	}
	return lipgloss.NewStyle().Width(w).MaxHeight(h).Render(strings.Join(out, "\n"))
}

func (m *model) viewPicker(w, h int) string {
	t := m.selected()
	bgs := t.Backgrounds()
	chosen := m.wallpaper[t.Name] % max(1, len(bgs))
	rows := []string{
		sTitle.Render(t.Name) + sDim.Render(fmt.Sprintf("  %d wallpapers", len(bgs))),
		"",
	}
	start := max(0, m.pickCursor-(h-6))
	for i := start; i < len(bgs) && len(rows) < h-3; i++ {
		mark := "  "
		if i == chosen {
			mark = lipgloss.NewStyle().Foreground(cOK).Render("● ")
		}
		name := fmt.Sprintf("%2d  %s", i+1, filepath.Base(bgs[i]))
		if i == m.pickCursor {
			rows = append(rows, sSel.Render("▸ ")+mark+sSel.Render(name))
		} else {
			rows = append(rows, "  "+mark+name)
		}
	}
	rows = append(rows, "", sDim.Render("● = chosen for the next apply   space opens Quick Look"))
	return lipgloss.NewStyle().Width(w).MaxHeight(h).Render(strings.Join(rows, "\n"))
}

func statusIcon(s adapter.Status) string {
	switch s {
	case adapter.OK:
		return lipgloss.NewStyle().Foreground(cOK).Render("✓")
	case adapter.Unchanged:
		return sDim.Render("=")
	case adapter.Skipped:
		return sDim.Render("·")
	case adapter.Warned:
		return lipgloss.NewStyle().Foreground(cWarn).Render("!")
	}
	return lipgloss.NewStyle().Foreground(cErr).Render("✗")
}

func (m *model) viewResults(h int) string {
	title := sTitle.Render(m.resultsTitle)
	if m.phase == phaseApplying {
		title = m.spin.View() + " " + title + "…"
	}
	rows := []string{title, ""}
	for _, r := range m.results {
		notes := r.Notes
		if r.Err != nil {
			notes = append(notes, r.Err.Error())
		}
		name := fmt.Sprintf("%-10s", r.Adapter.Name)
		text := strings.Join(notes, "; ")
		if r.Status == adapter.Skipped || r.Status == adapter.Unchanged {
			name, text = sDim.Render(name), sDim.Render(text)
		}
		rows = append(rows, fmt.Sprintf(" %s %s %s", statusIcon(r.Status), name, text))
	}
	for _, l := range m.lines {
		rows = append(rows, " "+l)
	}
	if m.phase == phaseResults && len(m.results) > 0 {
		rows = append(rows, "", sDim.Render(" u undoes this apply"))
	}
	return lipgloss.NewStyle().Width(m.w).MaxWidth(m.w).Render(strings.Join(rows, "\n"))
}

func (m *model) viewApps(h int) string {
	rows := []string{sDim.Render(fmt.Sprintf("  %-3s %-11s %-8s %-10s %s", "on", "app", "support", "status", "what it themes"))}
	start := max(0, m.appCursor-(h-3))
	for i := start; i < len(m.adapters) && len(rows) < h-1; i++ {
		a := m.adapters[i]
		on := "[x]"
		if m.cfg.IsDisabled(a.Name) {
			on = "[ ]"
		}
		status := lipgloss.NewStyle().Foreground(cOK).Render(fmt.Sprintf("%-10s", "installed"))
		if m.installed[a.Name] == "" {
			status = sDim.Render(fmt.Sprintf("%-10s", "missing"))
		}
		support := fmt.Sprintf("%-8s", a.Support)
		if a.Partial() {
			support = lipgloss.NewStyle().Foreground(cWarn).Render(support)
		}
		name := fmt.Sprintf("%-11s", a.Name)
		cursor := "  "
		if i == m.appCursor {
			cursor = sSel.Render("▸ ")
			name = sSel.Render(name)
		}
		desc := a.Description
		if a.Source != "built-in" {
			desc += sDim.Render(" (user)")
		}
		rows = append(rows, cursor+on+" "+name+" "+support+" "+status+" "+desc)
	}
	if m.appCursor < len(m.adapters) {
		a := m.adapters[m.appCursor]
		src := "built-in"
		if a.Source != "built-in" {
			src = xdg.Tilde(a.Source)
		}
		detail := fmt.Sprintf("source: %s", src)
		if via := m.installed[a.Name]; via != "" && via != "always" {
			detail += "   detected: " + via
		}
		rows = append(rows, "", sDim.Render("  "+detail))
	}
	return lipgloss.NewStyle().MaxWidth(m.w).Render(strings.Join(rows, "\n"))
}

func (m *model) viewDiscover(h int) string {
	if m.scanning {
		return "\n  " + m.spin.View() + " scanning ~/.config, ~/Library and dotfiles…"
	}
	if len(m.cands) == 0 {
		return "\n  " + sDim.Render("no themeable apps without an adapter found")
	}
	listW := 30
	var rows []string
	rows = append(rows, sDim.Render("  themeable, no adapter yet"))
	start := max(0, m.candCursor-(h-3))
	for i := start; i < len(m.cands) && len(rows) < h; i++ {
		c := m.cands[i]
		name := fmt.Sprintf("%-20.20s %3d", c.App, c.Score)
		if i == m.candCursor {
			rows = append(rows, sSel.Render("▸ "+name))
		} else {
			rows = append(rows, "  "+name)
		}
	}
	list := lipgloss.NewStyle().Width(listW).Render(strings.Join(rows, "\n"))

	c := m.cands[m.candCursor]
	detail := []string{sTitle.Render(c.App), sDim.Render(xdg.Tilde(c.Dir))}
	if c.Brew {
		detail = append(detail, sDim.Render("installed with Homebrew"))
	}
	detail = append(detail, "", "evidence:")
	for _, e := range c.Evidence {
		detail = append(detail, "  "+e)
	}
	detail = append(detail, "", sDim.Render("enter writes ~/.config/omac/adapters/"+strings.ToLower(c.App)+".toml to start from"))
	right := lipgloss.NewStyle().Width(max(30, m.w-listW-2)).MaxHeight(h).Render(strings.Join(detail, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, list, right)
}
