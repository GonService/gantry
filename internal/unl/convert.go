package unl

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gantry/internal/composex"
)

// Result is the lab folder made from a .unl.
type Result struct {
	Name    string            // project name (compose name:)
	Compose string            // compose.yaml
	Files   map[string]string // the other files, by path in the folder
	// Images are the image files the lab needs on the machine (IOL .bin,
	// Dynamips .image), by node.
	Images map[string][]string
	// Kinds are the kinds of equipment of the lab (iol, dyn, vpcs).
	Kinds map[string]bool
	// Blocking: equipment Gantry cannot run as it is; nothing is imported
	// while there is any (the lab would not be the same).
	Blocking []string
	// Notes: what the original has that the lab leaves out, and how it
	// runs here; they go in IMPORTACAO.md too.
	Notes []string
	// Remarks: things of the original that look wrong (a switch that runs
	// a router image, a configuration of an interface the device lacks).
	// Gantry imports them as they are; it only points them out.
	Remarks []string
}

var reName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func nodeName(s string, used map[string]bool) string {
	n := strings.Trim(reName.ReplaceAllString(s, "-"), "-.")
	if n == "" {
		n = "no"
	}
	base := n
	for i := 2; used[n]; i++ {
		n = base + "-" + strconv.Itoa(i)
	}
	used[n] = true
	return n
}

// ProjectName makes a compose project name of the lab's name.
func ProjectName(s string) string {
	n := strings.Trim(regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(strings.ToLower(s), "-"), "-_")
	if n == "" || !regexp.MustCompile(`^[a-z0-9]`).MatchString(n) {
		n = "lab-" + n
	}
	if len(n) > 60 {
		n = strings.TrimRight(n[:60], "-_")
	}
	return n
}

type end struct {
	node string
	port int
}

