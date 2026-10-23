package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"
)

// These editors change one key and leave the rest of the file — comments,
// ordering, unrelated settings — as it was.

func formatFor(step Step, file string) string {
	if step.Format != "" {
		return step.Format
	}
	switch {
	case strings.HasSuffix(file, ".json"), strings.HasSuffix(file, ".jsonc"):
		return "json"
	case strings.HasSuffix(file, ".yml"), strings.HasSuffix(file, ".yaml"):
		return "yaml"
	case strings.HasSuffix(file, ".toml"):
		return "toml"
	}
	return "kv"
}

func setKey(format string, src []byte, key string, val any) ([]byte, error) {
	switch format {
	case "toml":
		return []byte(setTOML(string(src), key, val)), nil
	case "json":
		return setJSON(src, strings.Split(key, "."), val)
	case "yaml":
		return setYAML(src, strings.Split(key, "."), val)
	case "kv":
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("kv format needs a string value for %s", key)
		}
		return []byte(setKV(string(src), key, s)), nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

// ---- TOML (line based, so comments survive) ----

var tomlHeader = regexp.MustCompile(`^\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)

func normTable(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

func countML(line string) int { return strings.Count(line, `"""`) + strings.Count(line, `'''`) }

func setTOML(src, dotted string, val any) string {
	table, key := "", dotted
	if i := strings.LastIndex(dotted, "."); i >= 0 {
		table, key = dotted[:i], dotted[i+1:]
	}
	assign := tomlKey(key) + " = " + tomlLiteral(val)
	keyRe := regexp.MustCompile(`^\s*["']?` + regexp.QuoteMeta(key) + `["']?\s*=`)

	lines := strings.Split(src, "\n")
	cur, inML := "", false
	tableFound := table == ""
	lastInTable, firstHeader := -1, -1
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if inML {
			if countML(l)%2 == 1 {
				inML = false
			}
			continue
		}
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "[[") {
			if m := tomlHeader.FindStringSubmatch(t); m != nil {
				if firstHeader < 0 {
					firstHeader = i
				}
				cur = normTable(m[1])
				if cur == table {
					tableFound, lastInTable = true, i
				}
				continue
			}
		} else if firstHeader < 0 {
			firstHeader = i
			cur = "\x00array"
		}
		if cur == table && keyRe.MatchString(l) {
			end := i
			if countML(l)%2 == 1 { // the old value is a multi-line string; drop its body too
				for end+1 < len(lines) {
					end++
					if countML(lines[end])%2 == 1 {
						break
					}
				}
			}
			indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			lines = append(lines[:i], append([]string{indent + assign}, lines[end+1:]...)...)
			return strings.Join(lines, "\n")
		}
		if cur == table && t != "" && (table != "" || firstHeader < 0) {
			lastInTable = i
		}
		if countML(l)%2 == 1 {
			inML = true
		}
	}

	insert := func(at int, newLines ...string) string {
		lines = append(lines[:at], append(newLines, lines[at:]...)...)
		return strings.Join(lines, "\n")
	}
	switch {
	case table == "":
		if firstHeader < 0 {
			return appendLines(src, assign)
		}
		// Top-level keys must come before the first table; put it after the
		// last top-level line, ahead of any comment that introduces the table.
		at := firstHeader
		for at > 0 && (strings.HasPrefix(strings.TrimSpace(lines[at-1]), "#") || strings.TrimSpace(lines[at-1]) == "") {
			at--
		}
		if at == 0 {
			return insert(firstHeader, assign, "")
		}
		return insert(at, assign)
	case tableFound:
		return insert(lastInTable+1, assign)
	default:
		return appendLines(src, "", "["+table+"]", assign)
	}
}

func appendLines(src string, add ...string) string {
	src = strings.TrimRight(src, "\n")
	if src == "" {
		for len(add) > 0 && add[0] == "" {
			add = add[1:]
		}
		return strings.Join(add, "\n") + "\n"
	}
	return src + "\n" + strings.Join(add, "\n") + "\n"
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return strconv.Quote(k)
}

func tomlLiteral(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = tomlLiteral(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := sortedKeys(t)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = tomlKey(k) + " = " + tomlLiteral(t[k])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	return strconv.Quote(fmt.Sprint(v))
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- key = value files such as Ghostty's config ----

func setKV(src, key, val string) string {
	re := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if re.MatchString(l) {
			lines[i] = key + " = " + val
			return strings.Join(lines, "\n")
		}
	}
	return appendLines(src, key+" = "+val)
}

// ---- JSON / JSONC (comments kept via hujson) ----

func setJSON(src []byte, path []string, val any) ([]byte, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		src = []byte("{}\n")
	}
	v, err := hujson.Parse(src)
	if err != nil {
		return nil, err
	}
	std := v.Clone()
	std.Standardize()
	var root any
	if err := json.Unmarshal(std.Pack(), &root); err != nil {
		return nil, err
	}
	cur, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("top level is not an object")
	}

	type op struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	var ops []op
	ptr := func(n int) string {
		parts := make([]string, n)
		for i := 0; i < n; i++ {
			parts[i] = strings.NewReplacer("~", "~0", "/", "~1").Replace(path[i])
		}
		return "/" + strings.Join(parts, "/")
	}
	for i := 0; i < len(path)-1; i++ {
		if next, ok := cur[path[i]].(map[string]any); ok && cur != nil {
			cur = next
			continue
		}
		ops = append(ops, op{"add", ptr(i + 1), map[string]any{}})
		cur = nil
	}
	// The first path element that is new (or the final key) gets laid out
	// below; hujson inserts new members without any whitespace.
	newAt := len(path) - 1
	if len(ops) > 0 {
		newAt = strings.Count(ops[0].Path, "/") - 1
	}
	ops = append(ops, op{"add", ptr(len(path)), val})
	patch, _ := json.Marshal(ops)
	if err := v.Patch(patch); err != nil {
		return nil, err
	}

	if json.Valid(src) {
		// Plain JSON: re-indent the way the file already was.
		var buf bytes.Buffer
		if err := json.Indent(&buf, compact(v.Pack()), "", detectIndent(src)); err != nil {
			return nil, err
		}
		buf.WriteByte('\n')
		return buf.Bytes(), nil
	}
	layoutMember(&v, path[:newAt+1], detectIndent(src))
	return v.Pack(), nil
}

// layoutMember puts the member at path on its own, properly indented line
// and pretty-prints its value, leaving the rest of a JSONC file untouched.
func layoutMember(v *hujson.Value, path []string, unit string) {
	cur := v
	for depth, name := range path {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return
		}
		var m *hujson.ObjectMember
		for i := range obj.Members {
			if lit, ok := obj.Members[i].Name.Value.(hujson.Literal); ok && lit.String() == name {
				m = &obj.Members[i]
			}
		}
		if m == nil {
			return
		}
		if depth < len(path)-1 {
			cur = &m.Value
			continue
		}
		indent := strings.Repeat(unit, depth+1)
		if len(strings.TrimSpace(string(m.Name.BeforeExtra))) == 0 && !strings.Contains(string(m.Name.BeforeExtra), "\n") {
			m.Name.BeforeExtra = hujson.Extra("\n" + indent)
		}
		std := m.Value.Clone()
		std.Standardize()
		var buf bytes.Buffer
		if json.Indent(&buf, std.Pack(), indent, unit) == nil {
			if pretty, err := hujson.Parse(buf.Bytes()); err == nil {
				m.Value.Value = pretty.Value
				m.Value.BeforeExtra = hujson.Extra(" ")
			}
		}
		// A member appended last still needs the closing brace on its own line.
		if &obj.Members[len(obj.Members)-1] == m && !strings.Contains(string(obj.AfterExtra), "\n") {
			obj.AfterExtra = hujson.Extra("\n" + strings.Repeat(unit, depth))
		}
	}
}

func compact(b []byte) []byte {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

func detectIndent(src []byte) string {
	for _, l := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimLeft(l, " \t")
		if trimmed != "" && len(trimmed) < len(l) {
			return l[:len(l)-len(trimmed)]
		}
	}
	return "  "
}

// ---- YAML (yaml.v3 nodes keep comments) ----

func setYAML(src []byte, path []string, val any) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return nil, err
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	cur := doc.Content[0]
	if cur.Kind == yaml.ScalarNode && cur.Tag == "!!null" {
		*cur = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if cur.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("top level is not a mapping")
	}
	for i, k := range path {
		idx := -1
		for j := 0; j+1 < len(cur.Content); j += 2 {
			if cur.Content[j].Value == k {
				idx = j + 1
			}
		}
		if i == len(path)-1 {
			var vn yaml.Node
			if err := vn.Encode(val); err != nil {
				return nil, err
			}
			if idx >= 0 {
				vn.LineComment = cur.Content[idx].LineComment
				*cur.Content[idx] = vn
			} else {
				cur.Content = append(cur.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &vn)
			}
			break
		}
		if idx < 0 {
			m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			cur.Content = append(cur.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, m)
			cur = m
		} else {
			if cur.Content[idx].Kind != yaml.MappingNode {
				*cur.Content[idx] = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			cur = cur.Content[idx]
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- managed blocks ----

func setBlock(src, id, content, position, comment string) string {
	if comment == "" {
		comment = "#"
	}
	begin := fmt.Sprintf("%s >>> omac:%s (managed by omac, edits here are overwritten) >>>", comment, id)
	end := fmt.Sprintf("%s <<< omac:%s <<<", comment, id)
	block := begin + "\n" + strings.TrimRight(content, "\n") + "\n" + end

	beginRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(comment) + ` >>> omac:` + regexp.QuoteMeta(id) + `\b.*$`)
	endRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(end) + `[ \t]*$`)
	if b := beginRe.FindStringIndex(src); b != nil {
		if e := endRe.FindStringIndex(src[b[1]:]); e != nil {
			return src[:b[0]] + block + src[b[1]+e[1]:]
		}
	}
	if position == "top" {
		return block + "\n\n" + src
	}
	return appendLines(src, "", block)
}
