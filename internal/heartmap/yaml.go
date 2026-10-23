package heartmap

import (
	"strconv"
	"strings"
)

// A deliberately small YAML subset parser: nested mappings, sequences, and
// scalar strings. Reachee carries zero third-party dependencies until the AWS
// SDK lands in the provider packages, so the Heart-Map loader parses its own
// input. Supported: "key: value", "key:" plus an indented block, "- " sequence
// items, single or double quoted scalars, and "#" comments.

type ymlLine struct {
	indent int
	raw    string
	seq    bool
}

func scan(src string) []ymlLine {
	var out []ymlLine
	for _, l := range strings.Split(src, "\n") {
		if i := strings.Index(l, " #"); i >= 0 {
			l = l[:i]
		}
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		trimmed := strings.TrimRight(l, " \t")
		if strings.TrimSpace(trimmed) == "" || strings.TrimSpace(trimmed) == "---" {
			continue
		}
		indent := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
		body := strings.TrimLeft(trimmed, " ")
		seq := false
		if strings.HasPrefix(body, "- ") {
			seq = true
			body = strings.TrimSpace(body[2:])
		}
		out = append(out, ymlLine{indent: indent, raw: body, seq: seq})
	}
	return out
}

func parse(items []ymlLine, indent int) any {
	if len(items) == 0 {
		return nil
	}
	if items[0].seq && items[0].indent == indent {
		var list []any
		pos := 0
		for pos < len(items) && items[pos].seq && items[pos].indent == indent {
			sub := []ymlLine{{indent: indent + 2, raw: items[pos].raw}}
			pos++
			for pos < len(items) && items[pos].indent >= indent+2 {
				sub = append(sub, items[pos])
				pos++
			}
			list = append(list, parse(sub, indent+2))
		}
		return list
	}

	m := map[string]any{}
	pos := 0
	for pos < len(items) && items[pos].indent == indent && !items[pos].seq {
		key, val, hasVal := splitKV(items[pos].raw)
		pos++
		if hasVal && val != "" {
			m[key] = unquote(val)
			continue
		}
		start := pos
		childIndent := -1
		for pos < len(items) && (items[pos].indent > indent || (items[pos].seq && items[pos].indent == indent)) {
			if childIndent == -1 {
				childIndent = items[pos].indent
			}
			pos++
		}
		if start == pos {
			m[key] = nil
			continue
		}
		m[key] = parse(items[start:pos], childIndent)
	}
	return m
}

func splitKV(s string) (string, string, bool) {
	if i := strings.Index(s, ": "); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:]), true
	}
	if strings.HasSuffix(s, ":") {
		return strings.TrimSpace(strings.TrimSuffix(s, ":")), "", true
	}
	return strings.TrimSpace(s), "", false
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func num(m map[string]any, key string, def int) int {
	s, ok := m[key].(string)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func boolean(m map[string]any, key string) bool {
	s, _ := m[key].(string)
	return s == "true" || s == "yes"
}
