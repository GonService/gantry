//go:build darwin && cgo

package cvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
)

const hasRunner = true

// RunMachine runs the machine in this process until it stops: Apple's
// Virtualization framework through Code-Hex/vz (the same binding Lima
// uses). It also serves the control socket (state, stop, memory), joins
// the vsock ports to unix sockets on the Mac and runs the memory balloon:
// what the guest is not using goes back to the Mac.
func RunMachine(name string) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	dir := Dir(name)
	lf, err := os.OpenFile(filepath.Join(dir, fVMLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	lg := log.New(lf, "", log.LstdFlags)
	lg.Printf("--- %s: %d CPU, %d MiB, Gantry", name, s.CPUs, s.MemoryMiB)

	vm, cfgErr := vmConfig(s, dir, lg)
	if cfgErr != nil {
		lg.Printf("configuração: %v", cfgErr)
		return cfgErr
	}
	if err := vm.Start(); err != nil {
		lg.Printf("start: %v", err)
		return err
	}
	r := &runner{name: name, spec: s, vm: vm, log: lg, target: uint64(s.MemoryMiB) << 20}
	for _, b := range vm.MemoryBalloonDevices() {
		if d, ok := b.(*vz.VirtioTraditionalMemoryBalloonDevice); ok {
			r.balloon = d
		}
	}
	if len(vm.SocketDevices()) > 0 {
		r.sock = vm.SocketDevices()[0]
	}
	r.forward(portSSH, filepath.Join(dir, fSSH))
	if s.Docker {
		r.forward(portDocker, filepath.Join(dir, fDocker))
	}
	if s.Podman {
		r.forward(portPodman, filepath.Join(dir, fPodman))
	}
	go r.serve(filepath.Join(dir, fREST))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if r.balloon != nil {
		go r.memoryLoop(ctx)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		lg.Printf("sinal: desligando")
		r.stop(false)
	}()
	for st := range vm.StateChangedNotify() {
		lg.Printf("estado: %v", st)
		if st == vz.VirtualMachineStateStopped || st == vz.VirtualMachineStateError {
			break
		}
	}
	r.closeAll()
	return nil
}

// vmConfig makes the machine's configuration from the spec.
func vmConfig(s Spec, dir string, lg *log.Logger) (*vz.VirtualMachine, error) {
	p := func(f string) string { return filepath.Join(dir, f) }
	efi := p(fEFI)
	var vs *vz.EFIVariableStore
	var err error
	if _, e := os.Stat(efi); e == nil {
		vs, err = vz.NewEFIVariableStore(efi)
	} else {
		vs, err = vz.NewEFIVariableStore(efi, vz.WithCreatingEFIVariableStore())
	}
	if err != nil {
		return nil, fmt.Errorf("EFI: %w", err)
	}
	boot, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(vs))
	if err != nil {
		return nil, err
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, uint(s.CPUs), uint64(s.MemoryMiB)<<20)
	if err != nil {
		return nil, err
	}
	// Disks: the system with the host's cache; the data disk with full
	// synchronization (what the guest confirmed is on the Mac's SSD).
	var disks []vz.StorageDeviceConfiguration
	for _, d := range []struct {
		file     string
		ro       bool
		cache    vz.DiskImageCachingMode
		sync     vz.DiskImageSynchronizationMode
		optional bool
	}{
		{fSystem, false, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync, false},
		{fData, false, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFull, false},
		{fSeedISO, true, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeNone, true},
	} {
		if _, e := os.Stat(p(d.file)); e != nil && d.optional {
			continue
		}
		at, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(p(d.file), d.ro, d.cache, d.sync)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.file, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(at)
		if err != nil {
			return nil, err
		}
		disks = append(disks, blk)
	}
	cfg.SetStorageDevicesVirtualMachineConfiguration(disks)

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, err
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, err
	}
	if hw, err := net.ParseMAC(mac(s.Name)); err == nil {
		if m, err := vz.NewMACAddress(hw); err == nil {
			nic.SetMACAddress(m)
		}
	}
	cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})

	if ent, err := vz.NewVirtioEntropyDeviceConfiguration(); err == nil {
		cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{ent})
	}
	if att, err := vz.NewFileSerialPortAttachment(p(fConsole), true); err == nil {
		if con, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(att); err == nil {
			cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{con})
		}
	}

	// Folders: the home folder and the extra ones, at the same path; Rosetta.
	home, _ := os.UserHomeDir()
	var fs []vz.DirectorySharingDeviceConfiguration
	add := func(tag string, share vz.DirectoryShare) error {
		dev, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
		if err != nil {
			return err
		}
		dev.SetDirectoryShare(share)
		fs = append(fs, dev)
		return nil
	}
	single := func(path string, ro bool) (vz.DirectoryShare, error) {
		sd, err := vz.NewSharedDirectory(path, ro)
		if err != nil {
			return nil, err
		}
		return vz.NewSingleDirectoryShare(sd)
	}
	if sh, err := single(home, false); err == nil {
		if err := add("home", sh); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("pasta %s: %w", home, err)
	}
	for i, x := range s.Shares {
		sh, err := single(x.Path, x.ReadOnly)
		if err != nil {
			lg.Printf("pasta %s fica de fora: %v", x.Path, err)
			continue
		}
		_ = add(fmt.Sprintf("share%d", i), sh)
	}
	if s.Rosetta && runtime.GOARCH == "arm64" {
		if ros := rosettaShare(lg); ros != nil {
			_ = add("rosetta", ros)
		}
	}
	cfg.SetDirectorySharingDevicesVirtualMachineConfiguration(fs)

	if vsock, err := vz.NewVirtioSocketDeviceConfiguration(); err == nil {
		cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsock})
	}
	if bal, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration(); err == nil {
		cfg.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{bal})
	}
	if ok, err := cfg.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("configuração inválida: %v", err)
	}
	return vz.NewVirtualMachine(cfg)
}

