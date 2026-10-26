// Package unl reads the labs of PNETLab and EVE-NG (.unl) and turns them
// into a Gantry lab folder without adapting anything: the same equipment,
// adapters, cables and saved configurations. What Gantry cannot run as it
// is stops the import (Result.Blocking) instead of being replaced by
// something else; what it leaves out is in the report.
package unl

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Lab is what a .unl holds.
type Lab struct {
	Name        string
	Description string
	Nodes       []Node
	Nets        map[int]Net
	Texts       []string  // the text boxes of the topology, as plain text
	Boxes       []TextBox // the same boxes where they are, with their images
	Workbook    []Page    // PNETLab workbook pages
}

// Node is one equipment of the lab.
type Node struct {
	ID              int
	Name            string
	Type            string // iol, dynamips, vpcs, qemu, docker…
	Template        string // c7200, iol, vpcs, vios…
	Image           string
	Icon            string
	Left, Top       int
	Ethernet        int // IOL: Ethernet port groups (4 ports each)
	RAM             int // MB
	Slots           []string
	Config          string
	IdlePC          string
	DynamipsOptions string
	Ports           []Port
}

// Port is a port of a node and the network it is plugged into.
type Port struct {
	ID   int
	Name string
	Net  int
}

// Net is a network of the lab: a cable (two ends), a segment, or a cloud.
type Net struct {
	ID   int
	Name string
	Type string // bridge, ovs, pnet0…pnet9
}

// TextBox is a text box of the topology: its text (Markdown-ish, with
// @@IMGn@@ where its n-th image was), where it is and its size.
type TextBox struct {
	Text            string
	Left, Top, W, H int
	Images          []BoxImage
}

// BoxImage is an image inside a text box (a data: URL in the original).
type BoxImage struct {
	Ext  string // png, jpg, gif, webp
	Data []byte
}

// Page is a page of the workbook, as text.
type Page struct {
	Title string
	Text  string
}

type xLab struct {
	Name        string `xml:"name,attr"`
	Description string `xml:"description"`
	Nodes       []struct {
		Attrs []xml.Attr `xml:",any,attr"`
		Ifs   []struct {
			ID   string `xml:"id,attr"`
			Name string `xml:"name,attr"`
			Type string `xml:"type,attr"`
			Net  string `xml:"network_id,attr"`
		} `xml:"interface"`
	} `xml:"topology>nodes>node"`
	Nets []struct {
		ID   string `xml:"id,attr"`
		Name string `xml:"name,attr"`
		Type string `xml:"type,attr"`
	} `xml:"topology>networks>network"`
	Texts []struct {
		Data string `xml:"data"`
	} `xml:"objects>textobjects>textobject"`
	Configs []struct {
		ID   string `xml:"id,attr"`
		Data string `xml:",chardata"`
	} `xml:"objects>configs>config"`
	Workbooks []struct {
		ID    string   `xml:"id,attr"`
		Pages []string `xml:"content>page"`
		Menu  string   `xml:"menu"`
	} `xml:"workbooks>workbook"`
}

// Parse reads a .unl.
func Parse(b []byte) (*Lab, error) {
	var x xLab
	if err := xml.Unmarshal(b, &x); err != nil {
		return nil, fmt.Errorf("não é um arquivo .unl do PNETLab/EVE-NG: %v", err)
	}
	if len(x.Nodes) == 0 {
		return nil, fmt.Errorf("o arquivo não tem equipamentos (topology > nodes)")
	}
	l := &Lab{Name: x.Name, Description: strings.TrimSpace(x.Description), Nets: map[int]Net{}}
	cfgs := map[string]string{}
	for _, c := range x.Configs {
		cfgs[c.ID] = decodeB64(c.Data)
	}
	for _, xn := range x.Nodes {
		a := map[string]string{}
		for _, at := range xn.Attrs {
			a[at.Name.Local] = at.Value
		}
		n := Node{ID: atoi(a["id"]), Name: strings.TrimSpace(a["name"]), Type: a["type"], Template: a["template"],
			Image: a["image"], Icon: a["icon"], Left: atoi(a["left"]), Top: atoi(a["top"]),
			Ethernet: atoi(a["ethernet"]), RAM: atoi(a["ram"]), IdlePC: a["idlepc"], DynamipsOptions: a["dynamips_options"]}
		for i := 1; i <= 6; i++ {
			n.Slots = append(n.Slots, a["slot"+strconv.Itoa(i)])
		}
		for len(n.Slots) > 0 && n.Slots[len(n.Slots)-1] == "" {
			n.Slots = n.Slots[:len(n.Slots)-1]
		}
		if d := a["config_data"]; d != "" {
			n.Config = decodeB64(d)
		} else if c, ok := cfgs[a["id"]]; ok {
			n.Config = c
		}
		for _, f := range xn.Ifs {
			if f.Type != "" && f.Type != "ethernet" {
				n.Ports = append(n.Ports, Port{ID: -1, Name: f.Name + " (" + f.Type + ")", Net: atoi(f.Net)})
				continue
			}
			n.Ports = append(n.Ports, Port{ID: atoi(f.ID), Name: f.Name, Net: atoi(f.Net)})
		}
		l.Nodes = append(l.Nodes, n)
	}
	sort.SliceStable(l.Nodes, func(i, j int) bool { return l.Nodes[i].ID < l.Nodes[j].ID })
	for _, xn := range x.Nets {
		l.Nets[atoi(xn.ID)] = Net{ID: atoi(xn.ID), Name: xn.Name, Type: xn.Type}
	}
	for _, t := range x.Texts {
		h := decodeText(t.Data)
		if s := strings.TrimSpace(htmlText(h)); s != "" {
			l.Texts = append(l.Texts, s)
		}
		if b, ok := textBox(h); ok {
			l.Boxes = append(l.Boxes, b)
		}
	}
	for _, w := range x.Workbooks {
		for i, p := range w.Pages {
			title := w.ID
			if len(w.Pages) > 1 {
				title += " (" + strconv.Itoa(i+1) + ")"
			}
			if s := strings.TrimSpace(htmlText(jsUnescape(decodeB64(p)))); s != "" {
				l.Workbook = append(l.Workbook, Page{Title: strings.TrimSpace(title), Text: s})
			}
		}
	}
	return l, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func decodeB64(s string) string {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return s
	}
	return string(b)
}

