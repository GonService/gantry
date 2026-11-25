package cvm

import "fmt"

// guestTuneTmpl is what every Container VM needs whatever its system, as root
// and safe to run again: the provisioning runs it once and every start runs
// it again (so machines made by an older Gantry get it too).
//
//   - IP forwarding on, from boot. When dockerd is the one that turns it on,
//     it also sets the policy of the iptables FORWARD chain to DROP and
//     accepts only its own networks: the ports published by Podman and
//     nerdctl then answer inside the machine (curl localhost) and nowhere
//     else, not at the machine's IP nor through the Mac's localhost. With
//     forwarding already on, Docker leaves the policy alone; a DROP left
//     from an earlier boot goes back to ACCEPT here.
//   - No getty on the serial ports: the console is hvc0, and busybox init
//     restarts a getty on a missing ttyAMA0/ttyS0 every second, two lines of
//     log each time.
//   - Logs with a limit, so they do not fill the disks: 10 MB × 3 for each
//     Docker container (new ones; the daemon reads it at its next start),
//     10 MB for each Podman container, 20 MB for each service log file on
//     Alpine (checked every 15 minutes) and 200 MB of journal on Debian.
//     Without it, the API service of Podman, failing on every request,
//     wrote 8.8 GB of log in a day and filled the system disk.
//   - Files are written to a temporary name and moved in place, and checked
//     by content, not by existence: with the disk full a write leaves an
//     empty file, and the next start must write it again, not skip it.
//   - Gantry's own ssh (over vsock, from 127.0.0.1) and the sudo of the log
//     tab stay out of the log: they come every few seconds and would bury
//     everything else.
const guestTuneTmpl = `echo 'net.ipv4.ip_forward = 1' >/etc/sysctl.d/90-gantry.conf
sysctl -q -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || echo 1 >/proc/sys/net/ipv4/ip_forward || true
if command -v rc-update >/dev/null 2>&1; then rc-update add sysctl boot >/dev/null 2>&1 || true; fi
for ipt in iptables ip6tables; do
  if command -v "$ipt" >/dev/null 2>&1 && "$ipt" -S FORWARD 2>/dev/null | grep -qx -- '-P FORWARD DROP'; then
    "$ipt" -P FORWARD ACCEPT || true
  fi
done
if [ -f /etc/inittab ] && grep -Eq '^(ttyS|ttyAMA)[0-9]+::' /etc/inittab; then
  sed -i -E 's/^((ttyS|ttyAMA)[0-9]+::)/#\1/' /etc/inittab
  kill -HUP 1 || true
fi
if [ -f /etc/ssh/sshd_config ] && ! grep -q '^# gantry: ssh do Gantry' /etc/ssh/sshd_config; then
  cp /etc/ssh/sshd_config /etc/ssh/sshd_config.gantry-antes
  printf '\n# gantry: ssh do Gantry (vsock, 127.0.0.1) fora do log\nMatch Address 127.0.0.1,::1\n    LogLevel ERROR\n' >>/etc/ssh/sshd_config
  if sshd -t >/dev/null 2>&1 || /usr/sbin/sshd -t >/dev/null 2>&1; then
%[1]s
  else
    mv /etc/ssh/sshd_config.gantry-antes /etc/ssh/sshd_config
  fi
fi
if [ -f /etc/docker/daemon.json ] && [ "$(tr -d ' \n' </etc/docker/daemon.json)" = '{"features":{"containerd-snapshotter":true}}' ]; then
  echo '{ "features": { "containerd-snapshotter": true }, "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "3" } }' >/etc/docker/daemon.gantry &&
    mv /etc/docker/daemon.gantry /etc/docker/daemon.json || rm -f /etc/docker/daemon.gantry
fi
if ! grep -qs log_size_max /etc/containers/containers.conf.d/90-gantry.conf; then
  mkdir -p /etc/containers/containers.conf.d &&
    printf '[containers]\nlog_size_max = 10485760\n' >/etc/containers/containers.conf.d/90-gantry.tmp &&
    mv /etc/containers/containers.conf.d/90-gantry.tmp /etc/containers/containers.conf.d/90-gantry.conf ||
    rm -f /etc/containers/containers.conf.d/90-gantry.tmp
fi
rm -f /etc/periodic/hourly/gantry-logs
if [ -d /etc/periodic/15min ] && ! grep -qs 'gantry-logs v2' /etc/periodic/15min/gantry-logs; then
  cat >/etc/periodic/15min/gantry-logs.tmp <<'EOF' && chmod 0755 /etc/periodic/15min/gantry-logs.tmp &&
#!/bin/sh
# gantry-logs v2: the logs of the services stay under 20 MB (the last lines).
# With the disk already full there is no room for the copy: the file is
# emptied instead, which frees the space at once.
for f in /var/log/docker.log /var/log/gantry-*.log; do
  [ -f "$f" ] || continue
  [ $(wc -c <"$f") -gt 20971520 ] || continue
  if tail -n 20000 "$f" >"$f.tmp" 2>/dev/null; then cat "$f.tmp" >"$f"; else : >"$f"; fi
  rm -f "$f.tmp"
done
EOF
    mv /etc/periodic/15min/gantry-logs.tmp /etc/periodic/15min/gantry-logs ||
    rm -f /etc/periodic/15min/gantry-logs.tmp
fi
%[2]sif [ -d /etc/sudoers.d ] && [ ! -e /etc/sudoers.d/gantry-logs ]; then
  printf 'Cmnd_Alias GANTRY_LOGS = /usr/bin/tail, /usr/bin/journalctl, /bin/journalctl\nDefaults!GANTRY_LOGS !syslog\n' >/etc/sudoers.d/gantry-logs
  chmod 0440 /etc/sudoers.d/gantry-logs
  visudo -cf /etc/sudoers.d/gantry-logs >/dev/null 2>&1 || rm -f /etc/sudoers.d/gantry-logs
fi
`

// sshdReload, by system: each one reloads sshd its own way.
var sshdReload = map[string]string{
	OSDebian: "    systemctl reload ssh 2>/dev/null || true",
	OSAlpine: "    rc-service sshd reload >/dev/null 2>&1 || true",
	OSCustom: `    if [ -d /run/systemd/system ]; then systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
    elif command -v rc-service >/dev/null 2>&1; then rc-service sshd reload >/dev/null 2>&1 || true
    fi`,
}

// journalCap keeps the journal small where there is one (not on Alpine).
const journalCap = `if [ -d /run/systemd/system ] && [ ! -e /etc/systemd/journald.conf.d/90-gantry.conf ]; then
  mkdir -p /etc/systemd/journald.conf.d
  printf '[Journal]\nSystemMaxUse=200M\n' >/etc/systemd/journald.conf.d/90-gantry.conf
  systemctl restart systemd-journald 2>/dev/null || true
fi
`

// guestTune is the script for one system, run as root.
func guestTune(system string) string {
	r, ok := sshdReload[system]
	if !ok {
		r = sshdReload[OSCustom]
	}
	j := journalCap
	if system == OSAlpine {
		j = ""
	}
	return fmt.Sprintf(guestTuneTmpl, r, j)
}

// guestTuneSudo is guestTune for a line that runs as the gantry user.
func guestTuneSudo(system string) string {
	return "sudo sh -s <<'GANTRY_TUNE'\n" + guestTune(system) + "GANTRY_TUNE\n"
}