type runner struct {
	name    string
	spec    Spec
	vm      *vz.VirtualMachine
	sock    *vz.VirtioSocketDevice
	balloon *vz.VirtioTraditionalMemoryBalloonDevice
	log     *log.Logger

	mu        sync.Mutex
	listeners []net.Listener
	target    uint64
	guest     memInfo
	guestAt   time.Time
	stopping  bool
}

// forward joins a unix socket on the Mac to a vsock port of the guest.
func (r *runner) forward(port uint32, path string) {
	if r.sock == nil {
		return
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		r.log.Printf("socket %s: %v", path, err)
		return
	}
	_ = os.Chmod(path, 0o600)
	r.mu.Lock()
	r.listeners = append(r.listeners, ln)
	r.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				g, err := r.sock.Connect(port)
				if err != nil {
					return // the guest is not listening yet
				}
				defer g.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(g, c); done <- struct{}{} }()
				go func() { _, _ = io.Copy(c, g); done <- struct{}{} }()
				<-done
			}()
		}
	}()
}

func (r *runner) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ln := range r.listeners {
		_ = ln.Close()
	}
}

// stop asks the guest to power off (ACPI), or forces it.
func (r *runner) stop(force bool) {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	if !force && r.vm.CanRequestStop() {
		if ok, err := r.vm.RequestStop(); ok && err == nil {
			go func() {
				time.Sleep(40 * time.Second)
				if r.vm.State() != vz.VirtualMachineStateStopped && r.vm.CanStop() {
					r.log.Printf("o sistema não desligou em 40 s; forçando")
					_ = r.vm.Stop()
				}
			}()
			return
		}
	}
	if r.vm.CanStop() {
		_ = r.vm.Stop()
	}
}

func stateName(s vz.VirtualMachineState) string {
	switch s {
	case vz.VirtualMachineStateRunning:
		return "VirtualMachineStateRunning"
	case vz.VirtualMachineStatePaused:
		return "VirtualMachineStatePaused"
	case vz.VirtualMachineStateStarting, vz.VirtualMachineStateResuming:
		return "VirtualMachineStateStarting"
	case vz.VirtualMachineStateStopping, vz.VirtualMachineStatePausing:
		return "VirtualMachineStateStopping"
	case vz.VirtualMachineStateError:
		return "VirtualMachineStateError"
	}
	return "VirtualMachineStateStopped"
}

