// Package i18n puts Gantry's messages in English when the app is in English.
//
// The messages are written in Portuguese in the code, and the Portuguese
// text is the key: en.json maps each one (a whole message, a format of
// fmt.Errorf/Sprintf or a piece of a message joined with +) to English.
// The translation happens where a message leaves the server (an error
// answer, the end of a log, the output of an operation, the JSON of the
// API), not where it is made, so the code goes on writing plain Portuguese.
package i18n

import (
	"embed"
	"encoding/json"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

//go:embed en/*.json
var enFS embed.FS

// enJSON is the dictionary: the files of en/ together (one per area of
// the code, so each one can be worked on alone).
var enJSON = func() []byte {
	all := map[string]string{}
	files, _ := fs.Glob(enFS, "en/*.json")
	for _, f := range files {
		b, err := enFS.ReadFile(f)
		if err != nil {
			continue
		}
		var m map[string]string
		if json.Unmarshal(b, &m) == nil {
			for k, v := range m {
				all[k] = v
			}
		}
	}
	b, _ := json.Marshal(all)
	return b
}()

var lang atomic.Value // "pt" or "en"

// Set chooses the language of what leaves the server ("en", "pt"…).
func Set(l string) {
	l = Normalize(l)
	if l == "" {
		return
	}
	if cur, _ := lang.Load().(string); cur != l {
		lang.Store(l)
	}
}

// Lang is the language in use ("pt" when none was set).
func Lang() string {
	if l, _ := lang.Load().(string); l != "" {
		return l
	}
	return "pt"
}

// English says whether messages go out in English.
func English() bool { return Lang() == "en" }

// Normalize turns a language tag (pt-BR, en-US, an Accept-Language) into
// "pt" or "en"; "" when it says nothing.
func Normalize(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" || tag == "auto" {
		return ""
	}
	if i := strings.IndexAny(tag, ",;"); i >= 0 {
		tag = tag[:i]
	}
	if strings.HasPrefix(tag, "pt") {
		return "pt"
	}
	return "en"
}

type pattern struct {
	re   *regexp.Regexp
	en   string // with %s for each argument, in order
	args []int  // the argument (of the regexp groups) each %s of en takes
}

type dict struct {
	exact    map[string]string
	patterns []pattern
	byWord   map[string][]int // first literal word -> patterns
	frags    []frag
}

type frag struct{ pt, en string }

var (
	once   sync.Once
	d      dict
	cache  sync.Map // translated strings
	cacheN atomic.Int64
)

var verbRe = regexp.MustCompile(`%(?:\[(\d+)\])?[-+# 0]*\d*(?:\.\d+)?[vTtbcdoOqxXUeEfFgGsp%]`)

func load() {
	d = dict{exact: map[string]string{}, byWord: map[string][]int{}}
	var raw map[string]string
	if err := json.Unmarshal(enJSON, &raw); err != nil {
		return
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	// Longer first: a pattern or a piece that says more wins.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, pt := range keys {
		en := raw[pt]
		if en == "" || en == pt {
			continue
		}
		if !verbRe.MatchString(pt) || strings.ReplaceAll(pt, "%%", "") == strings.ReplaceAll(en, "%%", "") {
			d.exact[strings.ReplaceAll(pt, "%%", "%")] = strings.ReplaceAll(en, "%%", "%")
			if p := strings.TrimSpace(pt); len(p) >= 8 && strings.Contains(p, " ") {
				// A piece joined with + ("o arquivo " + f + " sumiu"), also
				// without its line breaks (the message is matched trimmed).
				d.frags = append(d.frags, frag{pt, en})
				if q := strings.Trim(pt, "\n"); q != pt && len(q) >= 8 {
					d.frags = append(d.frags, frag{q, strings.Trim(en, "\n")})
				}
			}
			continue
		}
		// Messages are matched without the spaces and line breaks around
		// them (translate keeps those), so the format is too.
		if tp := strings.TrimSpace(pt); tp != pt && strings.TrimSpace(en) != "" {
			pt, en = tp, strings.TrimSpace(en)
		}
		if p, ok := compile(pt, en); ok {
			d.patterns = append(d.patterns, p)
			i := len(d.patterns) - 1
			// By its first word; a format that starts with an argument
			// ("%s não existe") under "".
			w := ""
			if loc := verbRe.FindStringIndex(pt); loc != nil {
				w = firstWord(pt[:loc[0]])
			}
			d.byWord[w] = append(d.byWord[w], i)
		}
	}
	sort.SliceStable(d.frags, func(i, j int) bool { return len(d.frags[i].pt) > len(d.frags[j].pt) })
}

