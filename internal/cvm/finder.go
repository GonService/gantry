package cvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// The volumes in the Finder: the machine exports the engines' volume
// folders by NFS, only to this Mac (the gateway of its network), and the
// Mac mounts them with "mount volume", which needs no administrator. The
// containers keep their native disk; only the Finder's access goes over
// the network. Read only unless the user allows writing.

// volRoots are where each engine keeps its volumes in the machine.
var volRoots = map[string]string{
	"docker": "/var/lib/docker/volumes",
	"podman": "/var/lib/containers/storage/volumes",
}

// exportName is the folder exported for an engine (and the name the
// Finder shows: /Volumes/<name>).
func exportName(name, engine string) string { return name + "-" + engine }

func finderEngines(s Spec) []string {
	var out []string
	if s.Docker {
		out = append(out, "docker")
	}
	if s.Podman {
		out = append(out, "podman")
	}
	return out
}

// FinderScript sets up (on) or removes (off) the exports in the machine.
// Idempotent; it runs again at each start while on.
func FinderScript(s Spec, on bool) string {
	var b strings.Builder
	b.WriteString("set -e\nexport DEBIAN_FRONTEND=noninteractive\n")
	if !on {
		b.WriteString("sudo rm -f /etc/exports.d/gantry.exports\ncommand -v exportfs >/dev/null && sudo exportfs -ra || true\n")
		for _, e := range []string{"docker", "podman"} {
			dst := "/srv/gantry/" + exportName(s.Name, e)
			fmt.Fprintf(&b, "mountpoint -q %s && sudo umount %s || true\n", dst, dst)
		}
		return b.String()
	}
	mode := "ro"
	if s.FinderRW {
		mode = "rw"
	}
	if s.System() == OSAlpine {
		b.WriteString("apk info -e nfs-utils >/dev/null 2>&1 || sudo apk add nfs-utils\n")
	} else {
		b.WriteString("dpkg -s nfs-kernel-server >/dev/null 2>&1 || { sudo apt-get update; sudo apt-get install -y --no-install-recommends nfs-kernel-server; }\n")
	}
	b.WriteString("gw=$(ip route | awk '/^default/ {print $3; exit}')\n")
	b.WriteString("[ -n \"$gw\" ] || { echo 'sem rota padrão'; exit 1; }\n")
	b.WriteString("sudo mkdir -p /srv/gantry /etc/exports.d\n: | sudo tee /etc/exports.d/gantry.exports >/dev/null\n")
	for i, e := range finderEngines(s) {
		src, dst := volRoots[e], "/srv/gantry/"+exportName(s.Name, e)
		fmt.Fprintf(&b, "sudo mkdir -p %s %s\n", src, dst)
		fmt.Fprintf(&b, "mountpoint -q %s || sudo mount --bind %s %s\n", dst, src, dst)
		fmt.Fprintf(&b, "echo \"%s $gw(%s,sync,no_subtree_check,insecure,all_squash,anonuid=0,anongid=0,fsid=%d)\" | sudo tee -a /etc/exports.d/gantry.exports >/dev/null\n", dst, mode, i+1)
	}
	if s.System() == OSAlpine {
		b.WriteString("sudo rc-update add nfs default >/dev/null 2>&1; sudo rc-service nfs start >/dev/null 2>&1 || sudo rc-service nfs restart\n")
	} else {
		b.WriteString("sudo systemctl enable --now nfs-server >/dev/null 2>&1 || sudo systemctl enable --now nfs-kernel-server\n")
	}
	b.WriteString("sudo exportfs -ra\n")
	b.WriteString("echo \"ip=$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -n1)\"\n")
	return b.String()
}

// guestIP is the machine's address on the network shared with the Mac.
func guestIP(ctx context.Context, name string) (string, error) {
	out, err := Run(ctx, name, "ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -n1")
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(out)
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("endereço da máquina inválido: %q", ip)
	}
	return ip, nil
}

// FinderMount is one engine's volumes on the Mac.
type FinderMount struct {
	Engine  string `json:"engine"`
	Path    string `json:"path"`    // where it is mounted ("" when not)
	Export  string `json:"export"`  // nfs://…
	Mounted bool   `json:"mounted"` // seen in the Mac's mounts
}

