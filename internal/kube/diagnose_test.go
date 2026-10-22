package kube

import (
	"encoding/json"
	"strings"
	"testing"
)

func pod(t *testing.T, js string) obj {
	t.Helper()
	var o obj
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestDiagnosePod(t *testing.T) {
	cases := []struct {
		name, pod, events string
		want              []string
	}{
		{"imagem", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Pending","containerStatuses":[{"name":"app","image":"nginx:nao","state":{"waiting":{"reason":"ImagePullBackOff","message":"manifest unknown"}}}]}}`, `[]`,
			[]string{"baixar a imagem nginx:nao", "não existe no registro"}},
		{"login", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"containerStatuses":[{"name":"app","image":"r/x","state":{"waiting":{"reason":"ErrImagePull","message":"unauthorized: authentication required"}}}]}}`, `[]`,
			[]string{"imagePullSecrets", "O pod não tem imagePullSecrets"}},
		{"crash", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"containerStatuses":[{"name":"app","restartCount":5,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"Error","exitCode":127}}}]}}`, `[]`,
			[]string{"CrashLoopBackOff", "exit 127", "comando não encontrado"}},
		{"oom", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app","resources":{"limits":{"memory":"64Mi"}}}]},"status":{"containerStatuses":[{"name":"app","restartCount":2,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137}}}]}}`, `[]`,
			[]string{"OOMKilled", "64Mi"}},
		{"pending", `{"metadata":{"name":"p"},"spec":{},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","message":"0/3 nodes are available: 3 Insufficient cpu, 1 node(s) had untolerated taint {x: y}."}]}}`, `[]`,
			[]string{"Nenhum nó aceitou", "requests", "tolerations"}},
		{"secret", `{"metadata":{"name":"p","namespace":"app"},"spec":{"containers":[{"name":"app"}]},"status":{"containerStatuses":[{"name":"app","state":{"waiting":{"reason":"CreateContainerConfigError","message":"secret \"db-pass\" not found"}}}]}}`, `[]`,
			[]string{"CreateContainerConfigError", "Secret db-pass não existe no namespace app"}},
		{"probe", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"name":"app","state":{"running":{}}}]}}`,
			`[{"reason":"Unhealthy","message":"Liveness probe failed: Get \"http://10.0.0.1:8080/health\": connection refused","count":7,"lastTimestamp":"2026-10-19T10:00:00Z"}]`,
			[]string{"liveness", "7 vezes", "reinicia o container"}},
		{"init", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"initContainerStatuses":[{"name":"migra","state":{"terminated":{"exitCode":3,"message":"falhou"}}}]}}`, `[]`,
			[]string{"init migra falhou (exit 3)"}},
		{"montagem", `{"metadata":{"name":"p","creationTimestamp":"2020-01-01T00:00:00Z"},"spec":{"containers":[{"name":"app"}]},"status":{"containerStatuses":[{"name":"app","state":{"waiting":{"reason":"ContainerCreating"}}}]}}`,
			`[{"reason":"FailedMount","message":"MountVolume.SetUp failed for volume \"cfg\" : configmap \"web-cfg\" not found","lastTimestamp":"2026-10-19T10:00:00Z"}]`,
			[]string{"Criando há", "configmap web-cfg"}},
		// The user's case: probes failed a while ago, the pod is fine now.
		{"probe resolvida", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"app","ready":true,"state":{"running":{"startedAt":"2026-10-19T10:05:00Z"}}}]}}`,
			`[{"reason":"Unhealthy","message":"Liveness probe failed: HTTP probe failed with statuscode: 500","count":6,"lastTimestamp":"2026-10-19T10:00:00Z","involvedObject":{"fieldPath":"spec.containers{app}"}},
			  {"reason":"Unhealthy","message":"Readiness probe failed: HTTP probe failed with statuscode: 500","count":6,"lastTimestamp":"2026-10-19T10:00:00Z","involvedObject":{"fieldPath":"spec.containers{app}"}}]`, nil},
		// Still failing in the current run: shows, with when.
		{"probe agora", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"state":{"running":{"startedAt":"2026-10-19T09:00:00Z"}}}]}}`,
			`[{"reason":"Unhealthy","message":"Readiness probe failed: HTTP probe failed with statuscode: 500","count":3,"lastTimestamp":"2026-10-19T10:00:00Z","involvedObject":{"fieldPath":"spec.containers{app}"}}]`,
			[]string{"readiness", "3 vezes", `"at":1792404000`}},
		{"saudável", `{"metadata":{"name":"p"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"name":"app","state":{"running":{}}}]}}`, `[]`, nil},
	}
	for _, c := range cases {
		var evs []obj
		json.Unmarshal([]byte(c.events), &evs)
		got := DiagnosePod(pod(t, c.pod), evs)
		b, _ := json.Marshal(got)
		if c.want == nil {
			if len(got) != 0 {
				t.Errorf("%s: %s", c.name, b)
			}
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s: faltou %q em %s", c.name, w, b)
			}
		}
	}
}
