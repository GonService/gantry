package server

// Clusters created by Gantry: a project with x-gantry: kubernetes gets its
// cluster in the sidebar by itself once the API server answers (k3s takes
// a few minutes to install on the first boot).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/kube"
	"gantry/internal/labs"
)

var kubeWaiting sync.Map // lab key -> true while waiting for its API

// watchKubeLab waits (in the background) for the project's Kubernetes API
// and adds or updates its cluster.
func (s *Server) watchKubeLab(h *hosts.Host, l labs.Lab, p *composex.Project, out func(string)) {
	if p == nil || p.Kube == nil {
		return
	}
	key := scheduleKey(h, l.ID)
	if _, busy := kubeWaiting.LoadOrStore(key, true); busy {
		return
	}
	if out != nil {
		out(fmt.Sprintf("O cluster Kubernetes entra na barra lateral quando a API da VM %s responder (na primeira vez o k3s leva alguns minutos para instalar).", p.Kube.VM))
	}
	go func() {
		defer applog.Recover("server/kubelab.go:41")
		defer kubeWaiting.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		if err := s.registerKubeLab(ctx, h, l, p.Kube); err != nil {
			log.Printf("cluster do projeto %s: %v", l.ID, err)
		}
	}()
}

func (s *Server) registerKubeLab(ctx context.Context, h *hosts.Host, l labs.Lab, ks *composex.KubeSpec) error {
	var raw string
	for {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := s.vmExec(cctx, h, l.ID, ks.VM, "sudo cat /etc/rancher/k3s/k3s.yaml 2>/dev/null || sudo cat /etc/kubernetes/admin.conf")
		cancel()
		if err == nil && strings.Contains(out, "apiVersion") {
			raw = out
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("a API não respondeu a tempo")
		case <-time.After(10 * time.Second):
		}
	}
	cfg, err := kube.Parse([]byte(raw), "")
	if err != nil {
		return err
	}
	// The file comes from inside the VM: whoever controls it must not get to
	// run commands or read files on this computer through it.
	cfg.Untrusted()
	// The machine reaches the API on the published port.
	cfg.SetServer(fmt.Sprintf("https://127.0.0.1:%d", ks.Port), "127.0.0.1")
	ctxName := cfg.CurrentContext
	if ctxName == "" && len(cfg.Contexts) > 0 {
		ctxName = cfg.Contexts[0].Name
	}
	one, err := cfg.Extract(ctxName)
	if err != nil {
		return err
	}
	labKey := scheduleKey(h, l.ID)
	sum := sha256.Sum256([]byte(labKey))
	id := "g" + hex.EncodeToString(sum[:5])
	dir := filepath.Join(s.configDir(), "clusters")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(file, one, 0o600); err != nil {
		return err
	}
	clusterMu.Lock()
	list := s.clusters()
	found := false
	for i := range list {
		if list[i].ID == id {
			list[i].File, list[i].Context, list[i].Port, list[i].Machine = file, ctxName, ks.Port, h.Machine()
			found = true
		}
	}
	if !found {
		list = append(list, clusterRec{ID: id, Name: l.Name, Source: "gantry", File: file, Context: ctxName, Mode: "livre",
			Lab: labKey, Added: time.Now().Unix(), Machine: h.Machine(), Port: ks.Port})
	}
	err = s.saveClusters(list)
	clusterMu.Unlock()
	if err != nil {
		return err
	}
	forgetKubeClient(id)
	if !found {
		// Next to its machine in the sidebar.
		groupsMu.Lock()
		d := s.loadGroups()
		if f, ok := d.Items[h.Machine()]; ok {
			if _, has := d.Items["cluster:"+id]; !has {
				d.Items["cluster:"+id] = f
				_ = s.saveGroups(d)
			}
		}
		groupsMu.Unlock()
	}
	return nil
}

// dropKubeLab removes the cluster of a project whose VMs were removed.
func (s *Server) dropKubeLab(h *hosts.Host, labID string) {
	key := scheduleKey(h, labID)
	clusterMu.Lock()
	list := s.clusters()
	kept := list[:0]
	var gone []clusterRec
	for _, c := range list {
		if c.Source == "gantry" && c.Lab == key {
			gone = append(gone, c)
			continue
		}
		kept = append(kept, c)
	}
	if len(gone) > 0 {
		_ = s.saveClusters(kept)
	}
	clusterMu.Unlock()
	for _, c := range gone {
		os.Remove(c.File)
		s.forgetItem("cluster:" + c.ID)
		forgetKubeClient(c.ID)
		closeTunnels(c.ID, nil)
	}
}