// mountTable maps "host:/path" to the mount point, from mount(8).
func mountTable() map[string]string {
	out, err := exec.Command("mount").Output()
	m := map[string]string{}
	if err != nil {
		return m
	}
	for _, l := range strings.Split(string(out), "\n") {
		// 192.168.64.3:/srv/gantry/gantry-vm-docker on /Volumes/gantry-vm-docker (nfs, …)
		src, rest, ok := strings.Cut(l, " on ")
		if !ok || !strings.Contains(src, ":/srv/gantry/") {
			continue
		}
		mp := rest
		if i := strings.LastIndex(rest, " ("); i >= 0 {
			mp = rest[:i]
		}
		m[src] = mp
	}
	return m
}

// FinderMounts reports the mounts of a machine (by export path, whatever
// the address).
func FinderMounts(name string) []FinderMount {
	s, _ := Load(name)
	tbl := mountTable()
	var out []FinderMount
	for _, e := range finderEngines(s) {
		exp := "/srv/gantry/" + exportName(name, e)
		fm := FinderMount{Engine: e}
		for src, mp := range tbl {
			if strings.HasSuffix(src, ":"+exp) {
				fm.Path, fm.Mounted, fm.Export = mp, true, "nfs://"+strings.TrimSuffix(src, ":"+exp)+exp
			}
		}
		out = append(out, fm)
	}
	return out
}

// MountFinder mounts the exports that are not mounted yet.
func MountFinder(ctx context.Context, name string) error {
	ip, err := guestIP(ctx, name)
	if err != nil {
		return err
	}
	for _, fm := range FinderMounts(name) {
		if fm.Mounted {
			continue
		}
		url := "nfs://" + ip + "/srv/gantry/" + exportName(name, fm.Engine)
		script := `mount volume "` + url + `"`
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := exec.CommandContext(cctx, "osascript", "-e", script).CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("mount volume %s: %s", url, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// UnmountFinder takes the volumes out of the Finder (before the machine
// stops: an NFS mount without its server hangs the Finder).
func UnmountFinder(name string) {
	for _, fm := range FinderMounts(name) {
		if !fm.Mounted || fm.Path == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if exec.CommandContext(ctx, "diskutil", "unmount", fm.Path).Run() != nil {
			_ = exec.CommandContext(ctx, "diskutil", "unmount", "force", fm.Path).Run()
		}
		cancel()
	}
}

// SetFinder turns the volumes in the Finder on or off (and writing).
func SetFinder(ctx context.Context, name string, on, rw bool, log io.Writer) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	// Off, or the mode changes: the mode only takes on a new mount.
	UnmountFinder(name)
	s.Finder, s.FinderRW = on, rw
	if State(ctx, name) == StateRunning {
		fmt.Fprintln(log, "Na máquina: exports do NFS para este Mac")
		out, err := RunFor(ctx, name, FinderScript(s, on), 10*time.Minute)
		_, _ = log.Write([]byte(out))
		if err != nil {
			return err
		}
	}
	if err := Save(s); err != nil {
		return err
	}
	if on && State(ctx, name) == StateRunning {
		fmt.Fprintln(log, "No Mac: montando no Finder")
		if err := MountFinder(ctx, name); err != nil {
			return err
		}
		for _, fm := range FinderMounts(name) {
			fmt.Fprintf(log, "  %s: %s\n", fm.Engine, fm.Path)
		}
	}
	return nil
}

// SyncFinder puts the exports back and mounts them after a start.
func SyncFinder(ctx context.Context, name string) error {
	s, err := Load(name)
	if err != nil || !s.Finder {
		return err
	}
	if _, err := RunFor(ctx, name, FinderScript(s, true), 10*time.Minute); err != nil {
		return err
	}
	return MountFinder(ctx, name)
}

var volName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// OpenVolume shows a volume in the Finder. mountpoint is the volume's
// folder in the machine (as the engine reports it).
func OpenVolume(ctx context.Context, name, engine, vol, mountpoint string) error {
	root, ok := volRoots[engine]
	if !ok || !volName.MatchString(vol) {
		return errors.New("volume inválido")
	}
	if mountpoint == "" {
		mountpoint = path.Join(root, vol, "_data")
	}
	rel, ok := strings.CutPrefix(path.Clean(mountpoint), root+"/")
	if !ok {
		return errors.New("esse volume fica fora da pasta de volumes do engine")
	}
	if err := MountFinder(ctx, name); err != nil {
		return err
	}
	for _, fm := range FinderMounts(name) {
		if fm.Engine == engine && fm.Mounted {
			p := path.Join(fm.Path, rel)
			if _, err := os.Stat(p); err != nil {
				p = fm.Path
			}
			return exec.CommandContext(ctx, "open", p).Run()
		}
	}
	return errors.New("os volumes não estão montados no Finder")
}
