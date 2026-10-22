package composex

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

// Converted is a compose file made from run commands.
type Converted struct {
	YAML     string   `json:"yaml"`
	Engine   string   `json:"engine,omitempty"` // docker, podman or nerdctl, from the commands
	Services []string `json:"services"`
	Warnings []string `json:"warnings"`
}

// SplitShell splits a command line like a POSIX shell for the common cases:
// quotes, backslash escapes and line continuations.
func SplitShell(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	any := false
	q := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			} else {
				cur.WriteByte(c)
			}
		case q == '"':
			if c == '"' {
				q = 0
			} else if c == '\\' && i+1 < len(s) && strings.ContainsRune(`"\$`+"`", rune(s[i+1])) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			q, any = c, true
		case c == '\\' && i+1 < len(s):
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
				any = true
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if cur.Len() > 0 || any {
				out = append(out, cur.String())
				cur.Reset()
				any = false
			}
		default:
			cur.WriteByte(c)
			any = true
		}
	}
	if q != 0 {
		return nil, fmt.Errorf("aspas sem fechar")
	}
	if cur.Len() > 0 || any {
		out = append(out, cur.String())
	}
	return out, nil
}

// splitCommands breaks the text into commands: one per line, joining
// continuations, and also at ; and &&.
func splitCommands(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = regexp.MustCompile(`\\\n`).ReplaceAllString(text, " ")
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		// Split at ; and && outside quotes.
		q := byte(0)
		start := 0
		for i := 0; i < len(l); i++ {
			c := l[i]
			switch {
			case q != 0:
				if c == q {
					q = 0
				} else if c == '\\' && q == '"' {
					i++
				}
			case c == '\'' || c == '"':
				q = c
			case c == '\\':
				i++
			case c == ';' || c == '&' && i+1 < len(l) && l[i+1] == '&':
				if part := strings.TrimSpace(l[start:i]); part != "" {
					out = append(out, part)
				}
				if c == '&' {
					i++
				}
				start = i + 1
			}
		}
		if part := strings.TrimSpace(l[start:]); part != "" {
			out = append(out, part)
		}
	}
	return out
}

var (
	// flags of run that take a value, in their long and short names
	runValue = map[string]string{
		"--name": "name", "-p": "publish", "--publish": "publish", "-e": "env", "--env": "env", "--env-file": "env-file",
		"-v": "volume", "--volume": "volume", "--mount": "mount", "--network": "network", "--net": "network",
		"--restart": "restart", "-w": "workdir", "--workdir": "workdir", "-u": "user", "--user": "user",
		"--entrypoint": "entrypoint", "-h": "hostname", "--hostname": "hostname", "--cap-add": "cap-add",
		"--cap-drop": "cap-drop", "-m": "memory", "--memory": "memory", "--memory-reservation": "memory-reservation",
		"--cpus": "cpus", "-l": "label", "--label": "label", "--add-host": "add-host", "--dns": "dns",
		"--device": "device", "--tmpfs": "tmpfs", "--shm-size": "shm-size", "--log-driver": "log-driver",
		"--log-opt": "log-opt", "--platform": "platform", "--pull": "pull", "--ip": "ip", "--mac-address": "mac",
		"--sysctl": "sysctl", "--expose": "expose", "--stop-signal": "stop-signal", "--stop-timeout": "stop-timeout",
		"--health-cmd": "health-cmd", "--health-interval": "health-interval", "--health-retries": "health-retries",
		"--health-timeout": "health-timeout", "--security-opt": "security-opt", "--ulimit": "ulimit",
		"--pid": "pid", "--ipc": "ipc", "--userns": "userns", "--group-add": "group-add", "--domainname": "domainname",
		"--network-alias": "alias", "--namespace": "ignore", "-a": "ignore", "--attach": "ignore", "--cidfile": "ignore",
		"--gpus": "gpus",
	}
	// flags without a value
	runBool = map[string]string{
		"-d": "", "--detach": "", "-i": "stdin", "--interactive": "stdin", "-t": "tty", "--tty": "tty", "--rm": "",
		"--init": "init", "--privileged": "privileged", "--read-only": "read-only", "-P": "publish-all",
		"--publish-all": "publish-all", "-q": "", "--quiet": "", "--no-healthcheck": "no-healthcheck",
	}
)

