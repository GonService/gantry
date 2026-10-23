// Gantry manages containers and labs on local and remote hosts.
package main

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/labs"
	"gantry/internal/server"
)

//go:embed all:web
var webFS embed.FS

var version = "dev"

// helpers is the listener on 127.0.0.1 for the programs of this computer
// when the main one is on the network or uses HTTPS.
var helpers net.Listener

func main() {
	if os.Getenv("GANTRY_ASKPASS_URL") != "" {
		runAskpass()
		return
	}
	dir := config.DefaultDir()
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		runMCP(dir, os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "importar-unl" {
		runImportUNL(os.Args[2:])
		return
	}
	cfgPath := flag.String("config", filepath.Join(dir, "config.json"), "arquivo de configuração")
	addr := flag.String("addr", "", "endereço de escuta (padrão 127.0.0.1:7777)")
	noBrowser := flag.Bool("no-browser", false, "não abrir o navegador")
	webMode := flag.Bool("web", false, "abrir no navegador em vez de uma janela (versão desktop)")
	showVersion := flag.Bool("version", false, "mostrar a versão")
	expose := flag.Bool("expor-na-rede", false, "permitir escutar fora do localhost (prefira um túnel SSH; se usar, ligue o -tls)")
	useTLS := flag.Bool("tls", false, "HTTPS com um certificado que o Gantry gera e guarda na pasta da configuração")
	certFile := flag.String("cert", "", "HTTPS com o seu certificado (arquivo PEM; com -key)")
	keyFile := flag.String("key", "", "chave do certificado de -cert (arquivo PEM)")
	flag.Parse()

	if *showVersion {
		fmt.Println("gantry", version)
		return
	}

	window := desktopBuild && !*webMode
	if desktopBuild {
		// Apps opened from Finder get a minimal PATH; docker, ssh and compose
		// usually live in Homebrew or the engine's own folders.
		fixPath()
	}

	cfgDir := filepath.Dir(*cfgPath)
	if lp, err := applog.Setup(filepath.Join(cfgDir, "logs"), !window); err != nil {
		log.Printf("sem arquivo de log: %v", err)
	} else {
		slog.Info("Gantry abrindo", "versão", version, "sistema", runtime.GOOS+"/"+runtime.GOARCH, "janela", window, "log", lp)
	}
	if p, err := config.Prepare(cfgDir, version); err != nil {
		log.Fatalf("configuração em %s: %v", cfgDir, err)
	} else if p.Migrated {
		slog.Info("configuração migrada", "de", p.Found, "para", p.Format, "cópia", p.Copy)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("configuração inválida em %s: %v", *cfgPath, err)
	}
	listen := *addr
	if listen == "" {
		listen = cfg.Listen
	}
	if listen == "" {
		listen = "127.0.0.1:7777"
	}
	if window && *addr == "" {
		// The window talks to the server directly, so any free port will do.
		listen = "127.0.0.1:0"
	}

	if (*certFile == "") != (*keyFile == "") {
		log.Fatal("-cert e -key andam juntos: informe os dois arquivos")
	}
	httpsOn := *useTLS || *certFile != ""
	if httpsOn && window {
		log.Fatal("-tls, -cert e -key valem para a versão de navegador: acrescente -web")
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil && *addr == "" && cfg.Listen == "" {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		log.Fatal(err)
	}
	host, _, _ := net.SplitHostPort(ln.Addr().String())
	ip := net.ParseIP(host)
	loopback := ip != nil && ip.IsLoopback()
	if !loopback {
		if !*expose {
			log.Fatalf("o endereço %s fica visível na rede, sem criptografia: quem pegar o link controla as suas máquinas. "+
				"Use um túnel SSH (ssh -L 7777:127.0.0.1:7777 servidor) ou, se for mesmo isso, acrescente -expor-na-rede", ln.Addr())
		}
		if !httpsOn {
			log.Printf("atenção: escutando fora do localhost (%s), sem criptografia: quem estiver na rede pode ler o link e a chave e controlar as suas máquinas. Acrescente -tls (ou -cert e -key) ou use um túnel SSH", ln.Addr())
		}
	}
	scheme, fingerprint := "http", ""
	if httpsOn {
		tc, fp, err := serverTLS(cfgDir, *certFile, *keyFile)
		if err != nil {
			log.Fatalf("HTTPS: %v", err)
		}
		ln = tls.NewListener(ln, tc)
		scheme, fingerprint = "https", fp
	}
	// The helpers of this computer (the SSH password prompts, gantry mcp)
	// talk to the server over 127.0.0.1, never over the network.
	local := ln.Addr().String()
	if !loopback || httpsOn {
		hl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		local = hl.Addr().String()
		defer hl.Close()
		helpers = hl
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	store := labs.OpenStore(filepath.Join(filepath.Dir(*cfgPath), "state.json"))
	server.Version = version
	srv := server.New(cfg, store, static, loopback)
	defer srv.Close()
	srv.SetAddr(local)
	srv.SetAccess(server.Access{Exposed: !loopback, TLS: httpsOn, Fingerprint: fingerprint, Link: scheme + "://" + linkHost(ln.Addr().String())})
	if window {
		for _, o := range desktopOrigins {
			srv.AllowOrigin(o)
		}
	}

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 1)
	go func() { defer applog.Recover("main.go:106"); serveErr <- hs.Serve(ln) }()
	if helpers != nil {
		go func() { defer applog.Recover("main.go:helpers"); _ = hs.Serve(helpers) }()
	}
	shutdown := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}

	if window {
		// The window keeps several streams open (events, logs, consoles,
		// captures, jobs) and the web view allows only 6 connections per
		// address: past that every other call waits in line. The streams
		// use other ports of the same server, each with its own 6.
		var streams []string
		for i := 0; i < 8; i++ {
			sl, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				break
			}
			streams = append(streams, "http://"+sl.Addr().String())
			go func() { defer applog.Recover("main.go:streams"); _ = hs.Serve(sl) }()
		}
		if err := runDesktop(static, "http://"+ln.Addr().String(), streams, srv.Token(), filepath.Join(filepath.Dir(*cfgPath), "janela.json"), cfg.Lang); err != nil {
			log.Print(err)
		}
		shutdown()
		return
	}

	// The links open the app once each (10 minutes): the token itself never
	// goes in a URL another process could read (the browser's command line).
	fmt.Printf("Gantry %s\n  abra: %s\n  config: %s\n", version, srv.LoginLink(), *cfgPath)
	if fingerprint != "" {
		fmt.Printf("  HTTPS: impressão digital SHA-256 do certificado (confira no navegador):\n    %s\n", fingerprint)
	}
	server.OnLoginUsed = func() { fmt.Printf("  link para outro navegador: %s\n", srv.LoginLink()) }
	if !*noBrowser {
		go func() { defer applog.Recover("main.go:123"); openBrowser(srv.LoginLink()) }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
		shutdown()
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}
}

func openBrowser(u string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}
