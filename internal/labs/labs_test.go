package labs

import (
	"testing"

	"gantry/internal/engine"
)

func ctr(id, state string, labels map[string]string) engine.Container {
	return engine.Container{ID: id, Names: []string{"/" + id}, State: state, Labels: labels}
}

func TestGroup(t *testing.T) {
	cs := []engine.Container{
		ctr("web", "running", map[string]string{LabelProject: "demo", LabelService: "web", LabelWorkingDir: "/labs/demo", LabelFiles: "/labs/demo/compose.yaml"}),
		ctr("db", "exited", map[string]string{LabelProject: "demo", LabelService: "db"}),
		ctr("solo", "running", nil),
		ctr("pod", "running", map[string]string{PodmanProject: "obs"}),
	}
	known := map[string]Known{"old": {Dir: "/labs/old"}, "demo": {Dir: "/x"}}
	got := Group(cs, known)

	if len(got) != 4 {
		t.Fatalf("esperava 4 labs, veio %d", len(got))
	}
	ids := []string{got[0].ID, got[1].ID, got[2].ID, got[3].ID}
	want := []string{"demo", "obs", "old", Loose}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ordem %v, esperava %v", ids, want)
		}
	}
	demo := got[0]
	if demo.Running != 1 || demo.Total != 2 || demo.WorkingDir != "/labs/demo" || len(demo.Files) != 1 {
		t.Fatalf("lab demo errado: %+v", demo)
	}
	if demo.Containers[0].Service != "db" {
		t.Fatalf("containers deveriam vir ordenados por serviço")
	}
	if got[2].Total != 0 || got[2].WorkingDir != "/labs/old" {
		t.Fatalf("lab lembrado errado: %+v", got[2])
	}
	if !got[3].Loose || got[3].Name != "Avulsos" {
		t.Fatalf("avulsos errado: %+v", got[3])
	}
}
