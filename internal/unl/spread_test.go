package unl

import "testing"

func TestSpread(t *testing.T) {
	far := []posBox{{0, 0, 140, 72}, {600, 0, 140, 72}}
	if f := spreadFactor(far); f != 1 {
		t.Fatalf("já espaçados: %v", f)
	}
	if p := spreadAt(far, 1, 1); p != [2]int{600, 0} {
		t.Fatalf("sem mudança: %v", p)
	}
	close := []posBox{{100, 100, 140, 72}, {180, 110, 140, 72}, {120, 160, 140, 72}}
	f := spreadFactor(close)
	if f <= 1 {
		t.Fatalf("encostados: %v", f)
	}
	for i := range close {
		for j := i + 1; j < len(close); j++ {
			a, c := spreadAt(close, i, f), spreadAt(close, j, f)
			if !(a[0]+140+50 <= c[0] || c[0]+140+50 <= a[0] || a[1]+72+60 <= c[1] || c[1]+72+60 <= a[1]) {
				t.Fatalf("%d e %d ainda se tocam: %v %v (×%v)", i, j, a, c, f)
			}
		}
	}
	// the one most to the left stays to the left
	if a, b := spreadAt(close, 0, f), spreadAt(close, 1, f); a[0] >= b[0] {
		t.Fatalf("a ordem mudou: %v %v", a, b)
	}
}
