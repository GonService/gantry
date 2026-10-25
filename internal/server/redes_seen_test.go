package server

import "testing"

func TestParsePortsSeen(t *testing.T) {
	got := parsePortsSeen("now 1000\nR1 0 990\nR1 1 1000\nlixo\nR2 x 1\n")
	if len(got) != 2 || got[0] != (portSeen{"R1", 0, 10}) || got[1] != (portSeen{"R1", 1, 0}) {
		t.Fatalf("%+v", got)
	}
	if len(parsePortsSeen("R1 0 990\n")) != 0 {
		t.Fatal("sem now, nada")
	}
}
