package kube

import (
	"encoding/json"
	"testing"
	"time"
)

func mustObj(t *testing.T, s string) obj {
	t.Helper()
	var o obj
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPodWorkload(t *testing.T) {
	cases := map[string]string{
		`{"metadata":{"name":"web-7f9c-x","labels":{"pod-template-hash":"7f9c"},"ownerReferences":[{"kind":"ReplicaSet","name":"web-7f9c","controller":true}]}}`: "Deployment/web",
		`{"metadata":{"name":"rs-x","ownerReferences":[{"kind":"ReplicaSet","name":"solto"}]}}`:                                                                  "ReplicaSet/solto",
		`{"metadata":{"name":"db-0","ownerReferences":[{"kind":"StatefulSet","name":"db"}]}}`:                                                                    "StatefulSet/db",
		`{"metadata":{"name":"avulso"}}`: "Pod/avulso",
	}
	for in, want := range cases {
		if got := PodWorkload(mustObj(t, in)); got != want {
			t.Errorf("%s: %q, queria %q", in, got, want)
		}
	}
}

// The watch of pods tells once, when a pod starts to terminate.
func TestPodLeftOnce(t *testing.T) {
	k := &Client{}
	got := make(chan LeavingPod, 4)
	k.SetPodLeaving(func(p LeavingPod) { got <- p })
	run := mustObj(t, `{"metadata":{"name":"a","namespace":"ns","uid":"u"},"spec":{"containers":[{"name":"c"}]},"status":{"containerStatuses":[{"name":"c","restartCount":2,"state":{"running":{}}}]}}`)
	term := mustObj(t, `{"metadata":{"name":"a","namespace":"ns","uid":"u","deletionTimestamp":"2026-10-15T10:00:00Z"},"spec":{"containers":[{"name":"c"}]},"status":{"containerStatuses":[{"name":"c","restartCount":2,"state":{"running":{}}}]}}`)
	k.podLeft(nil, run, false)  // a pod that runs: nothing
	k.podLeft(run, term, false) // starts to go: once
	k.podLeft(term, term, false)
	k.podLeft(term, term, true) // deleted after: already saved
	select {
	case p := <-got:
		if p.Name != "a" || len(p.Containers) != 1 || p.Containers[0].Restarts != 2 || !p.Containers[0].Started {
			t.Fatalf("%+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("não avisou")
	}
	select {
	case p := <-got:
		t.Fatalf("avisou de novo: %+v", p)
	case <-time.After(100 * time.Millisecond):
	}
}
