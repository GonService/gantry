# What Gantry uses on a machine: one line per tool, "name=path" (empty when
# missing), plus the system, the architecture and the package manager.
# Runs with sh locally or over SSH.
os=$(uname -s); arch=$(uname -m)
echo "os=$os"; echo "arch=$arch"
case "$arch" in arm64|aarch64) qa=aarch64 ;; *) qa=x86_64 ;; esac
pm=
for p in brew apt-get dnf yum pacman zypper apk; do
  if command -v $p >/dev/null 2>&1; then pm=$p; break; fi
done
echo "pm=$pm"
for t in docker podman nerdctl docker-compose podman-compose qemu-system-$qa qemu-img hdiutil genisoimage mkisofs xorriso curl wget helm git ssh zenity kdialog socat python3 dynamips; do
  echo "$t=$(command -v $t 2>/dev/null)"
done
echo "qemu-system=$(command -v qemu-system-$qa 2>/dev/null)"
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then echo "docker-compose-plugin=1"; fi
if command -v podman >/dev/null 2>&1 && podman compose version >/dev/null 2>&1; then echo "podman-compose-wrapper=1"; fi
# Docker from the official repository (download.docker.com): there the
# compose package is docker-compose-plugin, not the distribution's one.
if ls /etc/yum.repos.d/docker-ce*.repo /etc/apt/sources.list.d/docker*.list /etc/apt/sources.list.d/docker*.sources /etc/zypp/repos.d/docker-ce*.repo >/dev/null 2>&1 \
  || grep -qs download.docker.com /etc/apt/sources.list /etc/yum.repos.d/*.repo; then
  echo "docker-repo=1"
fi
[ -e /dev/kvm ] && echo "kvm-exists=1"
[ -w /dev/kvm ] && echo "kvm=1"
for h in /usr/lib/qemu/qemu-bridge-helper /usr/libexec/qemu-bridge-helper /usr/lib/qemu-bridge-helper; do
  [ -x "$h" ] && { echo "bridge-helper=$h"; break; }
done
for v in /opt/homebrew/opt/socket_vmnet/bin/socket_vmnet /usr/local/opt/socket_vmnet/bin/socket_vmnet /opt/socket_vmnet/bin/socket_vmnet; do
  [ -x "$v" ] && { echo "socket_vmnet=$v"; break; }
done
# 32-bit loader for the old IOL images (i86bi_linux…) on 64-bit Linux.
if [ "$os" = Linux ] && [ "$arch" = x86_64 ]; then
  [ -e /lib/ld-linux.so.2 ] && echo "lib32=1" || echo "lib32="
fi
exit 0