// compile turns a format ("o pod %s sumiu em %d s") into a pattern.
func compile(pt, en string) (pattern, bool) {
	var b strings.Builder
	b.WriteString("^")
	last, n := 0, 0
	literal := 0
	for _, m := range verbRe.FindAllStringSubmatchIndex(pt, -1) {
		lit := pt[last:m[0]]
		literal += countLetters(lit)
		b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(lit, "%%", "%")))
		if pt[m[0]:m[1]] == "%%" {
			b.WriteString("%")
		} else {
			b.WriteString("(.+?)")
			n++
		}
		last = m[1]
	}
	lit := pt[last:]
	literal += countLetters(lit)
	b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(lit, "%%", "%")))
	b.WriteString("$")
	if literal < 2 || n == 0 {
		return pattern{}, false // "%s: %s" says nothing to translate
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return pattern{}, false
	}
	// The English may take the arguments in another order: %[2]s.
	var args []int
	next := 0
	out := verbRe.ReplaceAllStringFunc(en, func(v string) string {
		if v == "%%" {
			return "%%"
		}
		sm := verbRe.FindStringSubmatch(v)
		i := next
		if sm[1] != "" {
			k, _ := strconv.Atoi(sm[1])
			i = k - 1
		}
		next = i + 1
		args = append(args, i)
		return "%s"
	})
	for _, a := range args {
		if a < 0 || a >= n {
			return pattern{}, false
		}
	}
	return pattern{re: re, en: out, args: args}, true
}

func countLetters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func firstWord(s string) string {
	s = ansiRe.ReplaceAllString(s, " ")
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		return strings.ToLower(w)
	}
	return ""
}

// T is msg in the language in use.
func T(msg string) string {
	if !English() || msg == "" {
		return msg
	}
	return ToEnglish(msg)
}

// ToEnglish is msg in English, whatever the language in use.
func ToEnglish(msg string) string {
	once.Do(load)
	if v, ok := cache.Load(msg); ok {
		return v.(string)
	}
	out := translate(msg, 0)
	if cacheN.Add(1) > 20000 {
		cache.Range(func(k, _ any) bool { cache.Delete(k); return true })
		cacheN.Store(0)
	}
	cache.Store(msg, out)
	return out
}

func translate(msg string, depth int) string {
	if en, ok := d.exact[msg]; ok {
		return en
	}
	// Keep the spaces and line breaks around the message.
	trimmed := strings.TrimSpace(msg)
	if trimmed != msg && trimmed != "" {
		i := strings.Index(msg, trimmed)
		return msg[:i] + translate(trimmed, depth) + msg[i+len(trimmed):]
	}
	if en, ok := matchPattern(msg, depth); ok {
		return en
	}
	if depth < 4 {
		// Lines, one by one.
		if strings.Contains(msg, "\n") {
			lines := strings.Split(msg, "\n")
			for i, l := range lines {
				lines[i] = translate(l, depth+1)
			}
			return strings.Join(lines, "\n")
		}
		// A wrapped error: "a coisa: o motivo".
		if i := strings.Index(msg, ": "); i > 0 {
			return translate(msg[:i], depth+1) + ": " + translate(msg[i+2:], depth+1)
		}
	}
	return fragments(msg)
}

