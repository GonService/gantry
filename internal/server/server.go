// Package server exposes the HTTP API and serves the embedded web UI.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/composex"
	"gantry/internal/config"
	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/i18n"
	"gantry/internal/labs"
	"gantry/internal/secrets"
	"gantry/internal/templates"
)

type Server struct {
	// composeEdit serializes the edits of compose files made by the page
	// (cables, positions): each reads, changes and writes the whole file.
	composeEdit sync.Mutex
	// mcpToken is the token of the assistants (gantry mcp), "" when off.
	mcpMu      sync.Mutex
	mcpToken   string
	tplOnce    sync.Once
	tpls       *templates.Store
	cfg        *config.Config
	store      *labs.Store
	static     fs.FS
	token      string
	addr       string
	loginMu    sync.Mutex
	loginCodes map[string]time.Time
	loopback   bool
	access     Access
	origins    []string

	mu    sync.RWMutex
	hosts []*hosts.Host

	termMu sync.Mutex
	terms  map[string]*term

	prompts *prompts

	jobMu sync.Mutex
	jobs  map[string]*job

	agentMu    sync.Mutex
	tools      map[*hosts.Host]map[string]bool // compose commands found per host
	obsFound   map[*hosts.Host]obsCache        // observability services found per host
	agentInfo  map[*hosts.Host]agentCache
	collectors map[*hosts.Host]collectorEntry
	// VM samplers per machine (hosts.Machine), shared by its hosts.
	vmCollectors map[string]collectorEntry
	stopLoops    context.CancelFunc // scheduled backups
	sec          secrets.Store
	secOnce      sync.Once
}

func New(cfg *config.Config, store *labs.Store, static fs.FS, loopback bool) *Server {
	s := &Server{cfg: cfg, store: store, static: static, loopback: loopback, terms: map[string]*term{}, jobs: map[string]*job{}, prompts: newPrompts(),
		agentInfo: map[*hosts.Host]agentCache{}, tools: map[*hosts.Host]map[string]bool{}, obsFound: map[*hosts.Host]obsCache{}, collectors: map[*hosts.Host]collectorEntry{}, vmCollectors: map[string]collectorEntry{}}
	s.token = randomID(24)
	engine.RegistryAuth = s.registryLogin
	s.reload()
	var bctx context.Context
	bctx, s.stopLoops = context.WithCancel(context.Background())
	go func() { defer applog.Recover("server/server.go:75"); s.backupLoop(bctx) }()
	go func() { defer applog.Recover("server/server.go:76"); s.autoApplyLoop(bctx) }()
	go func() { defer applog.Recover("server/server.go:argo"); s.forgetArgoTokens() }()
	return s
}

// Token is the secret every API call carries (header or ?t=).
func (s *Server) Token() string { return s.token }

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) reload() {
	var list []*hosts.Host
	names := map[string]bool{}
	urls := map[string]bool{}
	s.mu.RLock()
	prev := map[string]*hosts.Host{}
	for _, h := range s.hosts {
		prev[h.Name+"\x00"+h.URL+"\x00"+h.ConfigSocket()] = h
	}
	s.mu.RUnlock()
	add := func(c config.Host) {
		// The same server may be listed once per engine (socket).
		addr := c.URL + "\x00" + c.Socket
		if names[c.Name] || urls[addr] {
			return
		}
		// Keep the same object when nothing changed, so ssh tunnels survive.
		if h, ok := prev[c.Name+"\x00"+addr]; ok {
			names[c.Name], urls[addr] = true, true
			list = append(list, h)
			return
		}
		h, err := hosts.New(c)
		if err != nil {
			log.Printf("host %s ignorado: %v", c.Name, err)
			return
		}
		s.applyDirect(h)
		if h.Kind == hosts.WSL {
			h.SetSetup(s.setupWSL)
		}
		names[c.Name], urls[addr] = true, true
		list = append(list, h)
	}
	for _, c := range s.cfg.Hosts {
		add(c)
	}
	if s.cfg.AutodetectEnabled() {
		for _, c := range config.Detect() {
			if s.cfg.IsHidden(c.URL) {
				continue
			}
			add(c)
		}
	}
	s.mu.Lock()
	old := s.hosts
	s.hosts = list
	s.mu.Unlock()
	keep := map[*hosts.Host]bool{}
	for _, h := range list {
		keep[h] = true
	}
	for _, h := range old {
		if !keep[h] {
			h.Close()
		}
	}
}

// Close stops ssh tunnels.
func (s *Server) Close() {
	s.mcpRemove()
	if s.stopLoops != nil {
		s.stopLoops()
	}
	s.agentMu.Lock()
	for _, e := range s.collectors {
		e.cancel()
	}
	for _, e := range s.vmCollectors {
		e.cancel()
	}
	s.agentMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.hosts {
		h.Close()
	}
}

