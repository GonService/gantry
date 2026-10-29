package server

import (
	"slices"
	"testing"

	"gantry/internal/composex"
)

func TestTapSidecarArgs(t *testing.T) {
	p, err := composex.Parse([]byte(`services:
  tx:
    image: alpine
    x-interfaces: [{link: a, ip: 10.0.0.1/24}]
  rx:
    image: alpine
    x-interfaces: [{link: a, ip: 10.0.0.2/24}]
  web:
    image: nginx
`))
	if err != nil {
		t.Fatal(err)
	}
	got := tapSidecarArgs(p)
	if !slices.Equal(got[:3], []string{"rm", "-s", "-f"}) {
		t.Fatalf("começo: %v", got)
	}
	side := got[3:]
	slices.Sort(side)
	if !slices.Equal(side, []string{"rx-cabos", "tx-cabos"}) {
		t.Fatalf("sidecars: %v", side)
	}
}
