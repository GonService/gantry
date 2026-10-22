package kube

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"gantry/internal/i18n"
)

// "Why doesn't this pod come up?": rules over the pod, its events and what
// it uses (PVCs, Secrets, ConfigMaps), each with what happened and what to
// do next.

// Finding is one thing wrong with a pod.
type Finding struct {
	Pod       string   `json:"pod,omitempty"`
	Container string   `json:"container,omitempty"`
	Severity  string   `json:"severity"` // err, warn
	Title     string   `json:"title"`
	Detail    string   `json:"detail,omitempty"`
	Next      string   `json:"next,omitempty"`
	Lines     []string `json:"lines,omitempty"` // the end of the log of the run that died
	// At is when it last happened (unix seconds), for what comes from
	// events: the page says how long ago.
	At int64 `json:"at,omitempty"`
}

// probeFresh is how long a liveness/startup failure counts when Gantry
// cannot tell when the container started.
const probeFresh = 10 * 60

var reFieldContainer = regexp.MustCompile(`spec\.(?:initContainers|containers)\{([^}]+)\}`)

var exitHints = map[int]string{
	1:   "o programa terminou com erro (exit 1): veja as últimas linhas do log",
	2:   "erro de uso do comando (exit 2): confira command e args",
	126: "o comando não pôde ser executado (exit 126): permissão ou arquivo que não é executável",
	127: "comando não encontrado (exit 127): confira command, args e se o binário existe na imagem",
	128: "argumento inválido para exit (128)",
	134: "o programa abortou (SIGABRT, exit 134)",
	137: "o container foi morto (SIGKILL, exit 137): falta de memória ou probe de liveness",
	139: "falha de segmentação (SIGSEGV, exit 139)",
	143: "o container recebeu SIGTERM (exit 143) e terminou",
}

var reNotFound = regexp.MustCompile(`(secret|configmap)s? "([^"]+)" not found`)

