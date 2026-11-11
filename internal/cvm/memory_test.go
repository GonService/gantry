package cvm

import "testing"

const sampleMemInfo = `MemTotal:        8135044 kB
MemFree:         5100000 kB
MemAvailable:    6500000 kB
Buffers:           50000 kB
Cached:          1400000 kB
SwapTotal:             0 kB
`

func TestParseMemInfo(t *testing.T) {
	m, err := parseMemInfo(sampleMemInfo)
	if err != nil {
		t.Fatal(err)
	}
	if m.Total != 8135044<<10 || m.Available != 6500000<<10 || m.Cached != 1400000<<10 {
		t.Fatalf("leitura errada: %+v", m)
	}
	if m.Used() != (8135044-6500000)<<10 {
		t.Errorf("usado errado: %d", m.Used())
	}
	if _, err := parseMemInfo("lixo"); err == nil {
		t.Error("aceitou meminfo vazio")
	}
}

func TestBalloonTarget(t *testing.T) {
	max := uint64(8 * gib)
	// Idle guest: 600 MiB used. Goes down to 1.375 GiB (600+768 rounded).
	idle := memInfo{Total: max, Available: max - 600*mib}
	got := balloonTarget(idle, max, max)
	if got != 1408*mib {
		t.Errorf("ociosa: alvo %d MiB", got/mib)
	}
	// Busy guest: 6 GiB used: 6 + 1.5 = 7.5 GiB.
	busy := memInfo{Total: 7 * gib, Available: 1 * gib}
	if got := balloonTarget(busy, 7*gib, max); got != 7680*mib {
		t.Errorf("ocupada: alvo %d MiB", got/mib)
	}
	// Almost full: a whole GiB more at once, never over the spec.
	full := memInfo{Total: 2 * gib, Available: 100 * mib}
	if got := balloonTarget(full, 2*gib, max); got < 3*gib {
		t.Errorf("cheia: alvo %d MiB, devia subir 1 GiB", got/mib)
	}
	if got := balloonTarget(memInfo{Total: max, Available: 0}, max, max); got != max {
		t.Errorf("passou do máximo: %d", got)
	}
	// Never under 1 GiB.
	if got := balloonTarget(memInfo{Total: 2 * gib, Available: 2*gib - 10*mib}, 2*gib, max); got < 1*gib {
		t.Errorf("abaixo de 1 GiB: %d MiB", got/mib)
	}
	// A guest that keeps MemTotal whole and counts the balloon as used:
	// the balloon (6 GiB) is not taken as the guest's own use.
	whole := memInfo{Total: max, Available: max - 6*gib - 500*mib}
	if got := balloonTarget(whole, 2*gib, max); got > 2*gib {
		t.Errorf("contou o balão como uso: %d MiB", got/mib)
	}
}
