package composex

import (
	"fmt"
	"strings"
)

// DynAdapter is an Ethernet adapter of a Dynamips router: how many ports it
// has and how IOS names them.
type DynAdapter struct {
	Ports  int
	Prefix string // FastEthernet, Ethernet, GigabitEthernet
}

// DynAdapters are the Ethernet adapters each model takes, by the name
// Dynamips (and PNETLab, GNS3) give them.
var DynAdapters = map[string]map[string]DynAdapter{
	"7200": {
		"C7200-IO-FE": {1, "FastEthernet"}, "C7200-IO-2FE": {2, "FastEthernet"}, "C7200-IO-GE-E": {1, "GigabitEthernet"},
		"PA-FE-TX": {1, "FastEthernet"}, "PA-2FE-TX": {2, "FastEthernet"}, "PA-4E": {4, "Ethernet"}, "PA-8E": {8, "Ethernet"},
		"PA-GE": {1, "GigabitEthernet"},
	},
	"3725": {"GT96100-FE": {2, "FastEthernet"}, "NM-1FE-TX": {1, "FastEthernet"}, "NM-16ESW": {16, "FastEthernet"}},
	"2691": {"GT96100-FE": {2, "FastEthernet"}, "NM-1FE-TX": {1, "FastEthernet"}, "NM-16ESW": {16, "FastEthernet"}},
}

// dynChassis: the adapter built into slot 0, the slots for adapters, and the
// one Gantry puts in them when the compose does not say (slots:).
var dynChassis = map[string]struct {
	builtin string
	slots   int
	filler  string
}{
	"7200": {"C7200-IO-FE", 6, "PA-2FE-TX"},
	"3725": {"GT96100-FE", 2, "NM-16ESW"},
	"2691": {"GT96100-FE", 1, "NM-16ESW"},
}

// DynPort is one Ethernet port of a Dynamips router.
type DynPort struct {
	Slot, Port int
	Adapter    string
	Name       string // as IOS names it: FastEthernet2/0
}

// Short is the short name of the port: f2/0, e1/3, g0/0.
func (p DynPort) Short() string {
	i := strings.IndexAny(p.Name, "0123456789")
	return strings.ToLower(p.Name[:1]) + p.Name[i:]
}

// DynLayout lists the Ethernet ports of a router in the order of
// interfaces:. slots names the adapter of slot 1, 2…; empty (or none) is an
// empty slot. Without slots, every slot gets the default adapter.
func DynLayout(platform string, slots []string) ([]DynPort, error) {
	ch, ok := dynChassis[platform]
	if !ok {
		return nil, fmt.Errorf("modelo %q do Dynamips desconhecido (use 7200, 3725 ou 2691)", platform)
	}
	if len(slots) > ch.slots {
		return nil, fmt.Errorf("o c%s tem %d slots de placa (slots: tem %d)", platform, ch.slots, len(slots))
	}
	cards := []string{ch.builtin}
	if slots == nil {
		for i := 0; i < ch.slots; i++ {
			cards = append(cards, ch.filler)
		}
	} else {
		cards = append(cards, slots...)
	}
	var out []DynPort
	for s, a := range cards {
		a = strings.ToUpper(strings.TrimSpace(a))
		if a == "" || a == "NONE" {
			continue
		}
		ad, ok := DynAdapters[platform][a]
		if !ok {
			var names []string
			for n := range DynAdapters[platform] {
				if n != ch.builtin {
					names = append(names, n)
				}
			}
			return nil, fmt.Errorf("a placa %s não serve no c%s (Ethernet: %s)", a, platform, strings.Join(sortStrings(names), ", "))
		}
		for p := 0; p < ad.Ports; p++ {
			out = append(out, DynPort{Slot: s, Port: p, Adapter: a, Name: fmt.Sprintf("%s%d/%d", ad.Prefix, s, p)})
		}
	}
	return out, nil
}

func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}

// DynNPEs are the processor boards of the 7200 that run MIPS IOS images
// (the npe-g1/g2 take other images).
var DynNPEs = map[string]bool{"npe-100": true, "npe-150": true, "npe-175": true, "npe-200": true, "npe-225": true, "npe-300": true, "npe-400": true}

// DynMaxRAM7200 is the most RAM a 7200 runs with in Dynamips (MB).
const DynMaxRAM7200 = 256