func (s *Server) host(name string) *hosts.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.hosts {
		if h.Name == name {
			return h
		}
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hosts", s.listHosts)
	mux.HandleFunc("POST /api/cloud-init", s.cloudInit)
	mux.HandleFunc("POST /api/cloud-init/editar", s.cloudInitEdit)
	mux.HandleFunc("GET /api/templates", s.listTemplates)
	mux.HandleFunc("POST /api/templates", s.saveTemplate)
	mux.HandleFunc("POST /api/templates/check", s.checkTemplate)
	mux.HandleFunc("POST /api/convert", s.convertRun)
	mux.HandleFunc("GET /api/templates/{id}", s.getTemplate)
	mux.HandleFunc("PUT /api/templates/{id}", s.saveTemplate)
	mux.HandleFunc("DELETE /api/templates/{id}", s.deleteTemplate)
	mux.HandleFunc("POST /api/hosts", s.addHost)
	mux.HandleFunc("GET /api/hosts/{host}", s.withHost(s.getHost))
	mux.HandleFunc("DELETE /api/hosts/{host}", s.removeHost)
	mux.HandleFunc("PUT /api/hosts/{host}", s.editHost)
	mux.HandleFunc("POST /api/hosts/{host}/hide", s.withHost(s.hideHost))
	mux.HandleFunc("GET /api/hidden", s.listHidden)
	mux.HandleFunc("DELETE /api/hidden", s.unhide)
	mux.HandleFunc("GET /api/hosts/{host}/labs", s.withHost(s.listLabs))
	mux.HandleFunc("GET /api/hosts/{host}/stats", s.withHost(s.stats))
	mux.HandleFunc("GET /api/hosts/{host}/ports", s.withHost(s.ports))
	mux.HandleFunc("GET /api/hosts/{host}/web", s.withHost(s.web))
	mux.HandleFunc("GET /api/hosts/{host}/vmnet", s.withHost(s.vmNet))
	mux.HandleFunc("GET /api/hosts/{host}/imagens-vm", s.withHost(s.listVMImages))
	mux.HandleFunc("PUT /api/hosts/{host}/imagens-vm", s.withHost(s.setVMImage))
	mux.HandleFunc("GET /api/hosts/{host}/imagens", s.withHost(s.listImages))
	mux.HandleFunc("GET /api/hosts/{host}/pastas", s.withHost(s.hostFolders))
	mux.HandleFunc("POST /api/hosts/{host}/pastas", s.withHost(s.hostMkdir))
	mux.HandleFunc("PUT /api/hosts/{host}/imagens/pastas", s.withHost(s.setImageDirs))
	mux.HandleFunc("GET /api/hosts/{host}/alerts", s.withHost(s.alerts))
	mux.HandleFunc("GET /api/hosts/{host}/shared", s.withHost(s.listShared))
	mux.HandleFunc("POST /api/hosts/{host}/shared/{id}/provision", s.withHost(s.provisionShared))
	mux.HandleFunc("POST /api/hosts/{host}/shared/{id}/client", s.withHost(s.sharedClient))
	mux.HandleFunc("POST /api/hosts/{host}/web/open", s.withHost(s.webOpen))
	mux.HandleFunc("GET /api/hosts/{host}/events", s.withHost(s.events))
	mux.HandleFunc("POST /api/hosts/{host}/open", s.withHost(s.openFolder))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/{op}", s.withHost(s.labOp))
	mux.HandleFunc("DELETE /api/hosts/{host}/labs/{lab}", s.withHost(s.forgetLab))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/file", s.withHost(s.getFile))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/file", s.withHost(s.putFile))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/file/restart", s.withHost(s.restartForFile))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/term", s.withHost(s.vmTerm))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/vms/{vm}/snapshots", s.withHost(s.vmSnapshots))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/snapshots", s.withHost(s.vmSnapshotCreate))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/snapshots/{snap}/restore", s.withHost(s.vmSnapshotRestore))
	mux.HandleFunc("DELETE /api/hosts/{host}/labs/{lab}/vms/{vm}/snapshots/{snap}", s.withHost(s.vmSnapshotDelete))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/resize", s.withHost(s.vmResize))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/captura/ao-vivo", s.withHost(s.labCaptureLive))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/captura/arquivo", s.withHost(s.labCaptureFile))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/captura/salvar", s.withHost(s.labCaptureSave))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/captura/wireshark", s.withHost(s.labCaptureWireshark))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/clone", s.withHost(s.vmClone))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/idle-pc", s.withHost(s.vmIdlePC))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/portas", s.withHost(s.vmPorts))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/autostart", s.withHost(s.vmAutostart))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/nix-aplicar", s.withHost(s.vmNixApply))
	mux.HandleFunc("POST /api/hosts/{host}/discos/importar", s.withHost(s.importDisk))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/vms/{vm}/{op}", s.withHost(s.vmOp))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/vms/{vm}/console", s.withHost(s.vmConsole))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/vms/{vm}/recusadas", s.withHost(s.vmRejected))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/vms/{vm}/ssh", s.withHost(s.vmSSH))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/vms/{vm}/screen", s.withHost(s.vmScreen))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/topology", s.withHost(s.topology))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/cabos", s.withHost(s.listCables))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/cabos", s.withHost(s.saveCables))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/posicoes", s.withHost(s.savePositions))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/icones", s.withHost(s.saveIcons))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/redes", s.withHost(s.listRedes))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/redes/{rede}", s.withHost(s.saveRede))
	mux.HandleFunc("DELETE /api/hosts/{host}/labs/{lab}/redes/{rede}", s.withHost(s.deleteRede))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/estados", s.withHost(s.listLabStates))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/estados", s.withHost(s.saveLabState))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/estados/{name}", s.withHost(s.labStateAction))
	mux.HandleFunc("DELETE /api/hosts/{host}/labs/{lab}/estados/{name}", s.withHost(s.labStateAction))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/zerar", s.withHost(s.zeroLab))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/vm-metricas", s.withHost(s.labVMMetrics))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/drift", s.withHost(s.drift))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/aplicar", s.withHost(s.applyLab))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/aplicar-sozinho", s.withHost(s.setAutoApply))
	mux.HandleFunc("POST /api/hosts/{host}/agent/{op}", s.withHost(s.agentOp))
	mux.HandleFunc("GET /api/hosts/{host}/metrics", s.withHost(s.metrics))
	mux.HandleFunc("GET /api/hosts/{host}/obs", s.withHost(s.obsInfo))
	mux.HandleFunc("PUT /api/hosts/{host}/obs", s.withHost(s.obsSet))
	mux.HandleFunc("GET /api/hosts/{host}/obs/prom", s.withHost(s.promQuery))
	mux.HandleFunc("GET /api/hosts/{host}/obs/loki", s.withHost(s.lokiLogs))
	mux.HandleFunc("GET /api/hosts/{host}/obs/grafana/dashboards", s.withHost(s.grafanaDashboards))
	mux.HandleFunc("POST /api/hosts/{host}/obs/open", s.withHost(s.obsOpen))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/watch", s.withHost(s.watchLab))
	mux.HandleFunc("GET /api/hosts/{host}/engines", s.withHost(s.listEngines))
	mux.HandleFunc("GET /api/hosts/{host}/system", s.withHost(s.systemInfo))
	mux.HandleFunc("GET /api/hosts/{host}/overview", s.withHost(s.overview))
	mux.HandleFunc("GET /api/hosts/{host}/containers", s.withHost(s.listContainers))
	mux.HandleFunc("POST /api/hosts/{host}/labs", s.withHost(s.createLab))
	mux.HandleFunc("POST /api/hosts/{host}/labs/git", s.withHost(s.labFromGit))
	mux.HandleFunc("POST /api/hosts/{host}/labs/import", s.withHost(s.importLab))
	mux.HandleFunc("POST /api/hosts/{host}/labs/importar-unl", s.withHost(s.importUNL))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/export", s.withHost(s.exportLab))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/copy", s.withHost(s.copyLab))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/backups", s.withHost(s.listBackups))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/segredos", s.withHost(s.labSecretsView))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/atualizacoes", s.withHost(s.labUpdates))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/atualizacoes", s.withHost(s.setImageTag))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/segredos", s.withHost(s.labSecretsMove))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/backups", s.withHost(s.createBackup))
	mux.HandleFunc("PUT /api/hosts/{host}/labs/{lab}/backups/agenda", s.withHost(s.setBackupSchedule))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/backups/pasta", s.withHost(s.openBackups))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/backups/{name}/restore", s.withHost(s.restoreBackup))
	mux.HandleFunc("DELETE /api/hosts/{host}/labs/{lab}/backups/{name}", s.withHost(s.deleteBackup))
	mux.HandleFunc("GET /api/hosts/{host}/labs/{lab}/git", s.withHost(s.labGit))
	mux.HandleFunc("POST /api/hosts/{host}/labs/{lab}/git/pull", s.withHost(s.labGitPull))
	mux.HandleFunc("POST /api/hosts/{host}/containers", s.withHost(s.createContainer))
	mux.HandleFunc("POST /api/hosts/{host}/res/{kind}", s.withHost(s.createResource))
	mux.HandleFunc("POST /api/hosts/{host}/build", s.withHost(s.buildImage))
	mux.HandleFunc("POST /api/hosts/{host}/images/{id}/tag", s.withHost(s.tagImage))
	mux.HandleFunc("GET /api/hosts/{host}/res/{kind}", s.withHost(s.listResources))
	mux.HandleFunc("DELETE /api/hosts/{host}/res/{kind}/{id}", s.withHost(s.removeResource))
	mux.HandleFunc("POST /api/hosts/{host}/prune/{kind}", s.withHost(s.pruneResources))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/{action}", s.withHost(s.containerAction))
	mux.HandleFunc("GET /api/hosts/{host}/containers/{id}", s.withHost(s.getContainer))
	mux.HandleFunc("PUT /api/hosts/{host}/containers/{id}", s.withHost(s.recreate))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/net/{op}", s.withHost(s.netOp))
	mux.HandleFunc("DELETE /api/hosts/{host}/containers/{id}", s.withHost(s.removeContainer))
	mux.HandleFunc("GET /api/hosts/{host}/containers/{id}/logs", s.withHost(s.logs))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/logs/salvar", s.withHost(s.saveLogs))
	mux.HandleFunc("GET /api/hosts/{host}/containers/{id}/arquivos", s.withHost(s.ctrFiles))
	mux.HandleFunc("DELETE /api/hosts/{host}/containers/{id}/arquivos", s.withHost(s.ctrDelete))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/arquivos/baixar", s.withHost(s.ctrDownload))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/arquivos/enviar", s.withHost(s.ctrUpload))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/recursos", s.withHost(s.ctrResources))
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/commit", s.withHost(s.ctrCommit))
	mux.HandleFunc("GET /api/hosts/{host}/eventos", s.withHost(s.ctrEvents))
	mux.HandleFunc("GET /api/hosts/{host}/images/{id}/historico", s.withHost(s.imageHistory))
	mux.HandleFunc("POST /api/hosts/{host}/images/{id}/push", s.withHost(s.imagePush))
	mux.HandleFunc("POST /api/hosts/{host}/images/{id}/salvar", s.withHost(s.imageSave))
	mux.HandleFunc("POST /api/hosts/{host}/images/carregar", s.withHost(s.imageLoad))
	mux.HandleFunc("POST /api/hosts/{host}/registros/{server}/login", s.withHost(s.registryHostLogin))
	mux.HandleFunc("GET /api/mcp", s.getMCP)
	mux.HandleFunc("PUT /api/mcp", s.setMCP)
	mux.HandleFunc("GET /api/clusters", s.listClusters)
	mux.HandleFunc("POST /api/clusters", s.addCluster)
	mux.HandleFunc("POST /api/kubeconfig", s.kubeconfigContexts)
	mux.HandleFunc("GET /api/kube/tipos", s.clusterKinds)
	mux.HandleFunc("PATCH /api/clusters/{id}", s.editCluster)
	mux.HandleFunc("DELETE /api/clusters/{id}", s.removeCluster)
	mux.HandleFunc("GET /api/clusters/{id}/visao", s.withCluster(s.clusterOverview))
	mux.HandleFunc("GET /api/clusters/{id}/helm", s.withCluster(s.clusterHelm))
	mux.HandleFunc("GET /api/clusters/{id}/helm/{ns}/{name}", s.withCluster(s.clusterHelmRelease))
	mux.HandleFunc("POST /api/clusters/{id}/helm/{ns}/{name}/voltar", s.withCluster(s.clusterHelmRollback))
	mux.HandleFunc("GET /api/clusters/{id}/helm/{ns}/{name}/drift", s.withCluster(s.clusterHelmDrift))
	mux.HandleFunc("GET /api/clusters/{id}/crds", s.withCluster(s.clusterCRDs))
	mux.HandleFunc("GET /api/clusters/{id}/busca", s.withCluster(s.clusterSearch))
	mux.HandleFunc("GET /api/clusters/{id}/permissoes", s.withCluster(s.clusterPerms))
	mux.HandleFunc("POST /api/clusters/{id}/aplicar", s.withCluster(s.clusterApply))
	mux.HandleFunc("POST /api/clusters/{id}/abrir", s.withCluster(s.clusterOpen))
	mux.HandleFunc("POST /api/clusters/{id}/encaminhar", s.withCluster(s.clusterForward))
	mux.HandleFunc("GET /api/encaminhamentos", s.listForwards)
	mux.HandleFunc("DELETE /api/encaminhamentos/{fid}", s.closeForward)
	mux.HandleFunc("GET /api/clusters/{id}/r/{kind}", s.withCluster(s.clusterList))
	mux.HandleFunc("GET /api/clusters/{id}/r/{kind}/{ns}/{name}", s.withCluster(s.clusterYAML))
	mux.HandleFunc("PUT /api/clusters/{id}/r/{kind}/{ns}/{name}", s.withCluster(s.clusterYAML))
	mux.HandleFunc("DELETE /api/clusters/{id}/r/{kind}/{ns}/{name}", s.withCluster(s.clusterDelete))
	mux.HandleFunc("GET /api/clusters/{id}/r/secrets/{ns}/{name}/dados", s.withCluster(s.clusterSecret))
	mux.HandleFunc("GET /api/clusters/{id}/r/{kind}/{ns}/{name}/detalhe", s.withCluster(s.clusterDetail))
	mux.HandleFunc("POST /api/clusters/{id}/r/{kind}/{ns}/{name}/{acao}", s.withCluster(s.clusterAction))
	mux.HandleFunc("GET /api/clusters/{id}/pods/{ns}/{pod}/logs", s.withCluster(s.clusterLogs))
	mux.HandleFunc("GET /api/clusters/{id}/varios/{ns}/{kind}/{name}/logs", s.withCluster(s.clusterMultiLogs))
	mux.HandleFunc("GET /api/clusters/{id}/varios/{ns}/{kind}/{name}/pods", s.withCluster(s.clusterMultiPods))
	mux.HandleFunc("POST /api/clusters/{id}/pods/{ns}/{pod}/logs/salvar", s.withCluster(s.clusterSaveLogs))
	mux.HandleFunc("GET /api/clusters/{id}/podlogs", s.withCluster(s.clusterSavedPods))
	mux.HandleFunc("POST /api/clusters/{id}/validar", s.withCluster(s.clusterValidate))
	mux.HandleFunc("POST /api/clusters/{id}/desconectar", s.clusterDisconnect)
	mux.HandleFunc("GET /api/clusters/{id}/podlogs/{ns}/{pod}/log", s.withCluster(s.clusterSavedLog))
	mux.HandleFunc("POST /api/clusters/{id}/shell/{ns}/{pod}", s.withCluster(s.clusterShell))
	mux.HandleFunc("GET /api/clusters/{id}/debug/{ns}/{pod}", s.withCluster(s.clusterDebugList))
	mux.HandleFunc("POST /api/clusters/{id}/debug/{ns}/{pod}", s.withCluster(s.clusterDebug))
	mux.HandleFunc("GET /api/grupos", s.getGroups)
	mux.HandleFunc("POST /api/grupos", s.createFolder)
	mux.HandleFunc("PATCH /api/grupos/{id}", s.editFolder)
	mux.HandleFunc("DELETE /api/grupos/{id}", s.deleteFolder)
	mux.HandleFunc("PUT /api/grupos-itens", s.moveItem)
	mux.HandleFunc("GET /api/senhas-ssh", s.sshPasswords)
	mux.HandleFunc("DELETE /api/senhas-ssh/{host}", s.forgetSSHPassword)
	mux.HandleFunc("GET /api/versao", s.latestVersion)
	mux.HandleFunc("GET /api/acesso", s.accessInfo)
	mux.HandleFunc("POST /api/versao/abrir", s.openRelease)
	mux.HandleFunc("POST /api/versao/atualizar", s.updateApp)
	mux.HandleFunc("GET /api/registros", s.listRegistries)
	mux.HandleFunc("POST /api/registros", s.addRegistry)
	mux.HandleFunc("DELETE /api/registros/{server}", s.removeRegistry)
	mux.HandleFunc("POST /api/hosts/{host}/containers/{id}/term", s.withHost(s.newTerm))
	mux.HandleFunc("GET /api/term/{id}/stream", s.termStream)
	mux.HandleFunc("POST /api/term/{id}/input", s.termInput)
	mux.HandleFunc("POST /api/term/{id}/resize", s.termResize)
	mux.HandleFunc("DELETE /api/term/{id}", s.termClose)
	mux.HandleFunc("POST /api/askpass", s.askpass)
	mux.HandleFunc("GET /api/prompts/stream", s.promptStream)
	mux.HandleFunc("POST /api/prompts/{id}", s.answerPrompt)
	mux.HandleFunc("POST /api/hosts/{host}/retry", s.withHost(s.retryHost))
	mux.HandleFunc("POST /api/hosts/{host}/disconnect", s.disconnectHost)
	mux.HandleFunc("POST /api/hosts/{host}/fix/{id}", s.withHost(s.fixHost))
	mux.HandleFunc("GET /api/local", s.localStatus)
	mux.HandleFunc("GET /api/local/ferramentas", s.localToolsHandler)
	mux.HandleFunc("GET /api/ssh-config", s.sshConfigHosts)
	mux.HandleFunc("POST /api/local/ferramentas/{id}", s.installTool)
	mux.HandleFunc("GET /api/hosts/{host}/ferramentas", s.withHost(s.hostTools))
	mux.HandleFunc("GET /api/local/system", s.localSystem)
	mux.HandleFunc("POST /api/local/{action}", s.localAction)
	mux.HandleFunc("GET /api/jobs/{id}/stream", s.jobStream)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("POST /api/escolher", s.pick)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.cancelJob)
	mux.HandleFunc("GET /api/diagnostico", s.getDiagnostic)
	mux.HandleFunc("POST /api/diagnostico/salvar", s.saveDiagnostic)
	mux.HandleFunc("POST /api/log/abrir", s.openLogFolder)
	mux.HandleFunc("GET /config.js", s.configJS)
	mux.HandleFunc("GET /api/idioma", s.language)
	mux.HandleFunc("PUT /api/idioma", s.language)
	// The interface changes with each build: never let the webview keep an
	// old copy of the HTML, CSS or JS.
	files := http.FileServerFS(s.static)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	}))
	return recoverPanics(s.guard(mux))
}