// Convert makes the Gantry lab of a .unl (file is its name, kept in
// original/).
func Convert(l *Lab, original []byte, file string) *Result {
	r := &Result{Name: ProjectName(l.Name), Files: map[string]string{}, Images: map[string][]string{}, Kinds: map[string]bool{}}
	used := map[string]bool{}
	type gnode struct {
		n      *Node
		name   string
		kind   string // iol, dyn, vpcs
		ports  []string
		cfg    string
		labels []string // port names, as the original calls them
		ifs    []string // the device's Ethernet interfaces, as its IOS names them
	}
	var nodes []*gnode
	ends := map[int][]end{}
	for i := range l.Nodes {
		n := &l.Nodes[i]
		g := &gnode{n: n, name: nodeName(n.Name, used)}
		size := 0
		portIndex := func(p Port) (int, error) { return -1, fmt.Errorf("tipo %s", n.Type) }
		switch strings.ToLower(n.Type) {
		case "iol":
			g.kind = "iol"
			size = n.Ethernet * 4
			// EVE-NG numbers IOL ports slot + 16 × port; Gantry's order is
			// Ethernet0/0-0/3, 1/0-1/3…
			portIndex = func(p Port) (int, error) { return (p.ID%16)*4 + p.ID/16, nil }
			for i := 0; i < size; i++ {
				g.ifs = append(g.ifs, fmt.Sprintf("Ethernet%d/%d", i/4, i%4))
			}
		case "dynamips":
			g.kind = "dyn"
			plat := strings.TrimPrefix(strings.ToLower(n.Template), "c")
			layout, err := composex.DynLayout(plat, n.Slots)
			if err != nil {
				r.Blocking = append(r.Blocking, fmt.Sprintf("%s: Dynamips %s com as placas %v: %v", n.Name, n.Template, n.Slots, err))
				continue
			}
			size = len(layout)
			for _, x := range layout {
				g.ifs = append(g.ifs, x.Name)
			}
			portIndex = func(p Port) (int, error) {
				for i, x := range layout {
					if x.Slot == p.ID/16 && x.Port == p.ID%16 {
						return i, nil
					}
				}
				return -1, fmt.Errorf("a porta %s não existe nas placas %v", p.Name, n.Slots)
			}
		case "vpcs":
			g.kind = "vpcs"
			size = 1
			portIndex = func(p Port) (int, error) { return p.ID, nil }
		default:
			what := n.Type
			if n.Template != "" && n.Template != n.Type {
				what += " (" + n.Template + ")"
			}
			r.Blocking = append(r.Blocking, fmt.Sprintf("%s: %s ainda não roda no Gantry", n.Name, what))
			continue
		}
		g.ports = make([]string, size)
		g.labels = make([]string, size)
		for _, p := range n.Ports {
			if p.ID < 0 {
				r.Notes = append(r.Notes, fmt.Sprintf("%s: a porta %s não entrou (o Gantry liga só portas Ethernet)", n.Name, p.Name))
				continue
			}
			i, err := portIndex(p)
			if err != nil || i < 0 {
				r.Blocking = append(r.Blocking, fmt.Sprintf("%s: %v", n.Name, err))
				continue
			}
			for len(g.ports) <= i {
				g.ports = append(g.ports, "")
				g.labels = append(g.labels, "")
			}
			g.labels[i] = p.Name
			if p.Net > 0 {
				ends[p.Net] = append(ends[p.Net], end{g.name, i})
			}
		}
		r.Kinds[g.kind] = true
		nodes = append(nodes, g)
	}
	byName := map[string]*gnode{}
	for _, g := range nodes {
		byName[g.name] = g
	}
	// Networks: two ends are a cable; more, a segment (x-redes); a cloud
	// (pnet) goes out of the lab and is left out.
	var nets []int
	for id := range ends {
		nets = append(nets, id)
	}
	sort.Ints(nets)
	links := map[string]bool{}
	var redes []string
	for _, id := range nets {
		e := ends[id]
		nt := l.Nets[id]
		if strings.HasPrefix(strings.ToLower(nt.Type), "pnet") {
			for _, x := range e {
				r.Notes = append(r.Notes, fmt.Sprintf("%s %s: no original vai para a rede %s (%s), fora do lab; aqui fica sem cabo", x.node, byName[x.node].labels[x.port], nt.Name, nt.Type))
			}
			continue
		}
		var name string
		switch {
		case len(e) == 1:
			continue
		case len(e) == 2:
			a, b := strings.ToLower(e[0].node), strings.ToLower(e[1].node)
			if b < a {
				a, b = b, a
			}
			name = a + "-" + b
		default:
			name = strings.ToLower(nodeName(nt.Name, map[string]bool{}))
			if name == "no" {
				name = "rede-" + strconv.Itoa(id)
			}
		}
		base := name
		for i := 2; links[name] || byName[name] != nil; i++ {
			name = base + "-" + strconv.Itoa(i)
		}
		links[name] = true
		if len(e) > 2 {
			redes = append(redes, name)
		}
		for _, x := range e {
			byName[x.node].ports[x.port] = name
		}
	}
	// Notes that repeat on several nodes go in one line.
	noteNodes := map[string][]string{}
	var noteOrder []string
	note := func(node, msg string) {
		if noteNodes[msg] == nil {
			noteOrder = append(noteOrder, msg)
		}
		noteNodes[msg] = append(noteNodes[msg], node)
	}
	for _, g := range nodes {
		r.Remarks = append(r.Remarks, remarks(g.n, g.kind, g.ifs)...)
	}
	// Positions: PNETLab and EVE-NG draw small icons, so nodes close there
	// overlap in Gantry's boxes; the drawing is opened up, the same
	// arrangement with room between the boxes.
	boxes := make([]posBox, len(nodes))
	for i, g := range nodes {
		boxes[i] = posBox{x: g.n.Left, y: g.n.Top, w: max(140, 36+(len(g.ports)-1)*30), h: 72}
	}
	f := spreadFactor(boxes)
	if f > 1 {
		r.Notes = append(r.Notes, fmt.Sprintf("as posições do desenho foram afastadas %.1f vezes, porque os equipamentos ficavam encostados nas caixas do Gantry; a disposição é a mesma do original", f))
	}
	place := map[*gnode][2]int{}
	for i, g := range nodes {
		place[g] = spreadAt(boxes, i, f)
	}
	// compose.yaml
	var c strings.Builder
	fmt.Fprintf(&c, "# %s\n#\n# Importado do PNETLab/EVE-NG (%s) sem adaptar nada: os mesmos\n", oneLine(l.Name), oneLine(file))
	c.WriteString("# equipamentos, placas, cabos e configurações salvas. O que mudou ou ficou\n# de fora está em IMPORTACAO.md; o arquivo original, em original/.\n")
	fmt.Fprintf(&c, "name: %s\n", r.Name)
	if len(redes) > 0 {
		c.WriteString("\n# Segmentos com mais de dois equipamentos (um switch do lab)\nx-redes:\n")
		for _, n := range redes {
			fmt.Fprintf(&c, "  %s: {}\n", n)
		}
	}
	c.WriteString("\n# interfaces: os cabos, na ordem das portas; none é uma porta sem cabo.\nx-vms:\n")
	for _, g := range nodes {
		n := g.n
		fmt.Fprintf(&c, "  %s:\n", g.name)
		switch g.kind {
		case "vpcs":
			c.WriteString("    image: vpcs\n")
		default:
			fmt.Fprintf(&c, "    image: %s\n", yamlStr(n.Image))
			r.Images[g.name] = append(r.Images[g.name], n.Image)
		}
		if g.kind == "dyn" && len(n.Slots) > 0 {
			fmt.Fprintf(&c, "    slots: [%s]\n", strings.Join(quoteAll(n.Slots), ", "))
		}
		if g.kind == "dyn" && strings.EqualFold(n.Template, "c7200") {
			if npe := npeOf(n.DynamipsOptions); npe != "" && npe != "npe-400" {
				fmt.Fprintf(&c, "    npe: %s\n", npe)
			}
		}
		if g.kind == "dyn" && strings.EqualFold(n.Template, "c7200") && n.RAM > composex.DynMaxRAM7200 {
			fmt.Fprintf(&c, "    memory: %dM\n", composex.DynMaxRAM7200)
			why := fmt.Sprintf("o original pede %d MB; o 7200 do Dynamips roda com até %d MB (com mais, não inicia)", n.RAM, composex.DynMaxRAM7200)
			if npe := npeOf(n.DynamipsOptions); npe != "" && npe != "npe-400" {
				why += fmt.Sprintf(", e com a %s o próprio Dynamips do original já usava %d MB", npe, composex.DynMaxRAM7200)
			}
			note(n.Name, why)
		} else if g.kind != "vpcs" && n.RAM > 0 {
			fmt.Fprintf(&c, "    memory: %dM\n", n.RAM)
		}
		ports := make([]string, len(g.ports))
		for i, p := range g.ports {
			if p == "" {
				p = "none"
			}
			ports[i] = p
		}
		// Trailing ports with no cable: an IOL keeps its port groups (the
		// size the original had); the others drop them.
		if g.kind != "iol" {
			for len(ports) > 0 && ports[len(ports)-1] == "none" {
				ports = ports[:len(ports)-1]
			}
		}
		if len(ports) > 0 {
			fmt.Fprintf(&c, "    interfaces: [%s]\n", strings.Join(ports, ", "))
		}
		if strings.TrimSpace(n.Config) != "" {
			ext := ".cfg"
			if g.kind == "vpcs" {
				ext = ".vpc"
			}
			f := "equipamentos/" + g.name + ext
			r.Files[f] = strings.ReplaceAll(n.Config, "\r\n", "\n")
			fmt.Fprintf(&c, "    config: %s\n", f)
		}
		fmt.Fprintf(&c, "    pos: [%d, %d]\n", place[g][0], place[g][1])
		if g.kind == "dyn" {
			if n.IdlePC != "" && n.IdlePC != "0x0" {
				note(n.Name, fmt.Sprintf("o idle-PC do original é %s; no Gantry calcule em Gerenciar (ele guarda um por imagem)", n.IdlePC))
			}
		}
	}
	for _, m := range noteOrder {
		r.Notes = append(r.Notes, strings.Join(noteNodes[m], ", ")+": "+m)
	}
	r.Compose = c.String()
	// The compose made here has only network equipment: anything else (a
	// services: block slipped in by the file) stops the import.
	if p, err := composex.Parse([]byte(r.Compose)); err != nil {
		r.Blocking = append(r.Blocking, fmt.Sprintf("o compose gerado não é válido: %v", err))
	} else if len(p.Services) > 0 || len(p.VMs) != len(nodes) {
		r.Blocking = append(r.Blocking, "o arquivo tem conteúdo que viraria outra coisa além dos equipamentos do lab (containers ou VMs); nada foi importado")
	}
	r.Files["original/"+originalName(file)] = string(original)
	r.Files["IMPORTACAO.md"] = report(l, r, nodes2rows(nodes, func(g *gnode) row {
		return row{g.n.Name, g.name, g.n.Type, g.n.Template, g.kind, g.n.Image, g.n.Slots, g.labels, g.ports, strings.TrimSpace(g.n.Config) != ""}
	}))
	r.Files["README.md"] = readme(l, r)
	return r
}

