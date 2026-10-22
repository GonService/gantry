// Lists the Portuguese messages of the Go code still without English,
// as JSON ({"texto": "arquivo:linha"}), for internal/i18n/en.json.
//
//	go run ./tools/i18ngo [dir...]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gantry/internal/i18n/extract"
)

func main() {
	miss, err := extract.Missing(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	out := map[string]string{}
	for _, l := range miss {
		if len(os.Args) > 1 {
			ok := false
			for _, d := range os.Args[1:] {
				if strings.HasPrefix(l.Pos, strings.TrimPrefix(d, "./")) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		out[l.Text] = l.Pos
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
	fmt.Fprintf(os.Stderr, "%d sem inglês\n", len(out))
}
