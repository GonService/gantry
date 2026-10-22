package server

import (
	"strings"
)

// Small, line-based edits of compose files that keep the user's comments
// and layout (a YAML round trip would rewrite the whole file).

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// findBlock finds key under the top-level section (services:, x-vms:) and
// returns the line of the key and the end of its block (exclusive).
func findBlock(lines []string, section, key string) (start, end int) {
	in := false
	start, child := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		ind := indentOf(l)
		if ind == 0 {
			if start >= 0 {
				return start, trimEnd(lines, start, i)
			}
			in = strings.HasPrefix(t, section+":")
			child = -1
			continue
		}
		if !in {
			continue
		}
		if child < 0 {
			child = ind // the entries of the section (services, VMs)
		}
		if start >= 0 && ind <= indentOf(lines[start]) {
			return start, trimEnd(lines, start, i)
		}
		if start < 0 && ind == child {
			k := strings.Trim(strings.TrimSpace(strings.SplitN(t, ":", 2)[0]), `"'`)
			if k == key && strings.Contains(t, ":") {
				start = i
			}
		}
	}
	if start >= 0 {
		return start, trimEnd(lines, start, len(lines))
	}
	return -1, -1
}

// trimEnd leaves trailing blank lines out of a block.
func trimEnd(lines []string, start, end int) int {
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return end
}

// fieldLine finds field: directly inside the block (its own children only).
func fieldLine(lines []string, start, end int, field string) int {
	child := -1
	for i := start + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		ind := indentOf(lines[i])
		if child < 0 {
			child = ind
		}
		if ind == child && (strings.HasPrefix(t, field+":")) {
			return i
		}
	}
	return -1
}

// fieldValue is the value written after field: (without comment/quotes).
func fieldValue(line string) string {
	_, v, _ := strings.Cut(line, ":")
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "\"") && !strings.HasPrefix(v, "'") {
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
	}
	return strings.Trim(v, `"'`)
}

// cloneBlock copies the block of key under section as to, without the
// fields that must differ between the two (ports, ssh), right after it.
func cloneBlock(src, section, key, to string, drop ...string) (string, bool) {
	lines := strings.Split(src, "\n")
	start, end := findBlock(lines, section, key)
	if start < 0 {
		return src, false
	}
	keyLine := lines[start]
	ind := indentOf(keyLine)
	_, rest, _ := strings.Cut(strings.TrimSpace(keyLine), ":")
	out := []string{strings.Repeat(" ", ind) + to + ":" + rest}
	skipUntil, child := -1, -1
	for i := start + 1; i < end; i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if skipUntil >= 0 {
			if t == "" || indentOf(l) > skipUntil || strings.HasPrefix(t, "- ") && indentOf(l) >= skipUntil {
				continue
			}
			skipUntil = -1
		}
		if child < 0 && t != "" && !strings.HasPrefix(t, "#") {
			child = indentOf(l)
		}
		dropped := false
		for _, f := range drop {
			if indentOf(l) == child && strings.HasPrefix(t, f+":") {
				dropped = true
				skipUntil = indentOf(l)
			}
		}
		if !dropped {
			out = append(out, l)
		}
	}
	res := append([]string{}, lines[:end]...)
	res = append(res, out...)
	res = append(res, lines[end:]...)
	return strings.Join(res, "\n"), true
}

// childIndent is the indentation of the fields of a block (or the block's
// own indentation plus two when it has none yet).
func childIndent(lines []string, start, end int) int {
	for i := start + 1; i < end; i++ {
		if t := strings.TrimSpace(lines[i]); t != "" && !strings.HasPrefix(t, "#") {
			return indentOf(lines[i])
		}
	}
	return indentOf(lines[start]) + 2
}

// setBlockField writes field: value in the block of key under section,
// replacing the field's line and whatever is nested under it (a literal
// block, a map), or adding it at the end of the block. An empty value
// removes the field.
func setBlockField(src, section, key, field, value string) (string, bool) {
	lines := strings.Split(src, "\n")
	start, end := findBlock(lines, section, key)
	if start < 0 {
		return src, false
	}
	// An entry written on one line (SW1: {image: …}): the field goes inside
	// its braces, not on a line under it (which is not YAML).
	if l, ok := flowSet(lines[start], field, value); ok {
		lines[start] = l
		return strings.Join(lines, "\n"), true
	}
	child := childIndent(lines, start, end)
	newLine := strings.Repeat(" ", child) + field + ": " + value
	at := fieldLine(lines, start, end, field)
	if at < 0 {
		if value == "" {
			return src, true
		}
		res := append([]string{}, lines[:end]...)
		res = append(res, newLine)
		res = append(res, lines[end:]...)
		return strings.Join(res, "\n"), true
	}
	// The field's own lines: everything after it more indented than it
	// (blank lines inside count when more of it follows).
	stop := at + 1
	for i := at + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if indentOf(lines[i]) <= child {
			break
		}
		stop = i + 1
	}
	// Blank lines that ended the old value go with it (not the ones that
	// separate the block from the next).
	for stop < end && strings.TrimSpace(lines[stop]) == "" {
		stop++
	}
	var res []string
	res = append(res, lines[:at]...)
	if value != "" {
		res = append(res, newLine)
	}
	res = append(res, lines[stop:]...)
	return strings.Join(res, "\n"), true
}