type row struct {
	orig, name, typ, template, kind, image string
	slots, labels, ports                   []string
	config                                 bool
}

func nodes2rows[T any](xs []T, f func(T) row) []row {
	out := make([]row, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

// npeOf is the NPE of the original's Dynamips options (-t npe-200), when
// it is one Gantry runs.
func npeOf(opts string) string {
	if m := regexp.MustCompile(`-t (npe-\d+)`).FindStringSubmatch(opts); m != nil && composex.DynNPEs[m[1]] {
		return m[1]
	}
	return ""
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		if s == "" {
			s = "none"
		}
		out[i] = s
	}
	return out
}

// oneLine drops line breaks and other control characters: what comes from
// the .unl (an untrusted file) never starts a line of its own in the
// compose, where it could add services: or anything else.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
}

func yamlStr(s string) string {
	s = oneLine(s)
	if s == "" || strings.ContainsAny(s, ":#{}[],&*?|<>=!%@`'\"") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

func report(l *Lab, r *Result, rows []row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Importação de \"%s\"\n\n", l.Name)
	b.WriteString("Importado do PNETLab/EVE-NG sem adaptar nada: cada equipamento roda no mesmo\n")
	b.WriteString("tipo de emulador, com as mesmas placas e a configuração salva no original,\n")
	b.WriteString("gravada na NVRAM antes do primeiro boot. Nenhuma linha de configuração foi\nmudada.\n\n")
	b.WriteString("## Equipamentos\n\n| No original | Aqui | Emulador | Imagem | Portas | Configuração |\n|---|---|---|---|---|---|\n")
	for _, x := range rows {
		emu := map[string]string{"iol": "IOL", "dyn": "Dynamips " + x.template, "vpcs": "VPCS"}[x.kind]
		if x.kind == "dyn" && len(x.slots) > 0 {
			emu += " (" + strings.Join(quoteAll(x.slots), ", ") + ")"
		}
		var ps []string
		for i, p := range x.ports {
			if p != "" && x.labels[i] != "" {
				ps = append(ps, x.labels[i]+" → "+p)
			}
		}
		img := x.image
		if x.kind == "vpcs" {
			img = "(o Gantry leva o VPCS)"
		}
		cfg := "—"
		if x.config {
			cfg = "salva no original"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", x.orig, x.name, emu, img, strings.Join(ps, ", "), cfg)
	}
	if len(r.Blocking) > 0 {
		b.WriteString("\n## Não dá para rodar como está\n\n")
		for _, x := range r.Blocking {
			b.WriteString("- " + x + "\n")
		}
	}
	if len(r.Remarks) > 0 {
		b.WriteString("\n## Pontos de atenção no original\n\nO Gantry importou como está; estes pontos podem ser de propósito (um\nexercício de troubleshooting) ou um engano do original:\n\n")
		for _, x := range r.Remarks {
			b.WriteString("- " + x + "\n")
		}
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n## Diferenças e o que ficou de fora\n\n")
		for _, x := range r.Notes {
			b.WriteString("- " + x + "\n")
		}
	}
	b.WriteString("\n## Imagens que o lab usa\n\nO lab procura estes arquivos nas pastas de imagens da máquina (página da\nmáquina › Configurações › Imagens):\n\n")
	seen := map[string]bool{}
	var imgs []string
	for _, l := range r.Images {
		for _, i := range l {
			if !seen[i] {
				seen[i] = true
				imgs = append(imgs, i)
			}
		}
	}
	sort.Strings(imgs)
	for _, i := range imgs {
		b.WriteString("- `" + i + "`\n")
	}
	return b.String()
}

func readme(l *Lab, r *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.Name)
	if l.Description != "" {
		b.WriteString(l.Description + "\n\n")
	}
	b.WriteString("Lab importado do PNETLab/EVE-NG. Como foi importado e o que mudou:\n[IMPORTACAO.md](IMPORTACAO.md). O arquivo original está em `original/`.\n\n")
	b.WriteString("Configuração inicial de cada equipamento: `equipamentos/`. Ela entra no\nprimeiro boot; para voltar um equipamento a ela, remova o nó e suba de novo.\n")
	for _, t := range l.Texts {
		b.WriteString("\n## Notas da topologia\n\n" + mdTables(t) + "\n")
	}
	for _, p := range l.Workbook {
		b.WriteString("\n## " + p.Title + "\n\n" + mdTables(p.Text) + "\n")
	}
	return b.String()
}