// decodeText: a text box is base64 of the HTML, URL-encoded (PNETLab) or
// not (EVE-NG).
func decodeText(s string) string {
	t := decodeB64(s)
	if strings.HasPrefix(strings.TrimSpace(t), "%3C") {
		if u, err := url.PathUnescape(t); err == nil {
			return u
		}
	}
	return t
}

// jsUnescape undoes JavaScript's escape(): %uXXXX and %XX (Latin-1).
func jsUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+5 < len(s) && s[i+1] == 'u' {
			if v, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
				b.WriteRune(rune(v))
				i += 5
				continue
			}
		}
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteRune(rune(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var (
	reImg      = regexp.MustCompile(`(?is)<img[^>]*>`)
	reBreak    = regexp.MustCompile(`(?i)<(br\s*/?|/p|/h[1-6]|/li|/div|/ul|/ol|/table)>`)
	reLi       = regexp.MustCompile(`(?i)<li[^>]*>`)
	reHead     = regexp.MustCompile(`(?i)<h([1-6])[^>]*>`)
	reRow      = regexp.MustCompile(`(?i)</tr>`)
	reCell     = regexp.MustCompile(`(?i)</t[dh]>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlank    = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
	reEmptyEnd = regexp.MustCompile(`\|\s*\|$`)
)

// htmlText turns the HTML of a text box or workbook page into Markdown-ish
// text: headings, list items, table rows as | a | b |, no images.
func htmlText(s string) string {
	s = reImg.ReplaceAllString(s, "")
	s = reHead.ReplaceAllStringFunc(s, func(m string) string {
		return "\n\n" + strings.Repeat("#", atoi(reHead.FindStringSubmatch(m)[1])+1) + " "
	})
	s = reLi.ReplaceAllString(s, "\n- ")
	s = reRow.ReplaceAllString(s, "|\n")
	s = regexp.MustCompile(`(?i)<tr[^>]*>`).ReplaceAllString(s, "| ")
	s = reCell.ReplaceAllString(s, " | ")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.ReplaceAll(s, "|  |", "|")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t")
		l = strings.ReplaceAll(l, " |  | ", " | ")
		l = reEmptyEnd.ReplaceAllString(l, "|")
		lines = append(lines, strings.TrimLeft(l, " \t"))
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var (
	reBoxStyle = regexp.MustCompile(`(?is)^\s*<div[^>]*style="([^"]*)"`)
	reImgSrc   = regexp.MustCompile(`(?is)<img[^>]*src="data:image/(png|jpe?g|gif|webp);base64,([A-Za-z0-9+/=\s]+)"[^>]*>`)
)

// textBox reads a text box of the topology: the position and size from the
// style of its outer div, the images (data: URLs) apart, the text as
// htmlText makes it. False when it has neither text nor image.
func textBox(h string) (TextBox, bool) {
	var b TextBox
	if m := reBoxStyle.FindStringSubmatch(h); m != nil {
		for _, decl := range strings.Split(m[1], ";") {
			k, v, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "px"), 64)
			if err != nil {
				continue
			}
			switch strings.TrimSpace(strings.ToLower(k)) {
			case "left":
				b.Left = int(n + 0.5)
			case "top":
				b.Top = int(n + 0.5)
			case "width":
				b.W = int(n + 0.5)
			case "height":
				b.H = int(n + 0.5)
			}
		}
	}
	h = reImgSrc.ReplaceAllStringFunc(h, func(m string) string {
		sub := reImgSrc.FindStringSubmatch(m)
		data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(sub[2]), ""))
		if err != nil || len(data) == 0 {
			return ""
		}
		ext := strings.ToLower(sub[1])
		if ext == "jpeg" {
			ext = "jpg"
		}
		b.Images = append(b.Images, BoxImage{Ext: ext, Data: data})
		// a marker that survives htmlText: it becomes the Markdown of the image
		return "<p>@@IMG" + strconv.Itoa(len(b.Images)) + "@@</p>"
	})
	b.Text = strings.TrimSpace(htmlText(h))
	return b, b.Text != ""
}