// DiagnosePod looks at one pod. events are the pod's (any order).
func DiagnosePod(o obj, events []obj) []Finding {
	name := o.str("metadata", "name")
	var out []Finding
	add := func(f Finding) {
		f.Pod = name
		out = append(out, f)
	}
	evs := append([]obj{}, events...)
	sort.Slice(evs, func(i, j int) bool { return evTime(evs[i]) > evTime(evs[j]) })
	lastEvent := func(reasons ...string) obj {
		for _, e := range evs {
			for _, r := range reasons {
				if e.str("reason") == r {
					return e
				}
			}
		}
		return nil
	}
	if o.str("status", "reason") == "Evicted" {
		add(Finding{Severity: "err", Title: "O pod foi despejado do nó (Evicted)", Detail: o.str("status", "message"),
			Next: "O nó ficou sem recurso (memória, disco). O controlador cria outro pod; apague este para limpar a lista."})
	}
	// Not scheduled.
	for _, c := range o.list("status", "conditions") {
		if c.str("type") != "PodScheduled" || c.str("status") != "False" {
			continue
		}
		msg := c.str("message")
		f := Finding{Severity: "err", Title: "Nenhum nó aceitou o pod (Pending)", Detail: msg}
		var next []string
		if strings.Contains(msg, "Insufficient cpu") || strings.Contains(msg, "Insufficient memory") {
			next = append(next, "faltam CPU ou memória livres nos nós para os requests do pod: diminua resources.requests ou aumente o cluster")
		}
		if strings.Contains(msg, "untolerated taint") || strings.Contains(msg, "had taint") {
			next = append(next, "os nós têm taints que o pod não tolera: adicione tolerations ou use outros nós")
		}
		if strings.Contains(msg, "node affinity") || strings.Contains(msg, "node selector") || strings.Contains(msg, "didn't match Pod's node") {
			next = append(next, "nenhum nó casa com nodeSelector/affinity do pod: confira os labels dos nós")
		}
		if strings.Contains(msg, "unbound") && strings.Contains(msg, "PersistentVolumeClaim") {
			next = append(next, "o PVC do pod não está ligado a um volume: veja o PVC abaixo")
		}
		if strings.Contains(msg, "didn't have free ports") {
			next = append(next, "a porta de hostPort já está em uso nos nós")
		}
		if len(next) == 0 {
			next = append(next, "veja os eventos do pod e a capacidade dos nós")
		}
		for i := range next {
			next[i] = i18n.T(next[i])
		}
		f.Next = strings.Join(next, "; ") + "."
		add(f)
	}
	check := func(c obj, init bool) {
		cn := c.str("name")
		kind := "container"
		if init {
			kind = "container de init"
		}
		w := c.str("state", "waiting", "reason")
		wmsg := c.str("state", "waiting", "message")
		last := c.mapOf("lastState", "terminated")
		switch w {
		case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull":
			f := Finding{Container: cn, Severity: "err", Title: fmt.Sprintf("Não consegui baixar a imagem %s", c.str("image")), Detail: wmsg}
			low := strings.ToLower(wmsg)
			switch {
			case strings.Contains(low, "not found") || strings.Contains(low, "manifest unknown"):
				f.Next = "A imagem ou a tag não existe no registro: confira o nome e a tag."
			case strings.Contains(low, "unauthorized") || strings.Contains(low, "denied") || strings.Contains(low, "authentication"):
				f.Next = "O registro pediu login: o pod precisa de imagePullSecrets com um Secret do tipo docker-registry com acesso."
			case w == "ErrImageNeverPull":
				f.Next = "imagePullPolicy: Never e a imagem não está no nó."
			default:
				f.Next = "Confira o nome e a tag; se o registro é privado, o pod precisa de imagePullSecrets."
			}
			if len(o.list("spec", "imagePullSecrets")) == 0 && !strings.Contains(f.Next, "não existe") {
				f.Next = i18n.T(f.Next) + " " + i18n.T("O pod não tem imagePullSecrets.")
			}
			add(f)
		case "CrashLoopBackOff":
			f := Finding{Container: cn, Severity: "err", Title: fmt.Sprintf("O %s %s cai logo depois de iniciar (CrashLoopBackOff)", kind, cn)}
			if last != nil {
				code := obj(last).num("exitCode")
				f.Detail = fmt.Sprintf("última execução: %s, exit %d, reiniciou %d vezes", obj(last).str("reason"), code, c.num("restartCount"))
				if obj(last).str("reason") == "OOMKilled" {
					f.Next = oomNext(o, cn)
				} else if h, ok := exitHints[code]; ok {
					f.Next = h + "."
				} else {
					f.Next = "Veja as últimas linhas do log da execução que caiu."
				}
			}
			add(f)
		case "CreateContainerConfigError", "CreateContainerError":
			f := Finding{Container: cn, Severity: "err", Title: fmt.Sprintf("O %s %s não pôde ser criado (%s)", kind, cn, w), Detail: wmsg}
			if m := reNotFound.FindStringSubmatch(wmsg); m != nil {
				f.Next = fmt.Sprintf("O %s %s não existe no namespace %s: crie-o ou corrija o nome em env/envFrom/volumes.", map[string]string{"secret": "Secret", "configmap": "ConfigMap"}[strings.ToLower(m[1])], m[2], o.str("metadata", "namespace"))
			} else if strings.Contains(wmsg, "couldn't find key") {
				f.Next = "Uma chave usada em env (secretKeyRef/configMapKeyRef) não existe no Secret/ConfigMap."
			}
			add(f)
		case "ContainerCreating", "PodInitializing":
			// Stuck creating: the last event says why (mount, sandbox, network).
			if age := time.Now().Unix() - ts(o.str("metadata", "creationTimestamp")); age > 120 {
				if e := lastEvent("FailedMount", "FailedAttachVolume", "FailedCreatePodSandBox", "FailedScheduling"); e != nil {
					f := Finding{Container: cn, Severity: "warn", Title: fmt.Sprintf("Criando há %s", i18n.T(durationText(age))), Detail: e.str("reason") + ": " + firstLine(e.str("message"))}
					if m := reNotFound.FindStringSubmatch(e.str("message")); m != nil {
						f.Next = fmt.Sprintf("O volume usa o %s %s, que não existe.", m[1], m[2])
					} else if e.str("reason") == "FailedMount" {
						f.Next = "O volume não montou: confira o PVC, o Secret ou o ConfigMap do volume."
					}
					add(f)
				}
			}
		}
		// OOM in the last run while the container runs again now.
		if w != "CrashLoopBackOff" && last != nil && obj(last).str("reason") == "OOMKilled" {
			add(Finding{Container: cn, Severity: "warn", Title: fmt.Sprintf("O %s %s foi morto por falta de memória (OOMKilled)", kind, cn), Next: oomNext(o, cn)})
		}
		if t := c.mapOf("state", "terminated"); t != nil && init && obj(t).num("exitCode") != 0 {
			add(Finding{Container: cn, Severity: "err", Title: fmt.Sprintf("O container de init %s falhou (exit %d)", cn, obj(t).num("exitCode")), Detail: obj(t).str("message"),
				Next: "Os containers principais só iniciam quando os de init terminam bem. Veja o log dele."})
		}
	}
	for _, c := range o.list("status", "initContainerStatuses") {
		check(c, true)
	}
	for _, c := range o.list("status", "containerStatuses") {
		check(c, false)
	}
	// Probes. Their events stay for about an hour after the probe is fine
	// again: a failure counts only while it is still true now (readiness:
	// the container is not ready; liveness and startup: it failed in the
	// container's current run).
	statusOf := map[string]obj{}
	for _, c := range append(o.list("status", "initContainerStatuses"), o.list("status", "containerStatuses")...) {
		statusOf[c.str("name")] = c
	}
	podReady := false
	for _, c := range o.list("status", "conditions") {
		if c.str("type") == "Ready" && c.str("status") == "True" {
			podReady = true
		}
	}
	now := nowUnix()
	seen := map[string]bool{}
	for _, e := range evs {
		if e.str("reason") != "Unhealthy" {
			continue
		}
		msg := e.str("message")
		kind := "readiness"
		if strings.HasPrefix(msg, "Liveness") {
			kind = "liveness"
		} else if strings.HasPrefix(msg, "Startup") {
			kind = "startup"
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		at := evTime(e)
		var cs obj
		if m := reFieldContainer.FindStringSubmatch(e.str("involvedObject", "fieldPath")); m != nil {
			cs = statusOf[m[1]]
		} else if len(statusOf) == 1 {
			for _, x := range statusOf {
				cs = x
			}
		}
		if kind == "readiness" {
			ready := podReady
			if cs != nil {
				ready = cs.get("ready") == true
			}
			if ready {
				continue // ready now: the failures are past
			}
		} else {
			started := int64(0)
			if cs != nil {
				started = ts(cs.str("state", "running", "startedAt"))
			}
			if started > 0 && at > 0 && at < started {
				continue // it failed in an earlier run; this one has not
			}
			if started == 0 && at > 0 && now-at > probeFresh && (cs == nil || cs.get("ready") == true || podReady) {
				continue // old, and the container is fine
			}
		}
		f := Finding{Severity: "warn", Title: fmt.Sprintf("A probe de %s está falhando (%d vezes)", kind, max(1, e.num("count"))), Detail: firstLine(msg), At: at}
		switch kind {
		case "liveness":
			f.Next = "Com a liveness falhando o kubelet reinicia o container: confira a porta, o caminho e o initialDelaySeconds/timeoutSeconds."
		case "startup":
			f.Next = "A aplicação demora mais para subir que a startupProbe permite: aumente failureThreshold ou periodSeconds."
		default:
			f.Next = "Com a readiness falhando o pod sai do Service: confira a porta, o caminho e se a aplicação depende de algo fora do ar."
		}
		add(f)
	}
	// In the app's language (the values too: "container de init").
	for i := range out {
		out[i].Title = i18n.T(out[i].Title)
		out[i].Next = i18n.T(out[i].Next)
		if strings.HasPrefix(out[i].Detail, "última execução") {
			out[i].Detail = i18n.T(out[i].Detail)
		}
	}
	return out
}

func oomNext(o obj, cn string) string {
	for _, c := range o.list("spec", "containers") {
		if c.str("name") == cn {
			if lim := c.str("resources", "limits", "memory"); lim != "" {
				return fmt.Sprintf("O limite de memória é %s: aumente resources.limits.memory ou reduza o uso da aplicação.", lim)
			}
			return "O container não tem limite de memória: o nó ficou sem memória. Defina requests/limits de memória."
		}
	}
	return "Aumente resources.limits.memory."
}

func evTime(e obj) int64 {
	for _, f := range []string{"lastTimestamp", "eventTime", "firstTimestamp"} {
		if t := ts(e.str(f)); t > 0 {
			return t
		}
	}
	return ts(e.str("metadata", "creationTimestamp"))
}

func durationText(s int64) string {
	switch {
	case s < 3600:
		return fmt.Sprintf("%d min", s/60)
	case s < 86400:
		return fmt.Sprintf("%d h", s/3600)
	}
	return fmt.Sprintf("%d dias", s/86400)
}

// Diagnose looks at a pod with what it needs from the API: its events, its
// PVCs and the end of the log of a container that keeps falling.
func (k *Client) Diagnose(ctx context.Context, ns string, pod obj) []Finding {
	name := pod.str("metadata", "name")
	var evl struct {
		Items []obj `json:"items"`
	}
	_ = k.Get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/events", url.Values{"fieldSelector": {"involvedObject.kind=Pod,involvedObject.name=" + name}}, &evl)
	out := DiagnosePod(pod, evl.Items)
	// PVCs not bound.
	for _, v := range pod.list("spec", "volumes") {
		claim := v.str("persistentVolumeClaim", "claimName")
		if claim == "" {
			continue
		}
		var pvc obj
		if err := k.Get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/persistentvolumeclaims/"+url.PathEscape(claim), nil, &pvc); err != nil {
			out = append(out, Finding{Pod: name, Severity: "err", Title: fmt.Sprintf("O PVC %s não existe", claim), Next: "Crie o PVC ou corrija o nome em volumes."})
			continue
		}
		if ph := pvc.str("status", "phase"); ph != "Bound" {
			f := Finding{Pod: name, Severity: "err", Title: fmt.Sprintf("O PVC %s está %s", claim, ph)}
			var pe struct {
				Items []obj `json:"items"`
			}
			_ = k.Get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/events", url.Values{"fieldSelector": {"involvedObject.kind=PersistentVolumeClaim,involvedObject.name=" + claim}}, &pe)
			if len(pe.Items) > 0 {
				sort.Slice(pe.Items, func(i, j int) bool { return evTime(pe.Items[i]) > evTime(pe.Items[j]) })
				f.Detail = firstLine(pe.Items[0].str("message"))
			}
			sc := pvc.str("spec", "storageClassName")
			if sc == "" {
				f.Next = "O PVC não tem storageClassName e o cluster pode não ter uma classe padrão."
			} else {
				f.Next = fmt.Sprintf("A storage class %s não criou o volume: confira o provisionador dela e as cotas.", sc)
			}
			out = append(out, f)
		}
	}
	for i := range out {
		out[i].Title, out[i].Next = i18n.T(out[i].Title), i18n.T(out[i].Next)
	}
	// The end of the log of the run that died.
	for i, f := range out {
		if !strings.Contains(f.Title, "CrashLoopBackOff") || f.Container == "" {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rc, err := k.Logs(lctx, ns, name, LogOptions{Container: f.Container, Previous: true, Tail: 20, LimitBytes: 64 << 10})
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(rc, 64<<10))
			rc.Close()
			txt := strings.TrimRight(string(b), "\n")
			if txt != "" {
				out[i].Lines = strings.Split(txt, "\n")
			}
		}
		cancel()
	}
	return out
}

// DiagnoseWorkload diagnoses the newest pod with a problem among pods.
func (k *Client) DiagnoseWorkload(ctx context.Context, ns string, pods []Row) []Finding {
	var pick *Row
	for i := range pods {
		p := &pods[i]
		if p.Problem == "" {
			continue
		}
		if pick == nil || p.Created > pick.Created {
			pick = p
		}
	}
	if pick == nil {
		return nil
	}
	pk, _ := KindByID("pods")
	o := k.cachedObject(pk, ns, pick.Name)
	if o == nil {
		if err := k.Get(ctx, pk.Path(ns, pick.Name), nil, &o); err != nil {
			return nil
		}
	}
	return k.Diagnose(ctx, ns, o)
}

// nowUnix lets the tests set the clock.
var nowUnix = func() int64 { return time.Now().Unix() }