// mdTables puts the header separator under the first row of each table
// (lines starting with |).
func mdTables(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i, l := range lines {
		out = append(out, l)
		if strings.HasPrefix(l, "|") && (i == 0 || !strings.HasPrefix(lines[i-1], "|")) {
			n := strings.Count(strings.TrimSuffix(strings.TrimPrefix(l, "|"), "|"), "|") + 1
			if strings.HasSuffix(strings.TrimSpace(l), "|") {
				n = strings.Count(l, "|") - 1
			}
			if n < 1 {
				n = 1
			}
			out = append(out, "|"+strings.Repeat("---|", n))
		}
	}
	return strings.Join(out, "\n")
}

var (
	reSwitchName = regexp.MustCompile(`(?i)^(sw|switch)[\W_]*\d*`)
	reIfLine     = regexp.MustCompile(`(?m)^interface\s+(\S+)`)
	reEthIf      = regexp.MustCompile(`(?i)^(ethernet|fastethernet|gigabitethernet)(\d+/\d+)$`)
)

// remarks are the things of a node of the original that look wrong.
func remarks(n *Node, kind string, ifs []string) []string {
	var out []string
	if kind == "iol" && reSwitchName.MatchString(n.Name) && composex.ClassifyImage(n.Image) == "cisco-iol" {
		msg := fmt.Sprintf("%s: o nome é de switch, mas a imagem (%s) é IOL L3, um roteador", n.Name, n.Image)
		if strings.TrimSpace(n.Config) == "" {
			msg += "; sem configuração, as portas ficam desligadas e ele não comuta quadros"
		}
		out = append(out, msg)
	}
	if len(ifs) > 0 && strings.TrimSpace(n.Config) != "" {
		have := map[string]bool{}
		for _, x := range ifs {
			have[strings.ToLower(x)] = true
		}
		for _, m := range reIfLine.FindAllStringSubmatch(n.Config, -1) {
			e := reEthIf.FindStringSubmatch(m[1])
			if e == nil || have[strings.ToLower(m[1])] {
				continue
			}
			out = append(out, fmt.Sprintf("%s: a configuração tem a interface %s, que este equipamento não tem (as dele: %s)", n.Name, m[1], strings.Join(ifs, ", ")))
		}
	}
	return out
}

