package config

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SSHHost is one Host of ~/.ssh/config that can become a server in Gantry.
type SSHHost struct {
	Alias    string `json:"alias"`
	HostName string `json:"hostname,omitempty"`
	User     string `json:"user,omitempty"`
	Port     string `json:"port,omitempty"`
}

// SSHHosts reads the Host entries of ~/.ssh/config (and the files it
// includes): only plain aliases, not patterns like * or 10.0.*.
func SSHHosts() []SSHHost {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".ssh")
	seen := map[string]bool{}
	byAlias := map[string]*SSHHost{}
	var order []string
	var read func(path string, depth int)
	read = func(path string, depth int) {
		if depth > 5 || seen[path] {
			return
		}
		seen[path] = true
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		var cur []*SSHHost
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, val := splitSSH(line)
			switch strings.ToLower(key) {
			case "include":
				for _, pat := range strings.Fields(val) {
					if strings.HasPrefix(pat, "~/") {
						pat = filepath.Join(home, pat[2:])
					} else if !filepath.IsAbs(pat) {
						pat = filepath.Join(dir, pat)
					}
					matches, _ := filepath.Glob(pat)
					sort.Strings(matches)
					for _, m := range matches {
						read(m, depth+1)
					}
				}
			case "host":
				cur = nil
				for _, a := range strings.Fields(val) {
					if strings.ContainsAny(a, "*?!") {
						continue
					}
					h := byAlias[a]
					if h == nil {
						h = &SSHHost{Alias: a}
						byAlias[a] = h
						order = append(order, a)
					}
					cur = append(cur, h)
				}
			case "match":
				cur = nil
			case "hostname", "user", "port":
				for _, h := range cur {
					// The first value wins, as in ssh.
					switch strings.ToLower(key) {
					case "hostname":
						if h.HostName == "" {
							h.HostName = val
						}
					case "user":
						if h.User == "" {
							h.User = val
						}
					case "port":
						if h.Port == "" {
							h.Port = val
						}
					}
				}
			}
		}
	}
	read(filepath.Join(dir, "config"), 0)
	out := make([]SSHHost, 0, len(order))
	for _, a := range order {
		out = append(out, *byAlias[a])
	}
	return out
}

func splitSSH(line string) (string, string) {
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, ""
	}
	k := line[:i]
	v := strings.TrimLeft(line[i:], " \t=")
	return k, strings.Trim(v, `"`)
}