// serve is the control API on a unix socket (only the user reaches it).
func (r *runner) serve(path string) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		r.log.Printf("controle: %v", err)
		return
	}
	_ = os.Chmod(path, 0o600)
	r.mu.Lock()
	r.listeners = append(r.listeners, ln)
	r.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vm/state", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"state": stateName(r.vm.State())})
	})
	mux.HandleFunc("POST /vm/state", func(w http.ResponseWriter, req *http.Request) {
		var b struct{ State string }
		_ = json.NewDecoder(io.LimitReader(req.Body, 1<<10)).Decode(&b)
		var err error
		switch b.State {
		case "Stop":
			r.stop(false)
		case "HardStop":
			r.stop(true)
		case "Pause":
			err = r.vm.Pause()
		case "Resume":
			err = r.vm.Resume()
		default:
			err = errors.New("estado desconhecido")
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": stateName(r.vm.State())})
	})
	mux.HandleFunc("GET /vm/memoria", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(r.memory())
	})
	_ = http.Serve(ln, mux)
}

// rosettaShare is the Rosetta folder, when Rosetta is installed.
func rosettaShare(lg *log.Logger) vz.DirectoryShare {
	if vz.LinuxRosettaDirectoryShareAvailability() != vz.LinuxRosettaAvailabilityInstalled {
		lg.Printf("Rosetta não está instalado no Mac (softwareupdate --install-rosetta)")
		return nil
	}
	ros, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		lg.Printf("Rosetta: %v", err)
		return nil
	}
	return ros
}

// The memory balloon. The guest tells how much it uses (/proc/meminfo
// through a vsock port); the target is that plus room to breathe. Going up
// is immediate, coming down is gradual and only after a while stable, so
// the guest never runs short. Without an answer from the guest, the
// machine gets all its memory back.

func (r *runner) readGuest() (memInfo, error) {
	var mi memInfo
	if r.sock == nil {
		return mi, errors.New("sem vsock")
	}
	c, err := r.sock.Connect(portMem)
	if err != nil {
		return mi, err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, err := io.ReadAll(io.LimitReader(c, 64<<10))
	if err != nil && len(b) == 0 {
		return mi, err
	}
	return parseMemInfo(string(b))
}

func (r *runner) setTarget(t uint64) {
	r.mu.Lock()
	r.target = t
	r.mu.Unlock()
	r.balloon.SetTargetVirtualMachineMemorySize(t)
}

func (r *runner) memoryLoop(ctx context.Context) {
	max := uint64(r.spec.MemoryMiB) << 20
	var lowSince time.Time
	fails := 0
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if r.vm.State() != vz.VirtualMachineStateRunning {
			continue
		}
		// The switch in the app applies at once: the spec is read again.
		if sp, err := Load(r.name); err == nil {
			r.mu.Lock()
			r.spec.NoBalloon = sp.NoBalloon
			r.mu.Unlock()
		}
		if !r.spec.balloonOn() {
			if r.balloon.GetTargetVirtualMachineMemorySize() < max {
				r.log.Printf("memória dinâmica desligada: a máquina fica com toda a memória")
				r.setTarget(max)
			}
			lowSince = time.Time{}
			continue
		}
		mi, err := r.readGuest()
		if err != nil {
			fails++
			if fails == 3 && r.balloon.GetTargetVirtualMachineMemorySize() < max {
				r.log.Printf("memória: sem resposta da máquina; devolvendo toda a memória a ela")
				r.setTarget(max)
			}
			continue
		}
		fails = 0
		r.mu.Lock()
		r.guest, r.guestAt = mi, time.Now()
		cur := r.target
		r.mu.Unlock()
		want := balloonTarget(mi, cur, max)
		switch {
		case want > cur:
			r.setTarget(want) // the guest needs it now
			lowSince = time.Time{}
		case want < cur:
			if lowSince.IsZero() {
				lowSince = time.Now()
			}
			if time.Since(lowSince) >= 20*time.Second {
				// Down in steps of 512 MiB, one every 2 s.
				next := want
				if cur-want > 512<<20 {
					next = cur - 512<<20
				}
				r.setTarget(next)
			}
		default:
			lowSince = time.Time{}
		}
	}
}

func (r *runner) memory() MemoryState {
	r.mu.Lock()
	defer r.mu.Unlock()
	ms := MemoryState{Max: uint64(r.spec.MemoryMiB) << 20, Target: r.target, Balloon: r.balloon != nil && r.spec.balloonOn()}
	if !r.guestAt.IsZero() {
		ms.GuestUsed = r.guest.Used()
		ms.GuestCache = r.guest.Cached
		ms.At = r.guestAt.Unix()
	}
	if ms.Target < ms.Max {
		ms.Returned = ms.Max - ms.Target
	}
	return ms
}
