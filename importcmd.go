package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gantry/internal/unl"
)

// runImportUNL is gantry importar-unl LAB.unl PASTA: makes the lab folder
// of a PNETLab/EVE-NG lab (what the app's import does), without a server.
func runImportUNL(argv []string) {
	if len(argv) != 2 {
		fmt.Fprintln(os.Stderr, "uso: gantry importar-unl LAB.unl PASTA")
		os.Exit(2)
	}
	b, err := os.ReadFile(argv[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	l, err := unl.Parse(b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	r := unl.Convert(l, b, filepath.Base(argv[0]))
	if len(r.Blocking) > 0 {
		fmt.Fprintln(os.Stderr, "o lab não dá para rodar como está:")
		for _, x := range r.Blocking {
			fmt.Fprintln(os.Stderr, "  -", x)
		}
		os.Exit(1)
	}
	dir := argv[1]
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		fmt.Fprintf(os.Stderr, "a pasta %s já existe e não está vazia\n", dir)
		os.Exit(1)
	}
	files := map[string]string{"compose.yaml": r.Compose}
	for k, v := range r.Files {
		files[k] = v
	}
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		p := filepath.Join(dir, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(p, []byte(files[k]), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(p)
	}
	for _, n := range r.Remarks {
		fmt.Println("atenção:", n)
	}
	for _, n := range r.Notes {
		fmt.Println("nota:", n)
	}
}