// originalName is the name the .unl keeps in original/: its base name,
// without path parts or odd characters.
func originalName(file string) string {
	n := strings.Trim(reName.ReplaceAllString(path.Base(strings.ReplaceAll(file, "\\", "/")), "_"), "._")
	if n == "" {
		n = "lab"
	}
	if !strings.HasSuffix(strings.ToLower(n), ".unl") {
		n += ".unl"
	}
	return n
}

// posBox: a node of the drawing, its corner and size.
type posBox struct{ x, y, w, h int }

// spreadFactor: how much the positions must be multiplied (around the
// top-left of the drawing) for no two boxes to touch, with room for the
// port names and the cables; 1 when they already have it.
func spreadFactor(b []posBox) float64 {
	clear := func(f float64) bool {
		for i := range b {
			for j := i + 1; j < len(b); j++ {
				a, c := spreadAt(b, i, f), spreadAt(b, j, f)
				if !(a[0]+b[i].w+50 <= c[0] || c[0]+b[j].w+50 <= a[0] || a[1]+b[i].h+60 <= c[1] || c[1]+b[j].h+60 <= a[1]) {
					return false
				}
			}
		}
		return true
	}
	f := 1.0
	for f < 5 && !clear(f) {
		f += 0.1
	}
	return math.Ceil(f*10-1e-6) / 10
}

// spreadAt: where box i goes with the positions multiplied by f, keeping
// the center of each box where the original arrangement puts it.
func spreadAt(b []posBox, i int, f float64) [2]int {
	if len(b) == 0 {
		return [2]int{}
	}
	x0, y0 := b[0].x, b[0].y
	for _, p := range b {
		x0, y0 = min(x0, p.x), min(y0, p.y)
	}
	p := b[i]
	cx := float64(x0) + (float64(p.x)+float64(p.w)/2-float64(x0))*f
	cy := float64(y0) + (float64(p.y)+float64(p.h)/2-float64(y0))*f
	return [2]int{max(0, int(math.Round(cx-float64(p.w)/2))), max(0, int(math.Round(cy-float64(p.h)/2)))}
}