func matchPattern(msg string, depth int) (string, bool) {
	seen := map[int]bool{}
	try := func(idx []int) (string, bool) {
		for _, i := range idx {
			if seen[i] {
				continue
			}
			seen[i] = true
			p := d.patterns[i]
			m := p.re.FindStringSubmatch(msg)
			if m == nil {
				continue
			}
			vals := make([]any, len(p.args))
			for k, a := range p.args {
				arg := m[a+1]
				if depth < 4 {
					arg = translate(arg, depth+1) // an argument may be a message too
				}
				vals[k] = arg
			}
			return sprintf(p.en, vals), true
		}
		return "", false
	}
	if w := firstWord(msg); w != "" {
		if s, ok := try(d.byWord[w]); ok {
			return s, true
		}
	}
	// A format that starts with an argument ("%s não existe").
	return try(d.byWord[""])
}

func sprintf(f string, vals []any) string {
	var b strings.Builder
	k := 0
	for i := 0; i < len(f); i++ {
		if f[i] == '%' && i+1 < len(f) {
			switch f[i+1] {
			case 's':
				if k < len(vals) {
					b.WriteString(vals[k].(string))
				}
				k++
				i++
				continue
			case '%':
				b.WriteByte('%')
				i++
				continue
			}
		}
		b.WriteByte(f[i])
	}
	return b.String()
}

// fragments puts in English the pieces of a message joined with +.
func fragments(msg string) string {
	if !looksPortuguese(msg) {
		return msg
	}
	for _, f := range d.frags {
		if strings.Contains(msg, f.pt) {
			msg = strings.ReplaceAll(msg, f.pt, f.en)
		}
	}
	return msg
}

var ptMarks = regexp.MustCompile(`(?i)[ãõçáéíóúâêô]|\b(não|nao|para|com|uma|um|está|esta|ser|sem|mais|já|ainda|foi|são|pelo|pela|nos|nas|dos|das|do|da|no|na|em|de|que|ou)\b`)

func looksPortuguese(s string) bool { return ptMarks.MatchString(s) }

// JSON puts the texts inside an API answer in English. Only whole texts
// Gantry makes (a known phrase, or a known format with its values) change:
// the API also carries what the user wrote (compose files, files being
// edited, env values), which must come back exactly as it was.
func JSON(v any) any {
	if !English() {
		return v
	}
	once.Do(load)
	return jsonWalk(v, "")
}

// Keys whose values are content, never Gantry's words.
var contentKeys = map[string]bool{
	"yaml": true, "content": true, "data": true, "text": true, "body": true, "files": true, "env": true,
	"environment": true, "command": true, "cmd": true, "entrypoint": true, "compose": true, "labels": true,
	"annotations": true, "value": true, "values": true, "script": true, "notes": true, "readme": true,
	"line": true, "lines": true, "output": true, "log": true, "logs": true, "args": true, "path": true,
	"dest": true, "url": true, "image": true, "name": true, "id": true,
}

// Keys whose values are errors and warnings Gantry made.
var errKeys = map[string]bool{"error": true, "warning": true, "warnings": true}

func jsonWalk(v any, key string) any {
	if contentKeys[strings.ToLower(key)] {
		return v
	}
	switch x := v.(type) {
	case string:
		if x == "" || len(x) > 2000 || strings.Contains(x, "\n") {
			return x
		}
		if en, ok := cache.Load("\x00" + x); ok {
			return en.(string)
		}
		out := x
		if en, ok := d.exact[x]; ok {
			out = en
		} else if errKeys[strings.ToLower(key)] {
			// An error is Gantry's words all the way down: a wrapped error
			// ("não consegui ler o compose: x-vms.R1.memory: …") has a
			// message of Gantry's inside, translated too.
			out = translate(x, 0)
		} else if strings.Contains(x, " ") {
			if en, ok := matchPattern(x, 4); ok { // 4: the values as they are
				out = en
			}
		}
		cache.Store("\x00"+x, out)
		return out
	case []any:
		for i := range x {
			x[i] = jsonWalk(x[i], key)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = jsonWalk(e, k)
		}
		return x
	}
	return v
}
