package kube

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive runs against a real cluster when GANTRY_KUBECONFIG is set.
func TestLive(t *testing.T) {
	p := os.Getenv("GANTRY_KUBECONFIG")
	if p == "" {
		t.Skip("sem GANTRY_KUBECONFIG")
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	k, err := cfg.NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	v, err := k.Version(ctx)
	if err != nil || !strings.HasPrefix(v, "v1.") {
		t.Fatalf("versão: %q %v", v, err)
	}
	for _, kind := range Kinds {
		rows, err := k.List(ctx, kind, "", "")
		if err != nil && !NotFound(err) {
			t.Errorf("%s: %v", kind.ID, err)
			continue
		}
		t.Logf("%s: %d", kind.ID, len(rows))
	}
	res, err := k.Apply(ctx, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: gantry-teste\ndata:\n  a: \"1\"\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: gantry-web\nspec:\n  replicas: 1\n  selector:\n    matchLabels: {app: gweb}\n  template:\n    metadata:\n      labels: {app: gweb}\n    spec:\n      containers:\n      - name: web\n        image: rancher/mirrored-library-busybox:1.36.1\n        command: [sh, -c, 'echo oi; sleep 3600']\n", "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res {
		if a.Error != "" {
			t.Errorf("apply %s/%s: %s", a.Kind, a.Name, a.Error)
		}
	}
	dk, _ := KindByID("deployments")
	if err := k.Scale(ctx, dk, "default", "gantry-web", 2); err != nil {
		t.Fatal(err)
	}
	if err := k.Restart(ctx, dk, "default", "gantry-web"); err != nil {
		t.Fatal(err)
	}
	y, err := k.YAML(ctx, dk, "default", "gantry-web", false)
	if err != nil || !strings.Contains(y, "replicas: 2") || strings.Contains(y, "managedFields") {
		t.Fatalf("yaml: %v\n%s", err, y)
	}
	if err := k.Replace(ctx, dk, "default", "gantry-web", strings.Replace(y, "replicas: 2", "replicas: 3", 1)); err != nil {
		t.Fatal(err)
	}
	ck, _ := KindByID("configmaps")
	if err := k.Delete(ctx, ck, "default", "gantry-teste"); err != nil {
		t.Fatal(err)
	}
	rel, err := k.HelmReleases(ctx, "")
	t.Logf("helm: %+v %v", rel, err)
}
