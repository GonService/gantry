package server

import "testing"

func TestPackNodeName(t *testing.T) {
	for _, n := range []string{"SW01", "R1", "pc-1", "PC_02", "r1.lab"} {
		if !packNode.MatchString(n) {
			t.Errorf("%q deveria valer", n)
		}
	}
	for _, n := range []string{"", "..", ".x", "a/b", "../R1", "R 1"} {
		if packNode.MatchString(n) {
			t.Errorf("%q não deveria valer", n)
		}
	}
}
