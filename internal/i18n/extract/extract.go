// Package extract finds the Portuguese messages in Gantry's Go code, to
// check that each one has its English in internal/i18n/en/*.json (or is in
// skip/*.json: texts that are not messages, like scripts and identifiers).
package extract

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var ptMarks = regexp.MustCompile(`(?i)[ãõçáéíóúâêôà]|\b(não|nao|para|com|uma|está|sem|mais|já|ainda|foi|são|pelo|pela|do|da|dos|das|no|na|nos|nas|em|de|que|ou|o|os|as|ao|aos|um|seu|sua|este|esta|esse|essa|abra|use|veja)\b`)

// Literal is one message found.
type Literal struct {
	Text string
	Pos  string
}

// Find walks the Go files under root (no tests, no i18n itself).
func Find(root string) ([]Literal, error) {
	var out []Literal
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			switch de.Name() {
			case ".git", "node_modules", "frontend", "web", "build", "dist", "e2e", "i18n", "testdata":
				return filepath.SkipDir
			}
			if p == filepath.Join(root, "tools") { // the helpers of the repo, not internal/tools
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		skip := map[*ast.BasicLit]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.ImportSpec:
				return false
			case *ast.Field:
				if x.Tag != nil {
					skip[x.Tag] = true
				}
			case *ast.CallExpr:
				// The log file stays in Portuguese (it goes to whoever
				// supports Gantry): slog.Info("…", "chave", v).
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "slog" {
						for _, a := range x.Args {
							if bl, ok := a.(*ast.BasicLit); ok {
								skip[bl] = true
							}
						}
					}
					// applog.Recover("where") and applog.Safe("where", …): places.
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "applog" && len(x.Args) > 0 {
						if bl, ok := x.Args[0].(*ast.BasicLit); ok {
							skip[bl] = true
						}
					}
				}
			case *ast.BasicLit:
				if x.Kind != token.STRING || skip[x] {
					return true
				}
				s, err := strconv.Unquote(x.Value)
				if err != nil || !Candidate(s) {
					return true
				}
				if !seen[s] {
					seen[s] = true
					rel, _ := filepath.Rel(root, p)
					out = append(out, Literal{s, rel + ":" + strconv.Itoa(fset.Position(x.Pos()).Line)})
				}
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out, err
}

// Candidate says whether a literal looks like Portuguese for a person.
func Candidate(s string) bool {
	t := strings.TrimSpace(s)
	if len(t) < 3 || !ptMarks.MatchString(t) {
		return false
	}
	letters := 0
	for _, r := range t {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127 {
			letters++
		}
	}
	if letters < 3 {
		return false
	}
	// A single identifier-like word without accents (a key, a label value).
	if !strings.ContainsAny(t, " ãõçáéíóúâêôà") {
		return false
	}
	return true
}

// Missing are the literals with neither English nor a place in skip.json.
func Missing(root string) ([]Literal, error) {
	lits, err := Find(root)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	names, _ := filepath.Glob(filepath.Join(root, "internal", "i18n", "en", "*.json"))
	skips, _ := filepath.Glob(filepath.Join(root, "internal", "i18n", "skip", "*.json"))
	names = append(names, skips...)
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		var m map[string]string
		if json.Unmarshal(b, &m) == nil {
			for k := range m {
				known[k] = true
			}
			continue
		}
		var l []string
		if json.Unmarshal(b, &l) == nil {
			for _, k := range l {
				known[k] = true
			}
		}
	}
	var out []Literal
	for _, l := range lits {
		if !known[l.Text] {
			out = append(out, l)
		}
	}
	return out, nil
}
