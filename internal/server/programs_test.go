package server

import (
	"reflect"
	"testing"
)

func TestProgramsFor(t *testing.T) {
	cases := []struct {
		kinds map[string]bool
		want  []string
	}{
		{map[string]bool{"container": true}, nil},
		{map[string]bool{"iol": true, "vpcs": true}, []string{"python3"}},
		{map[string]bool{"dyn": true}, []string{"python3", "dynamips"}},
	}
	for _, c := range cases {
		if got := programsFor(c.kinds); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%v: %v, quero %v", c.kinds, got, c.want)
		}
	}
}
