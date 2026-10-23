package theme

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"omac/internal/xdg"
)

const (
	OmarchyRepo = "basecamp/omarchy"
	indexTTL    = 24 * time.Hour
)

// Theme is one theme folder in a GitHub repo.
type Theme struct {
	Name   string            `json:"name"`
	Repo   string            `json:"repo"`
	Branch string            `json:"branch"`
	Dir    string            `json:"dir"`   // folder inside the repo ("" for repo-root themes)
	Files  map[string]string `json:"files"` // path relative to Dir -> blob sha
	Extra  bool              `json:"extra"` // came from extra_themes rather than Omarchy

	palette Palette
}

// Entry is a single file in a repo, such as an Omarchy template.
type Entry struct {
	Repo, Branch, Path, Sha string
}

type Index struct {
	Fetched   time.Time        `json:"fetched"`
	Themes    []*Theme         `json:"themes"`
	Templates map[string]Entry `json:"templates"` // "ghostty.conf.tpl" -> entry
}

// LoadIndex returns the cached theme index, refreshing it from GitHub when it
// is older than a day or force is set. A stale cache is used if GitHub fails.
func LoadIndex(force bool, extraRepos []string) (*Index, error) {
	cachePath := filepath.Join(xdg.CacheDir(), "index.json")
	var cached *Index
	if data, err := os.ReadFile(cachePath); err == nil {
		var ix Index
		if json.Unmarshal(data, &ix) == nil {
			cached = &ix
		}
	}
	if cached != nil && !force && time.Since(cached.Fetched) < indexTTL && sameExtras(cached, extraRepos) {
		return cached, nil
	}
	ix, err := fetchIndex(extraRepos)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, err
	}
	data, _ := json.MarshalIndent(ix, "", " ")
	_ = xdg.WriteFileAtomic(cachePath, data)
	return ix, nil
}

func sameExtras(ix *Index, repos []string) bool {
	have := map[string]bool{}
	for _, t := range ix.Themes {
		if t.Extra {
			have[t.Repo] = true
		}
	}
	for _, r := range repos {
		if !have[normalizeRepo(r)] {
			return false
		}
	}
	return true
}

func normalizeRepo(r string) string {
	r = strings.TrimSuffix(strings.TrimSpace(r), ".git")
	r = strings.TrimPrefix(r, "https://")
	r = strings.TrimPrefix(r, "github.com/")
	return strings.Trim(r, "/")
}

