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
if [ -d /etc/sudoers.d ] && [ ! -e /etc/sudoers.d/gantry-logs ]; then
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

// guestTune is the script for one system, run as root.
func guestTune(system string) string {
	r, ok := sshdReload[system]
	if !ok {
		r = sshdReload[OSCustom]
	}
	return fmt.Sprintf(guestTuneTmpl, r)
}

// guestTuneSudo is guestTune for a line that runs as the gantry user.
func guestTuneSudo(system string) string {
	return "sudo sh -s <<'GANTRY_TUNE'\n" + guestTune(system) + "GANTRY_TUNE\n"
}