// FromCommands turns docker/podman/nerdctl run (or create) commands into a
// compose file with one service per command.
func FromCommands(text string) (Converted, error) {
	res := Converted{Services: []string{}, Warnings: []string{}}
	type svc struct {
		name string
		m    yaml.MapSlice
	}
	var svcs []svc
	used := map[string]bool{}
	volumes := map[string]bool{}
	networks := map[string]bool{}
	var volOrder, netOrder []string
	warn := func(f string, a ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(f, a...)) }

	for _, line := range splitCommands(text) {
		args, err := SplitShell(line)
		if err != nil {
			return res, fmt.Errorf("%q: %v", line, err)
		}
		for len(args) > 0 && (args[0] == "sudo" || strings.Contains(args[0], "=") && !strings.HasPrefix(args[0], "-")) {
			args = args[1:] // sudo, VAR=x
		}
		if len(args) == 0 {
			continue
		}
		eng := path.Base(args[0])
		switch eng {
		case "docker", "podman", "nerdctl":
		default:
			return res, fmt.Errorf("não reconheci %q: use comandos docker run, podman run ou nerdctl run", line)
		}
		if res.Engine == "" {
			res.Engine = eng
		}
		args = args[1:]
		// Global flags before the subcommand (nerdctl --namespace x, podman --remote).
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			if (args[0] == "-n" || args[0] == "--namespace" || args[0] == "--host" || args[0] == "-H" || args[0] == "--context" || args[0] == "-c") && len(args) > 1 {
				args = args[2:]
			} else {
				args = args[1:]
			}
		}
		if len(args) > 0 && args[0] == "container" {
			args = args[1:]
		}
		if len(args) == 0 || (args[0] != "run" && args[0] != "create") {
			return res, fmt.Errorf("%q: só comandos run ou create viram serviços", line)
		}
		args = args[1:]

		vals := map[string][]string{}
		flags := map[string]bool{}
		image := ""
		var cmd []string
		for i := 0; i < len(args); i++ {
			a := args[i]
			if image != "" {
				cmd = append(cmd, a)
				continue
			}
			if !strings.HasPrefix(a, "-") || a == "-" {
				image = a
				continue
			}
			if a == "--" {
				continue
			}
			name, val, hasVal := strings.Cut(a, "=")
			if key, ok := runValue[name]; ok {
				if !hasVal {
					if i+1 >= len(args) {
						return res, fmt.Errorf("%q: falta o valor de %s", line, name)
					}
					i++
					val = args[i]
				}
				vals[key] = append(vals[key], val)
				continue
			}
			if key, ok := runBool[name]; ok {
				if key != "" && (!hasVal || val == "true") {
					flags[key] = true
				}
				continue
			}
			// Grouped short flags: -it, -dit, -itp 80:80 is not supported.
			if !strings.HasPrefix(a, "--") && len(a) > 2 {
				okAll := true
				for _, c := range a[1:] {
					if key, ok := runBool["-"+string(c)]; ok {
						if key != "" {
							flags[key] = true
						}
					} else {
						okAll = false
					}
				}
				if okAll {
					continue
				}
				// -p8080:80 style
				if key, ok := runValue[a[:2]]; ok {
					vals[key] = append(vals[key], a[2:])
					continue
				}
			}
			warn("opção %s ignorada", name)
		}
		if image == "" {
			return res, fmt.Errorf("%q: falta a imagem", line)
		}

		name := ""
		if v := vals["name"]; len(v) > 0 {
			name = v[len(v)-1]
		} else {
			base := image
			if i := strings.LastIndex(base, "/"); i >= 0 {
				base = base[i+1:]
			}
			base, _, _ = strings.Cut(base, "@")
			base, _, _ = strings.Cut(base, ":")
			name = base
		}
		name = strings.Trim(regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(name, "-"), "-.")
		if name == "" {
			name = "app"
		}
		for n, i := name, 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", n, i)
		}
		used[name] = true

		var m yaml.MapSlice
		add := func(k string, v any) { m = append(m, yaml.MapItem{Key: k, Value: v}) }
		add("image", image)
		if len(vals["entrypoint"]) > 0 {
			ep, _ := SplitShell(vals["entrypoint"][0])
			add("entrypoint", ep)
		}
		if len(cmd) > 0 {
			add("command", cmd)
		}
		if v := vals["name"]; len(v) > 0 {
			add("container_name", v[len(v)-1])
		}
		if v := vals["hostname"]; len(v) > 0 {
			add("hostname", v[0])
		}
		if v := vals["domainname"]; len(v) > 0 {
			add("domainname", v[0])
		}
		if v := vals["restart"]; len(v) > 0 {
			add("restart", v[0])
		}
		if v := vals["workdir"]; len(v) > 0 {
			add("working_dir", v[0])
		}
		if v := vals["user"]; len(v) > 0 {
			add("user", v[0])
		}
		if v := vals["platform"]; len(v) > 0 {
			add("platform", v[0])
		}
		if v := vals["pull"]; len(v) > 0 {
			add("pull_policy", v[0])
		}
		if flags["tty"] {
			add("tty", true)
		}
		if flags["stdin"] {
			add("stdin_open", true)
		}
		if flags["init"] {
			add("init", true)
		}
		if flags["privileged"] {
			add("privileged", true)
		}
		if flags["read-only"] {
			add("read_only", true)
		}
		if flags["publish-all"] {
			warn("%s: -P (publicar todas as portas) não existe no compose; liste as portas", name)
		}
		if v := vals["publish"]; len(v) > 0 {
			add("ports", v)
		}
		if v := vals["expose"]; len(v) > 0 {
			add("expose", v)
		}
		if v := vals["env"]; len(v) > 0 {
			env := make([]string, 0, len(v))
			for _, e := range v {
				if !strings.Contains(e, "=") {
					e += "=${" + e + "}" // -e VAR takes it from the environment
				}
				env = append(env, e)
			}
			add("environment", env)
		}
		if v := vals["env-file"]; len(v) > 0 {
			add("env_file", v)
		}
		if v := vals["volume"]; len(v) > 0 {
			add("volumes", v)
			for _, x := range v {
				src, _, ok := strings.Cut(x, ":")
				if ok && src != "" && !strings.ContainsAny(src[:1], "/.~$") && !volumes[src] {
					volumes[src] = true
					volOrder = append(volOrder, src)
				}
			}
		}
		if len(vals["mount"]) > 0 {
			warn("%s: --mount não foi convertido; use -v ou ajuste os volumes no YAML", name)
		}
		if v := vals["tmpfs"]; len(v) > 0 {
			add("tmpfs", v)
		}
		if nets := vals["network"]; len(nets) > 0 {
			n := nets[0]
			switch {
			case n == "host" || n == "none" || n == "bridge" && len(nets) == 1 || strings.HasPrefix(n, "container:"):
				if n != "bridge" {
					add("network_mode", n)
				}
			default:
				var ns []string
				for _, x := range nets {
					x, _, _ = strings.Cut(x, ":") // podman name:ip=…
					ns = append(ns, x)
					if !networks[x] {
						networks[x] = true
						netOrder = append(netOrder, x)
					}
				}
				if ip := vals["ip"]; len(ip) > 0 {
					add("networks", yaml.MapSlice{{Key: ns[0], Value: yaml.MapSlice{{Key: "ipv4_address", Value: ip[0]}}}})
				} else {
					add("networks", ns)
				}
			}
		}
		if v := vals["mac"]; len(v) > 0 {
			add("mac_address", v[0])
		}
		if v := vals["add-host"]; len(v) > 0 {
			add("extra_hosts", v)
		}
		if v := vals["dns"]; len(v) > 0 {
			add("dns", v)
		}
		if v := vals["cap-add"]; len(v) > 0 {
			add("cap_add", v)
		}
		if v := vals["cap-drop"]; len(v) > 0 {
			add("cap_drop", v)
		}
		if v := vals["device"]; len(v) > 0 {
			add("devices", v)
		}
		if v := vals["security-opt"]; len(v) > 0 {
			add("security_opt", v)
		}
		if v := vals["sysctl"]; len(v) > 0 {
			add("sysctls", v)
		}
		if v := vals["memory"]; len(v) > 0 {
			add("mem_limit", v[0])
		}
		if v := vals["memory-reservation"]; len(v) > 0 {
			add("mem_reservation", v[0])
		}
		if v := vals["cpus"]; len(v) > 0 {
			add("cpus", v[0])
		}
		if v := vals["shm-size"]; len(v) > 0 {
			add("shm_size", v[0])
		}
		for _, k := range []string{"pid", "ipc", "userns"} {
			if v := vals[k]; len(v) > 0 {
				add(map[string]string{"pid": "pid", "ipc": "ipc", "userns": "userns_mode"}[k], v[0])
			}
		}
		if v := vals["group-add"]; len(v) > 0 {
			add("group_add", v)
		}
		if v := vals["stop-signal"]; len(v) > 0 {
			add("stop_signal", v[0])
		}
		if v := vals["stop-timeout"]; len(v) > 0 {
			add("stop_grace_period", v[0]+"s")
		}
		if v := vals["label"]; len(v) > 0 {
			add("labels", v)
		}
		if v := vals["log-driver"]; len(v) > 0 {
			lg := yaml.MapSlice{{Key: "driver", Value: v[0]}}
			if o := vals["log-opt"]; len(o) > 0 {
				opts := yaml.MapSlice{}
				for _, x := range o {
					k, val, _ := strings.Cut(x, "=")
					opts = append(opts, yaml.MapItem{Key: k, Value: val})
				}
				lg = append(lg, yaml.MapItem{Key: "options", Value: opts})
			}
			add("logging", lg)
		}
		if v := vals["ulimit"]; len(v) > 0 {
			ul := yaml.MapSlice{}
			for _, x := range v {
				k, val, _ := strings.Cut(x, "=")
				ul = append(ul, yaml.MapItem{Key: k, Value: val})
			}
			add("ulimits", ul)
		}
		if v := vals["health-cmd"]; len(v) > 0 {
			hc := yaml.MapSlice{{Key: "test", Value: []string{"CMD-SHELL", v[0]}}}
			for _, k := range []string{"interval", "timeout", "retries"} {
				if x := vals["health-"+k]; len(x) > 0 {
					hc = append(hc, yaml.MapItem{Key: k, Value: x[0]})
				}
			}
			add("healthcheck", hc)
		}
		if flags["no-healthcheck"] {
			add("healthcheck", yaml.MapSlice{{Key: "disable", Value: true}})
		}
		if len(vals["alias"]) > 0 {
			warn("%s: --network-alias não foi convertido", name)
		}
		if len(vals["gpus"]) > 0 {
			warn("%s: --gpus não foi convertido; veja deploy.resources.reservations.devices no compose", name)
		}
		svcs = append(svcs, svc{name: name, m: m})
		res.Services = append(res.Services, name)
	}
	if len(svcs) == 0 {
		return res, fmt.Errorf("cole pelo menos um comando docker run, podman run ou nerdctl run")
	}
	doc := yaml.MapSlice{}
	services := yaml.MapSlice{}
	for _, s := range svcs {
		services = append(services, yaml.MapItem{Key: s.name, Value: s.m})
	}
	doc = append(doc, yaml.MapItem{Key: "services", Value: services})
	if len(volOrder) > 0 {
		vs := yaml.MapSlice{}
		for _, v := range volOrder {
			vs = append(vs, yaml.MapItem{Key: v, Value: yaml.MapSlice{}})
		}
		doc = append(doc, yaml.MapItem{Key: "volumes", Value: vs})
	}
	if len(netOrder) > 0 {
		ns := yaml.MapSlice{}
		for _, n := range netOrder {
			ns = append(ns, yaml.MapItem{Key: n, Value: yaml.MapSlice{}})
		}
		doc = append(doc, yaml.MapItem{Key: "networks", Value: ns})
	}
	b, err := yaml.MarshalWithOptions(doc, yaml.IndentSequence(true))
	if err != nil {
		return res, err
	}
	res.YAML = strings.ReplaceAll(string(b), ": {}\n", ": {}\n")
	return res, nil
}