type treeResp struct {
	Tree []struct {
		Path, Type, Sha string
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

func fetchTree(repo string) (branch string, tr treeResp, err error) {
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err = githubJSON("https://api.github.com/repos/"+repo, &info); err != nil {
		return
	}
	branch = info.DefaultBranch
	err = githubJSON(fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", repo, url.PathEscape(branch)), &tr)
	return
}

func fetchIndex(extraRepos []string) (*Index, error) {
	branch, tr, err := fetchTree(OmarchyRepo)
	if err != nil {
		return nil, fmt.Errorf("listing Omarchy themes: %w", err)
	}
	ix := &Index{Fetched: time.Now(), Templates: map[string]Entry{}}
	byName := map[string]*Theme{}
	for _, e := range tr.Tree {
		if e.Type != "blob" {
			continue
		}
		if rest, ok := strings.CutPrefix(e.Path, "themes/"); ok {
			name, rel, ok := strings.Cut(rest, "/")
			if !ok {
				continue
			}
			t := byName[name]
			if t == nil {
				t = &Theme{Name: name, Repo: OmarchyRepo, Branch: branch, Dir: "themes/" + name, Files: map[string]string{}}
				byName[name] = t
			}
			t.Files[rel] = e.Sha
		} else if rest, ok := strings.CutPrefix(e.Path, "default/themed/"); ok && strings.HasSuffix(rest, ".tpl") && !strings.Contains(rest, "/") {
			ix.Templates[rest] = Entry{Repo: OmarchyRepo, Branch: branch, Path: e.Path, Sha: e.Sha}
		}
	}
	for _, t := range byName {
		if _, ok := t.Files["colors.toml"]; ok {
			ix.Themes = append(ix.Themes, t)
		}
	}

	for _, r := range extraRepos {
		repo := normalizeRepo(r)
		b, tr, err := fetchTree(repo)
		if err != nil {
			continue
		}
		name := path.Base(repo)
		name = strings.TrimPrefix(name, "omarchy-")
		name = strings.TrimSuffix(name, "-theme")
		t := &Theme{Name: name, Repo: repo, Branch: b, Files: map[string]string{}, Extra: true}
		for _, e := range tr.Tree {
			if e.Type == "blob" {
				t.Files[e.Path] = e.Sha
			}
		}
		if _, ok := t.Files["colors.toml"]; ok {
			ix.Themes = append(ix.Themes, t)
		}
	}
	sort.Slice(ix.Themes, func(i, j int) bool { return ix.Themes[i].Name < ix.Themes[j].Name })
	return ix, nil
}

func (ix *Index) Find(name string) *Theme {
	for _, t := range ix.Themes {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Template returns the content of an Omarchy template such as "ghostty.conf.tpl".
func (ix *Index) Template(name string) (string, error) {
	e, ok := ix.Templates[name]
	if !ok {
		return "", fmt.Errorf("omarchy has no template %q", name)
	}
	p, err := fetchBlob(e.Repo, e.Branch, e.Path, e.Sha)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

func (t *Theme) Has(rel string) bool { _, ok := t.Files[rel]; return ok }

// Fetch downloads one theme file into the blob cache and returns its local path.
func (t *Theme) Fetch(rel string) (string, error) {
	sha, ok := t.Files[rel]
	if !ok {
		return "", fmt.Errorf("theme %s has no %s", t.Name, rel)
	}
	return fetchBlob(t.Repo, t.Branch, path.Join(t.Dir, rel), sha)
}

func (t *Theme) Read(rel string) (string, error) {
	p, err := t.Fetch(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// Palette returns the fully resolved colors of the theme.
func (t *Theme) Palette() (Palette, error) {
	if t.palette != nil {
		return t.palette, nil
	}
	data, err := t.Read("colors.toml")
	if err != nil {
		return nil, err
	}
	t.palette = Resolve(ParseColors([]byte(data)), t.Has("light.mode"))
	return t.palette, nil
}

// Backgrounds lists wallpaper paths (relative to the theme) in display order.
func (t *Theme) Backgrounds() []string {
	var out []string
	for rel := range t.Files {
		if strings.HasPrefix(rel, "backgrounds/") {
			switch strings.ToLower(path.Ext(rel)) {
			case ".jpg", ".jpeg", ".png", ".webp", ".heic":
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

// LoadPalettes fetches colors.toml for every theme concurrently.
func (ix *Index) LoadPalettes() {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, t := range ix.Themes {
		wg.Add(1)
		go func(t *Theme) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = t.Palette()
		}(t)
	}
	wg.Wait()
}

func fetchBlob(repo, branch, p, sha string) (string, error) {
	local := filepath.Join(xdg.CacheDir(), "blobs", sha+strings.ToLower(path.Ext(p)))
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, url.PathEscape(branch), strings.Join(segs, "/"))
	resp, err := httpClient.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := xdg.WriteFileAtomic(local, data); err != nil {
		return "", err
	}
	return local, nil
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

var (
	tokenOnce sync.Once
	token     string
)

// githubToken uses $GITHUB_TOKEN or the gh CLI login to avoid the low
// anonymous rate limit; anonymous access still works without either.
func githubToken() string {
	tokenOnce.Do(func() {
		if token = os.Getenv("GITHUB_TOKEN"); token != "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output(); err == nil {
			token = strings.TrimSpace(string(out))
		}
	})
	return token
}

func githubJSON(u string, v any) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if t := githubToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
