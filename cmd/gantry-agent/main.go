// gantry-agent runs on a server as a user service. It keeps container
// metrics (containers and VMs) while the app is closed and reports file
// changes, over a Unix socket that the app reaches through SSH, and, once
// paired, directly over TCP with mutual TLS.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gantry/internal/agent"
	"gantry/internal/tap"
)

var version = "dev"

func main() {
	// gantry-agent pair [addr] < app.crt: trust the app for direct
	// connections and print the agent's certificate (run by the app via SSH).
	if len(os.Args) > 1 && os.Args[1] == "pair" {
		addr := ""
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		in, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err == nil {
			var crt []byte
			if crt, err = agent.Pair(agent.TLSDir(), in, addr); err == nil {
				os.Stdout.Write(crt)
				return
			}
		}
		fmt.Fprintln(os.Stderr, "pair:", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "unpair" {
		if err := os.RemoveAll(agent.TLSDir()); err != nil {
			fmt.Fprintln(os.Stderr, "unpair:", err)
			os.Exit(1)
		}
		return
	}
	// gantry-agent tap [-relay host] eth1,30010,mac,ip/mask …: the ports of
	// a container on the lab's cables (run in a sidecar by the app).
	if len(os.Args) > 1 && os.Args[1] == "tap" {
		fs := flag.NewFlagSet("tap", flag.ExitOnError)
		relay := fs.String("relay", "auto", "onde o relay do projeto escuta (auto: o gateway do container)")
		fs.Parse(os.Args[2:])
		var ports []tap.Port
		for _, a := range fs.Args() {
			p, err := tap.ParsePort(a)
			if err != nil {
				fmt.Fprintln(os.Stderr, "tap:", err)
				os.Exit(2)
			}
			ports = append(ports, p)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := tap.Run(ctx, *relay, ports, func(f string, a ...any) { fmt.Printf("tap: "+f+"\n", a...) }); err != nil {
			fmt.Fprintln(os.Stderr, "tap:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "protocol" {
		fmt.Println(agent.Protocol)
		return
	}
	sock := flag.String("socket", agent.DefaultSocket(), "socket Unix onde o agente escuta")
	state := flag.String("state", agent.DefaultState(), "arquivo do histórico de métricas")
	eng := flag.String("engine", "", "socket do Docker ou Podman (padrão: procura sozinho)")
	interval := flag.Duration("interval", 15*time.Second, "intervalo entre as coletas de métricas")
	keep := flag.Duration("keep", 24*time.Hour, "quanto histórico de métricas guardar")
	showVersion := flag.Bool("version", false, "mostrar a versão")
	flag.Parse()
	if *showVersion || flag.Arg(0) == "version" {
		fmt.Println(version)
		return
	}
	log.SetFlags(0)
	a := agent.New(agent.Options{
		Version: version, Socket: *sock, State: *state, Engine: *eng,
		Interval: *interval, Keep: *keep, Log: log.Printf,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