// Version is shown in the UI.
var Version = "dev"

func (s *Server) configJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	b, _ := json.Marshal(map[string]string{"api": "", "token": "", "version": Version, "mode": "web", "lang": s.cfg.Lang()})
	fmt.Fprintf(w, "window.GANTRY=%s;\n", b)
}

// AllowOrigin lets a page served from another origin (the desktop window,
// wails://wails) call the API with the token in a header or query string.
func (s *Server) AllowOrigin(o string) {
	s.origins = append(s.origins, o)
}

func (s *Server) allowed(origin string) bool {
	for _, o := range s.origins {
		if o == origin {
			return true
		}
	}
	return false
}

func (s *Server) validToken(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.loopback && !loopbackHost(r.Host) {
			http.Error(w, "host não permitido", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		cross := origin != "" && s.allowed(origin)
		if cross {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Gantry-Lang")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin != "" && origin != selfOrigin(r) && !cross {
				http.Error(w, "origem não permitida", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// Every API call carries the token: in the Authorization header,
			// or in the query string for EventSource, which cannot set
			// headers. No cookie: a cookie for 127.0.0.1 would also go to
			// every other app on 127.0.0.1 (the containers' web pages, the
			// port forwards), whatever the port.
			t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if t == "" {
				t = r.URL.Query().Get("t")
			}
			// A page of another site never uses the API, even with the token.
			foreign := origin != "" && origin != selfOrigin(r) && !cross
			// The assistants' requests keep the app's language (it is one
			// for the whole app: the window sets it).
			if !foreign && !s.validToken(t) && s.validMCP(t) {
				if !s.mcpAllowed(r) {
					writeErr(w, http.StatusForbidden, errors.New("o acesso dos assistentes (MCP) não permite isso"))
					return
				}
				next.ServeHTTP(w, withMCP(r))
				return
			}
			s.useLang(r)
			if foreign || !s.validToken(t) {
				writeErr(w, http.StatusUnauthorized, errors.New("sessão inválida; abra o link mostrado no terminal"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// The page itself (HTML, CSS, JS) has no secrets and loads without
		// the token. The link printed at the start (?c=, one use) or the
		// token (?t=) hand the token to this browser, kept in the
		// localStorage of this origin only.
		if r.URL.Path == "/" {
			if c := r.URL.Query().Get("c"); c != "" {
				if !s.useLoginCode(c) {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, loginPage("Este link já foi usado ou expirou. Use o link novo mostrado no terminal."))
					return
				}
				handOver(w, s.token)
				return
			}
			if t := r.URL.Query().Get("t"); t != "" && s.validToken(t) {
				handOver(w, s.token)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handOver gives the token to the page and opens the app without it in the
// address bar.
func handOver(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	b, _ := json.Marshal(token)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Gantry</title><script>try{localStorage.setItem("gantry.token",%s)}catch(e){}location.replace("/")</script>`, b)
}

func loginPage(msg string) string {
	return `<!doctype html><meta charset="utf-8"><title>Gantry</title><body style="font-family:-apple-system,system-ui;background:#f6f7f9;color:#1a1d23;padding:40px"><h1>Gantry</h1><p>` + html.EscapeString(msg) + `</p></body>`
}

// LoginLink is a link that opens the app in a browser once (it expires in
// 10 minutes). Using it prints a new one (OnLoginUsed), for another browser.
func (s *Server) LoginLink() string {
	c := randomID(18)
	s.loginMu.Lock()
	if s.loginCodes == nil {
		s.loginCodes = map[string]time.Time{}
	}
	for k, t := range s.loginCodes {
		if time.Since(t) > loginCodeTTL {
			delete(s.loginCodes, k)
		}
	}
	s.loginCodes[c] = time.Now()
	s.loginMu.Unlock()
	base := s.access.Link
	if base == "" {
		base = "http://" + s.addr
	}
	return base + "/?c=" + c
}

// Access is how the browser reaches the app: on the network or only on
// this computer, with HTTPS or not.
type Access struct {
	Exposed     bool   `json:"exposed"`     // listening outside localhost
	TLS         bool   `json:"tls"`         // HTTPS
	Fingerprint string `json:"fingerprint"` // SHA-256 of the certificate
	Link        string `json:"link"`        // scheme://host:port of the links
}

// SetAccess tells the server how it is reached (before it serves).
func (s *Server) SetAccess(a Access) { s.access = a }

// accessInfo is the access for the interface: a page served on the network
// without HTTPS shows a warning.
func (s *Server) accessInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.access)
}

// selfOrigin is the origin of the app's own pages for this request.
func selfOrigin(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

const loginCodeTTL = 10 * time.Minute

// OnLoginUsed runs after a login link was used (the terminal shows a new one).
var OnLoginUsed func()

func (s *Server) useLoginCode(c string) bool {
	s.loginMu.Lock()
	t, ok := s.loginCodes[c]
	delete(s.loginCodes, c)
	s.loginMu.Unlock()
	if !ok || time.Since(t) > loginCodeTTL {
		return false
	}
	if OnLoginUsed != nil {
		go func() { defer applog.Recover("novo link"); OnLoginUsed() }()
	}
	return true
}

func loopbackHost(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if i18n.English() {
		// The texts made here (states, hints, problems) in English.
		if b, err := json.Marshal(v); err == nil {
			var x any
			if json.Unmarshal(b, &x) == nil {
				v = i18n.JSON(x)
			}
		}
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": i18n.T(err.Error())})
}

func engineStatus(err error) int {
	var ae *engine.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return http.StatusBadGateway
}

type hostHandler func(w http.ResponseWriter, r *http.Request, h *hosts.Host)

func (s *Server) withHost(fn hostHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := s.host(r.PathValue("host"))
		if h == nil {
			writeErr(w, http.StatusNotFound, errors.New("host não encontrado"))
			return
		}
		h.Want() // opening anything of a host connects it
		fn(w, r, h)
	}
}

type hostView struct {
	Name    string       `json:"name"`
	URL     string       `json:"url"`
	Source  string       `json:"source"`
	Kind    string       `json:"kind"`
	Files   bool         `json:"files"`
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Runtime string       `json:"runtime,omitempty"`
	Version string       `json:"version,omitempty"`
	Compose []string     `json:"compose"`
	Info    *engine.Info `json:"info,omitempty"`

	Machine    string     `json:"machine"`
	Idle       bool       `json:"idle,omitempty"` // remote host not opened yet
	Connecting bool       `json:"connecting,omitempty"`
	Socket     string     `json:"socket,omitempty"`
	ComposeCfg []string   `json:"composeConfig,omitempty"`
	Fix        *hosts.Fix `json:"fix,omitempty"`
	Agent      *agentView `json:"agent,omitempty"`
}

const connectingMsg = "conectando via ssh… se o servidor ou a chave pedirem senha, responda na janela de autenticação"

func (s *Server) describe(ctx context.Context, h *hosts.Host) hostView {
	v := hostView{Name: h.Name, URL: h.URL, Source: h.Source, Kind: string(h.Kind), Files: h.Files(), Machine: h.Machine()}
	if c, ok := s.cfg.Find(h.Name); ok {
		v.Socket, v.ComposeCfg = c.Socket, c.Compose
	}
	if !h.Wanted() {
		v.Idle = true
		v.Compose = h.Compose()
		return v
	}
	if h.Connecting() {
		// Don't hold the whole host list while someone types a password.
		v.Error = connectingMsg
		v.Connecting = true
		v.Compose = h.Compose()
		return v
	}
	wait := 20 * time.Second
	if h.Remote() {
		// A first connection may stop at a password prompt; answer early
		// and let the UI poll until it is done.
		wait = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ver, err := h.Engine.Version(ctx)
	if err != nil {
		v.Error = err.Error()
		if h.Connecting() {
			v.Error, v.Connecting = connectingMsg, true
		} else if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF") {
			v.Error = "a conexão com o engine foi fechada pelo servidor. O Docker/Podman pode ter parado, ou o socket mudou. Use Atualizar para reconectar."
			if h.Remote() {
				h.Close()
			}
		}
		v.Fix = h.Fix()
		v.Compose = h.Compose()
		return v
	}
	h.SetRuntime(ver.Runtime())
	h.SetComposeHint(ver.ComposeHint())
	v.Runtime, v.Version = ver.Runtime(), ver.Version
	info, err := h.Engine.Info(ctx)
	if err != nil {
		v.Error = err.Error()
	} else {
		v.OK, v.Info = true, &info
		v.Agent = s.agentState(ctx, h, ctx.Value(freshAgent{}) != nil)
	}
	v.Compose = h.Compose()
	return v
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		s.reload()
	}
	s.mu.RLock()
	list := append([]*hosts.Host(nil), s.hosts...)
	s.mu.RUnlock()
	out := make([]hostView, len(list))
	var wg sync.WaitGroup
	for i, h := range list {
		wg.Add(1)
		go func() {
			defer applog.Recover("server/server.go:591")
			defer wg.Done()
			out[i] = s.describe(r.Context(), h)
		}()
	}
	wg.Wait()
	writeJSON(w, out)
}

func (s *Server) fixHost(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := h.RunFix(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, errors.New(strings.TrimSpace(err.Error()+"\n"+out)))
		return
	}
	writeJSON(w, s.describe(r.Context(), h))
}

func (s *Server) retryHost(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	h.Retry()
	writeJSON(w, s.describe(context.WithValue(r.Context(), freshAgent{}, true), h))
}

// disconnectHost closes every connection to the host's machine and forgets
// the passwords remembered for it: opening it again asks them again.
func (s *Server) disconnectHost(w http.ResponseWriter, r *http.Request) {
	h := s.host(r.PathValue("host"))
	if h == nil {
		writeErr(w, http.StatusNotFound, errors.New("host não encontrado"))
		return
	}
	for _, x := range s.machineHosts(h) {
		x.Disconnect()
		s.prompts.forget(x.Name)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// machineHosts lists the Gantry hosts on the same machine as h, h first.
func (s *Server) machineHosts(h *hosts.Host) []*hosts.Host {
	out := []*hosts.Host{h}
	m := h.Machine()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, x := range s.hosts {
		if x != h && x.Machine() == m {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	writeJSON(w, s.describe(context.WithValue(r.Context(), freshAgent{}, true), h))
}

func (s *Server) addHost(w http.ResponseWriter, r *http.Request) {
	var c config.Host
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	c.URL = strings.TrimSpace(c.URL)
	if c.Name == "" || c.URL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("nome e URL são obrigatórios"))
		return
	}
	if s.host(c.Name) != nil {
		writeErr(w, http.StatusConflict, errors.New("já existe um host com esse nome"))
		return
	}
	if _, err := hosts.New(c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.cfg.AddHost(c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.reload()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) editHost(w http.ResponseWriter, r *http.Request) {
	var c config.Host
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c.Name, c.URL, c.Socket = strings.TrimSpace(c.Name), strings.TrimSpace(c.URL), strings.TrimSpace(c.Socket)
	if c.Name == "" || c.URL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("nome e URL são obrigatórios"))
		return
	}
	old := r.PathValue("host")
	if c.Name != old && s.host(c.Name) != nil {
		writeErr(w, http.StatusConflict, errors.New("já existe um host com esse nome"))
		return
	}
	if _, err := hosts.New(c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.cfg.UpdateHost(old, c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if c.Name != old {
		s.store.Rename(old, c.Name)
		s.cfg.RenameObs(old, c.Name)
	}
	s.reload()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) hideHost(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if h.Source == "config" {
		writeErr(w, http.StatusBadRequest, errors.New("este host foi adicionado por você; use Remover"))
		return
	}
	if err := s.cfg.Hide(h.URL); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.reload()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) listHidden(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.cfg.HiddenList())
}

func (s *Server) unhide(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Unhide(r.URL.Query().Get("url")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.reload()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) removeHost(w http.ResponseWriter, r *http.Request) {
	machine := ""
	if h := s.host(r.PathValue("host")); h != nil {
		machine = h.Machine()
	}
	if err := s.cfg.RemoveHost(r.PathValue("host")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.reload()
	// The machine leaves its folder when no other host shows it.
	if machine != "" && s.machineHost(machine) == nil {
		s.forgetItem(machine)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) findLab(ctx context.Context, h *hosts.Host, id string) (labs.Lab, error) {
	ls, err := s.labsOf(ctx, h)
	if err != nil {
		return labs.Lab{}, err
	}
	for _, l := range ls {
		if l.ID == id {
			return l, nil
		}
	}
	return labs.Lab{}, errors.New("projeto não encontrado")
}

func (s *Server) listLabs(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ls, err := s.labsOf(r.Context(), h)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, ls)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cs, err := h.Engine.Containers(ctx)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	type item struct {
		eng *engine.Client
		id  string
	}
	var todo []item
	for _, c := range cs {
		if c.State == "running" {
			todo = append(todo, item{h.Engine, c.ID})
		}
	}
	// Containers of mixed labs that live in the machine's other engines.
	if mixed := s.store.AllMixed(); len(mixed) > 0 {
		for _, e := range s.machineEngines(ctx, h) {
			if e.Engine == h.Engine || !mixed[e.Runtime] {
				continue
			}
			ecs, err := e.Engine.Containers(ctx)
			if err != nil {
				continue
			}
			for _, c := range ecs {
				if c.State == "running" && s.store.MixedRuntimes(c.Labels[labs.LabelProject], c.Labels[labs.LabelWorkingDir]) != nil {
					todo = append(todo, item{e.Engine, c.ID})
				}
			}
		}
	}
	out := map[string]engine.Stats{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, c := range todo {
		wg.Add(1)
		go func() {
			defer applog.Recover("server/server.go:813")
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := c.eng.Stats(ctx, c.id)
			if err != nil {
				return
			}
			mu.Lock()
			out[c.id] = st
			mu.Unlock()
		}()
	}
	wg.Wait()
	// Whoever looks at the live numbers will want the history next: start
	// keeping it (the agent, when there is one, already does).
	if av := s.agentState(r.Context(), h, false); av == nil || av.State != "running" {
		s.collector(h)
	}
	// VMs of the machine, by "vm:<projeto>/<vm>": the newest sample.
	if vc := s.vmCollector(h); vc != nil {
		for id, p := range vc.Latest() {
			out[id] = engine.Stats{CPU: float64(p.CPU), Mem: p.Mem, Disk: p.Disk}
		}
	}
	writeJSON(w, out)
}

func startSSE(w http.ResponseWriter) (func(event, data string), bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	var mu sync.Mutex
	return func(event, data string) {
		mu.Lock()
		defer mu.Unlock()
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
			// Events carry Gantry's words (the end of a log, a failure,
			// progress); the plain data is what the containers write.
			if i18n.English() {
				data = translateEvent(data)
			}
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		fl.Flush()
	}, true
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	send, ok := startSSE(w)
	if !ok {
		return
	}
	ctx := r.Context()
	go func() {
		defer applog.Recover("server/server.go:870")
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				send("ping", "{}")
			}
		}
	}()
	err := h.Engine.Events(ctx, func(e engine.Event) {
		b, _ := json.Marshal(map[string]string{
			"action":  e.Action,
			"id":      e.Actor.ID,
			"name":    e.Actor.Attributes["name"],
			"project": e.Actor.Attributes[labs.LabelProject],
		})
		send("", string(b))
	})
	if err != nil && ctx.Err() == nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		send("failure", string(b))
	}
}

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := eng.Action(ctx, r.PathValue("id"), r.PathValue("action")); err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// b64Writer sends every write as one base64 SSE message.
type b64Writer struct{ send func(string, string) }

func (b b64Writer) Write(p []byte) (int, error) {
	b.send("", base64.StdEncoding.EncodeToString(p))
	return len(p), nil
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	rc, tty, err := eng.LogsWith(r.Context(), r.PathValue("id"), logOptions(r, r.URL.Query().Get("follow") != "0"))
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	defer rc.Close()
	send, ok := startSSE(w)
	if !ok {
		return
	}
	out := newLineFilter(b64Writer{send}, r.URL.Query().Get("grep"))
	if tty {
		_, err = io.Copy(out, rc)
	} else {
		err = engine.Demux(out, rc)
	}
	if r.Context().Err() == nil {
		msg := "fim do log"
		if err != nil {
			msg = err.Error()
		}
		b, _ := json.Marshal(map[string]string{"message": msg})
		send("end", string(b))
	}
}

// --- labs and compose ---

func (s *Server) composeBase(h *hosts.Host, l labs.Lab) []string {
	argv := append([]string{}, h.Compose()...)
	argv = append(argv, "-p", l.ID)
	for _, f := range l.Files {
		argv = append(argv, "-f", f)
	}
	return argv
}

var labOps = map[string][]string{
	"up":      {"up", "-d", "--remove-orphans"},
	"stop":    {"stop"},
	"restart": {"restart"},
	"down":    {"down"},
	"pull":    {"pull"},
	// Build the images of services with build: again and recreate every
	// container (compose up -d --build --force-recreate).
	"rebuild": {"up", "-d", "--build", "--force-recreate", "--remove-orphans"},
	"create":  {"up", "--no-start"},
}

func (s *Server) labOp(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	op, ok := labOps[r.PathValue("op")]
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("operação desconhecida"))
		return
	}
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não executa compose; conecte via ssh://"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if l.Loose {
		writeErr(w, http.StatusBadRequest, errors.New("este grupo não tem um arquivo compose"))
		return
	}
	// ?job=1 runs it in the background with live output (the app's way).
	if r.URL.Query().Get("job") != "" {
		if l.WorkingDir == "" && r.PathValue("op") != "stop" && r.PathValue("op") != "restart" && r.PathValue("op") != "down" {
			writeErr(w, http.StatusBadRequest, errors.New("não sei onde está o arquivo compose deste projeto"))
			return
		}
		id, err := s.labJob(h, l, r.PathValue("op"))
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, map[string]string{"id": id})
		return
	}
	// Stop, restart and remove talk to the engine directly: they work even
	// when the compose file was moved or deleted, and with any compose tool.
	switch name := r.PathValue("op"); name {
	case "stop", "restart", "down":
		out, err := s.labViaEngine(ctx, h, l, name)
		if err == nil && name == "down" {
			s.dropKubeLab(h, l.ID)
		}
		s.store.Touch(h.Name, l.ID)
		resp := map[string]any{"ok": err == nil, "output": out, "command": name + i18n.T(" (via API do engine)")}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, resp)
		return
	}
	if l.WorkingDir == "" {
		writeErr(w, http.StatusBadRequest, errors.New("não sei onde está o arquivo compose deste projeto"))
		return
	}
	for _, f := range l.Files {
		if !h.Exists(ctx, f) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("o arquivo %s não existe mais no host. Se o projeto mudou de pasta, use Abrir pasta com compose no novo caminho", f))
			return
		}
	}
	_ = op
	out, cmd, err := s.composeLab(ctx, h, l.ID, l.WorkingDir, l.Files, r.PathValue("op"))
	resp := map[string]any{"ok": err == nil, "output": out, "command": cmd}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// labViaEngine stops, restarts or removes a lab's containers through the
// engine API. "down" also removes the networks the project created.

// removeContainer deletes one container (stopping it first).
func (s *Server) removeContainer(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := eng.Remove(ctx, r.PathValue("id"), true); err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) openFolder(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Dir) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a pasta"))
		return
	}
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não executa compose; conecte via ssh://"))
		return
	}
	dir := strings.TrimRight(strings.TrimSpace(req.Dir), "/")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	if !h.Exists(ctx, dir) {
		writeErr(w, http.StatusBadRequest, errors.New("pasta não encontrada no host"))
		return
	}
	project := composex.ProjectName(dir)
	files := s.findComposeFiles(ctx, h, dir)
	var out, cmd string
	var err error
	if len(files) > 0 {
		if p, _, perr := s.loadProject(ctx, h, files); perr == nil && p.Name != "" {
			project = p.Name
		}
		out, cmd, err = s.composeLab(ctx, h, project, dir, files, "up")
	} else {
		argv := append(append([]string{}, h.Compose()...), "up", "-d")
		out, err = h.Run(ctx, dir, argv, nil)
		cmd = strings.Join(argv, " ")
	}
	resp := map[string]any{"ok": err == nil, "output": out, "command": cmd, "project": project}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

func (s *Server) forgetLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	s.store.Forget(h.Name, r.PathValue("lab"))
	writeJSON(w, map[string]bool{"ok": true})
}

var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

func (s *Server) labFile(ctx context.Context, h *hosts.Host, l labs.Lab) (string, error) {
	if len(l.Files) > 0 {
		return l.Files[0], nil
	}
	if l.WorkingDir == "" {
		return "", errors.New("projeto sem pasta conhecida")
	}
	for _, n := range composeNames {
		p := path.Join(l.WorkingDir, n)
		if h.Exists(ctx, p) {
			return p, nil
		}
	}
	return "", errors.New("nenhum arquivo compose encontrado na pasta do projeto")
}

// pickFile resolves the ?name= of the file endpoints: the main compose
// file by default, another compose file of the lab, or its .env.
func (s *Server) pickFile(ctx context.Context, h *hosts.Host, l labs.Lab, name string) (p string, optional bool, err error) {
	main, err := s.labFile(ctx, h, l)
	if err != nil {
		return "", false, err
	}
	switch {
	case name == "" || name == main:
		return main, false, nil
	case name == ".env":
		return path.Join(path.Dir(main), ".env"), true, nil
	}
	for _, f := range l.Files {
		if f == name {
			return f, false, nil
		}
	}
	// Other text files of the folder (prometheus.yml, Traefik routes…).
	p, err = s.relProjectFile(ctx, h, path.Dir(main), name)
	return p, false, err
}

// fileChoices are the files of a lab the editor can switch between.
func fileChoices(main string, l labs.Lab) []map[string]string {
	out := []map[string]string{{"name": "", "label": path.Base(main)}}
	for _, f := range l.Files {
		if f != main {
			out = append(out, map[string]string{"name": f, "label": path.Base(f)})
		}
	}
	return append(out, map[string]string{"name": ".env", "label": ".env"})
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos; conecte via ssh://"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	p, optional, err := s.pickFile(ctx, h, l, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var b []byte
	missing := optional && !h.Exists(ctx, p)
	if !missing {
		if b, err = h.ReadFile(ctx, p); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}
	main, _ := s.labFile(ctx, h, l)
	writeJSON(w, map[string]any{"path": p, "content": string(b), "hash": hashOf(b), "missing": missing, "files": s.extraChoices(ctx, h, main, l, fileChoices(main, l))})
}

func (s *Server) putFile(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos"))
		return
	}
	var req struct {
		Content string `json:"content"`
		Hash    string `json:"hash"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	p, optional, err := s.pickFile(ctx, h, l, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var cur []byte
	if !optional || h.Exists(ctx, p) {
		if cur, err = h.ReadFile(ctx, p); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}
	if req.Hash != "" && hashOf(cur) != req.Hash {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "o arquivo mudou no disco desde que você abriu", "content": string(cur), "hash": hashOf(cur),
		})
		return
	}
	data := []byte(req.Content)
	if err := h.WriteFile(ctx, p, data); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "hash": hashOf(data)})
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// parseConfigHashes reads "service hash" lines of compose config --hash,
// ignoring color codes and the banner podman compose prints (">>>> Executing
// external compose provider …").
func parseConfigHashes(out string) map[string]string {
	want := map[string]string{}
	for _, line := range strings.Split(ansiRe.ReplaceAllString(out, ""), "\n") {
		if strings.Contains(line, ">>>>") || strings.Contains(line, "<<<<") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 2 && composeServiceName.MatchString(f[0]) {
			want[f[0]] = f[1]
		}
	}
	return want
}

var composeServiceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
