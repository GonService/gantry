package kube

import (
	"encoding/json"
	"strings"
	"testing"
)

func driftOf(t *testing.T, manifest, live string) []DriftChange {
	t.Helper()
	docs := manifestDocs(manifest)
	if len(docs) != 1 {
		t.Fatalf("manifest: %d docs", len(docs))
	}
	var l map[string]any
	if err := json.Unmarshal([]byte(live), &l); err != nil {
		t.Fatal(err)
	}
	if docs[0]["kind"] == "Secret" {
		secretStrings(docs[0])
	}
	var ch []DriftChange
	diffValue("", docs[0], l, &ch)
	return ch
}

const deployManifest = `---
# Source: web/templates/deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels: {app: web}
  annotations: {}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  template:
    metadata:
      labels: {app: web}
    spec:
      containers:
        - name: web
          image: nginx:1.27
          ports: [{containerPort: 80}]
          env:
            - {name: MODE, value: prod}
            - {name: EMPTY, value: null}
          resources:
            requests: {cpu: 0.5, memory: 128Mi}
            limits: {cpu: "1", memory: 1Gi}
      nodeSelector: null
`

const deployLive = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"loja","uid":"x","resourceVersion":"9","generation":3,
 "creationTimestamp":"2026-01-01T00:00:00Z","labels":{"app":"web","app.kubernetes.io/managed-by":"Helm"},
 "annotations":{"deployment.kubernetes.io/revision":"3","meta.helm.sh/release-name":"web"}},
 "spec":{"replicas":2,"revisionHistoryLimit":10,"selector":{"matchLabels":{"app":"web"}},"strategy":{"type":"RollingUpdate"},
  "template":{"metadata":{"creationTimestamp":null,"labels":{"app":"web"}},"spec":{"containers":[{"name":"web","image":"nginx:1.27",
   "ports":[{"containerPort":80,"protocol":"TCP"}],"env":[{"name":"MODE","value":"prod"},{"name":"EMPTY"}],
   "resources":{"requests":{"cpu":"500m","memory":"128Mi"},"limits":{"cpu":"1","memory":"1Gi"}},"terminationMessagePath":"/dev/termination-log","imagePullPolicy":"IfNotPresent"}],
   "restartPolicy":"Always","dnsPolicy":"ClusterFirst"}}},
 "status":{"replicas":2}}`

func TestHelmDriftSameDeployment(t *testing.T) {
	if ch := driftOf(t, deployManifest, deployLive); len(ch) != 0 {
		t.Fatalf("false positives: %+v", ch)
	}
}

func TestHelmDriftChangedDeployment(t *testing.T) {
	live := strings.Replace(deployLive, `"replicas":2,"revisionHistoryLimit"`, `"replicas":5,"revisionHistoryLimit"`, 1)
	live = strings.Replace(live, `"image":"nginx:1.27"`, `"image":"nginx:1.28"`, 1)
	live = strings.Replace(live, `{"name":"MODE","value":"prod"}`, `{"name":"MODE","value":"dev"},{"name":"DEBUG","value":"1"}`, 1)
	ch := driftOf(t, deployManifest, live)
	got := map[string]DriftChange{}
	for _, c := range ch {
		got[c.Path] = c
	}
	want := map[string][2]string{
		"spec.replicas": {"2", "5"},
		"spec.template.spec.containers[name=web].image":                {"nginx:1.27", "nginx:1.28"},
		"spec.template.spec.containers[name=web].env[name=MODE].value": {"prod", "dev"},
	}
	for p, v := range want {
		c, ok := got[p]
		if !ok || c.Want != v[0] || c.Have != v[1] {
			t.Errorf("%s: %+v (all: %+v)", p, c, ch)
		}
	}
	if c, ok := got["spec.template.spec.containers[name=web].env[name=DEBUG]"]; !ok || c.Want != "—" {
		t.Errorf("added env not seen: %+v", ch)
	}
	if len(ch) != 4 {
		t.Errorf("%d changes: %+v", len(ch), ch)
	}
}

func TestHelmDriftService(t *testing.T) {
	m := `apiVersion: v1
kind: Service
metadata: {name: web}
spec:
  type: ClusterIP
  selector: {app: web}
  ports:
    - port: 80
      targetPort: http
      name: http
`
	live := `{"metadata":{"name":"web"},"spec":{"type":"ClusterIP","clusterIP":"10.0.0.5","clusterIPs":["10.0.0.5"],"selector":{"app":"web"},
	 "ports":[{"name":"http","port":80,"targetPort":"http","protocol":"TCP"}],"sessionAffinity":"None"}}`
	if ch := driftOf(t, m, live); len(ch) != 0 {
		t.Fatalf("false positives: %+v", ch)
	}
	if ch := driftOf(t, m, strings.Replace(live, `"type":"ClusterIP"`, `"type":"NodePort"`, 1)); len(ch) != 1 || ch[0].Path != "spec.type" {
		t.Fatalf("type change: %+v", ch)
	}
}

func TestHelmDriftConfigMapAndIngress(t *testing.T) {
	m := `apiVersion: v1
kind: ConfigMap
metadata: {name: cfg}
data:
  a: "1"
  port: "8080"
`
	if ch := driftOf(t, m, `{"metadata":{"name":"cfg"},"data":{"a":"1","port":"8080"}}`); len(ch) != 0 {
		t.Fatalf("cm: %+v", ch)
	}
	if ch := driftOf(t, m, `{"metadata":{"name":"cfg"},"data":{"a":"2","port":"8080"}}`); len(ch) != 1 || ch[0].Path != "data.a" {
		t.Fatalf("cm change: %+v", ch)
	}
	ing := `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: web}
spec:
  ingressClassName: nginx
  rules:
    - host: loja.example
      http:
        paths:
          - path: /
            pathType: Prefix
            backend: {service: {name: web, port: {number: 80}}}
`
	live := `{"metadata":{"name":"web"},"spec":{"ingressClassName":"nginx","rules":[{"host":"loja.example","http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":"web","port":{"number":80}}}}]}}]},"status":{"loadBalancer":{}}}`
	if ch := driftOf(t, ing, live); len(ch) != 0 {
		t.Fatalf("ingress: %+v", ch)
	}
}

func TestHelmDriftSecretStringData(t *testing.T) {
	m := `apiVersion: v1
kind: Secret
metadata: {name: s}
type: Opaque
stringData:
  password: hunter2
`
	if ch := driftOf(t, m, `{"metadata":{"name":"s"},"type":"Opaque","data":{"password":"aHVudGVyMg=="}}`); len(ch) != 0 {
		t.Fatalf("secret: %+v", ch)
	}
	if ch := driftOf(t, m, `{"metadata":{"name":"s"},"type":"Opaque","data":{"password":"eA=="}}`); len(ch) != 1 {
		t.Fatalf("secret change: %+v", ch)
	}
}

func TestHelmDriftManagers(t *testing.T) {
	var live map[string]any
	_ = json.Unmarshal([]byte(`{"metadata":{"managedFields":[{"manager":"helm","operation":"Update"},{"manager":"kubectl-edit","operation":"Update","time":"2026-10-01T10:00:00Z"},{"manager":"kube-controller-manager","operation":"Update","subresource":"status"}]}}`), &live)
	m := managers(live)
	if len(m) != 1 || !strings.HasPrefix(m[0], "kubectl-edit (update)") {
		t.Fatalf("%v", m)
	}
}
