# vmctl: QEMU VMs of Gantry labs. Runs with sh on the lab's machine (the Mac
# itself or a server over SSH). Everything a VM has lives in one folder:
#   ${XDG_STATE_HOME:-~/.local/state}/gantry/vms/<project>/<vm>/
# Images are cached in ${XDG_CACHE_HOME:-~/.cache}/gantry/images/.
#
# Usage: sh vmctl.sh <up|start|stop|restart|rm|status|stats|ssh|keyfile|netinfo|lan-ip|image|
#   snap-list|snap-create|snap-restore|snap-delete|export|export-clean|import|
#   resize|clone|autostart-set|autostart|convert|spec-set> ; input in GV_* vars.
#
# Project network (lan): besides the user-mode NIC (internet, SSH, published
# ports), a VM with GV_LAN=1 gets a second NIC on the machine's VM network,
# with a fixed IP from $ROOT/lan.ips. On macOS the network is socket_vmnet
# (brew install socket_vmnet; sudo brew services start socket_vmnet); on
# Linux and WSL2 it is the bridge gantry0, through qemu-bridge-helper.
set -u

ROOT=${XDG_STATE_HOME:-$HOME/.local/state}/gantry/vms
CACHE=${XDG_CACHE_HOME:-$HOME/.cache}/gantry/images
P=${GV_PROJECT:-}
N=${GV_NAME:-}
D=$ROOT/$P/$N

die() { echo "erro: $*" >&2; exit 1; }
say() { echo "$*"; }

# The project and node names become paths and go, quoted, into the start.sh
# of each node: only letters, digits, . _ and -, never a path of their own.
for v in "$P" "$N"; do
  case "$v" in
    "" | . | .. | .* | *[!A-Za-z0-9._-]*) [ -z "$v" ] || die "nome inválido: '$v' (use letras, números, . _ e -)" ;;
  esac
done
# plain VALUE WHAT: values that go between ' ' in a start.sh (image files,
# ISOs) may not close the quotes nor break the line.
NL='
'
CR=$(printf '\r')
plain() {
  case "$1" in *"'"* | *"$NL"* | *"$CR"*) die "$2 tem caracteres que não são aceitos (aspas ou quebra de linha): $1" ;; esac
}

case "$(uname -m)" in
  arm64 | aarch64) ARCH=aarch64; DARCH=arm64 ;;
  x86_64 | amd64) ARCH=x86_64; DARCH=amd64 ;;
  *) die "arquitetura sem suporte: $(uname -m)" ;;
esac

hint() {
  case "$(uname -s)" in
    Darwin) echo "Instale com: brew install qemu" ;;
    *) if command -v dnf >/dev/null 2>&1; then echo "Instale com: sudo dnf install qemu-kvm qemu-img genisoimage"
       elif command -v apt-get >/dev/null 2>&1; then echo "Instale com: sudo apt install qemu-system qemu-utils genisoimage"
       else echo "Instale o QEMU (qemu-system e qemu-img) e o genisoimage"; fi ;;
  esac
}

need_qemu() {
  command -v "qemu-system-$ARCH" >/dev/null 2>&1 || die "o QEMU (qemu-system-$ARCH) não está instalado nesta máquina. $(hint)"
  command -v qemu-img >/dev/null 2>&1 || die "o qemu-img não está instalado nesta máquina. $(hint)"
}

alive() { [ -f "$1/qemu.pid" ] && kill -0 "$(cat "$1/qemu.pid")" 2>/dev/null; }
# isprog: a node that is a program on the machine (IOL, Dynamips, VPCS), not a VM.
isprog() { [ -f "$1/iol" ] || [ -f "$1/dyn" ] || [ -f "$1/vpcs" ]; }

accel() {
  case "$(uname -s)" in
    Darwin) echo hvf ;;
    Linux) if [ -w /dev/kvm ]; then echo kvm; else echo tcg; fi ;;
    *) echo tcg ;;
  esac
}

firmware() {
  for f in \
    "$(brew --prefix qemu 2>/dev/null)/share/qemu/edk2-aarch64-code.fd" \
    /opt/homebrew/share/qemu/edk2-aarch64-code.fd /usr/local/share/qemu/edk2-aarch64-code.fd \
    /usr/share/qemu/edk2-aarch64-code.fd /usr/share/AAVMF/AAVMF_CODE.fd \
    /usr/share/qemu-efi-aarch64/QEMU_EFI.fd /usr/share/edk2/aarch64/QEMU_EFI.fd; do
    [ -f "$f" ] && { echo "$f"; return; }
  done
  die "não achei o firmware UEFI para aarch64 (edk2). $(hint)"
}

# firmware_x86 is the UEFI firmware for x86 VMs whose image boots only
# with UEFI (GV_EFI=1, the NixOS images); the others keep the BIOS.
firmware_x86() {
  for f in \
    "$(brew --prefix qemu 2>/dev/null)/share/qemu/edk2-x86_64-code.fd" \
    /opt/homebrew/share/qemu/edk2-x86_64-code.fd /usr/local/share/qemu/edk2-x86_64-code.fd \
    /usr/share/qemu/edk2-x86_64-code.fd /usr/share/OVMF/OVMF_CODE.fd /usr/share/OVMF/OVMF_CODE_4M.fd \
    /usr/share/edk2/ovmf/OVMF_CODE.fd /usr/share/edk2/x64/OVMF_CODE.fd /usr/share/qemu/OVMF.fd; do
    [ -f "$f" ] && { echo "$f"; return; }
  done
  die "não achei o firmware UEFI para x86_64 (OVMF/edk2), que a imagem $GV_IMAGE precisa. Instale o pacote ovmf (ou edk2-ovmf)"
}

# image_url turns a short name into where the image comes from on this
# machine: a URL for the cloud images, a path for the images Gantry builds
# itself (NixOS), whose file the user points to in the settings (GV_IMAGE_SRC).
image_url() {
  case "$1" in
    debian-12 | debian) echo "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-$DARCH.qcow2" ;;
    debian-13) echo "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-$DARCH.qcow2" ;;
    ubuntu-24.04 | ubuntu) echo "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-$DARCH.img" ;;
    ubuntu-22.04) echo "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-$DARCH.img" ;;
    rocky-9 | rocky) echo "https://dl.rockylinux.org/pub/rocky/9/images/$ARCH/Rocky-9-GenericCloud-Base.latest.$ARCH.qcow2" ;;
    alma-9 | almalinux) echo "https://repo.almalinux.org/almalinux/9/cloud/$ARCH/images/AlmaLinux-9-GenericCloud-latest.$ARCH.qcow2" ;;
    nixos-lab-26.05 | cisco-*) echo "${GV_IMAGE_SRC:-}" ;;
    http://* | https://*) echo "$1" ;;
    *) echo "" ;;
  esac
}

# image_sums is the checksum list published next to a catalog image (one
# line per file, GNU or BSD format, sha256 or sha512). GV_IMAGE_SUMS gives
# one for any other URL.
image_sums() {
  [ -n "${GV_IMAGE_SUMS:-}" ] && { echo "$GV_IMAGE_SUMS"; return; }
  u=$1
  case "$u" in
    https://cloud.debian.org/*) echo "${u%/*}/SHA512SUMS" ;;
    https://cloud-images.ubuntu.com/*) echo "${u%/*}/SHA256SUMS" ;;
    https://dl.rockylinux.org/*) echo "$u.CHECKSUM" ;;
    https://repo.almalinux.org/*) echo "${u%/*}/CHECKSUM" ;;
    *) echo "" ;;
  esac
}

# sum_of prints the checksum of a file with as many hex digits as the
# expected one has (64: sha256, 128: sha512).
sum_of() { # file digits
  a=256; [ "$2" -eq 128 ] && a=512
  if command -v "sha${a}sum" >/dev/null 2>&1; then "sha${a}sum" "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a "$a" "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then openssl dgst "-sha$a" "$1" | sed 's/.*= *//'
  else return 1; fi
}

# verify checks a downloaded image against the published checksum list.
# A wrong or missing checksum is an error: the image is not used.
verify() { # file name sums_url
  sums=$1.sums
  fetch "$3" "$sums" || { rm -f "$sums"; die "não consegui baixar a lista de checksums ($3); a imagem não foi usada"; }
  want=$(awk -v n="$2" '
    $1 ~ /^SHA(256|512)$/ && $2 == "(" n ")" { print $4; exit }
    { f = $2; sub(/^\*/, "", f) }
    NF >= 2 && f == n && $1 ~ /^[0-9a-fA-F]+$/ { print $1; exit }' "$sums" | tr 'A-F' 'a-f')
  rm -f "$sums"
  case ${#want} in
    64 | 128) ;;
    *) die "a lista $3 não tem o checksum de $2; a imagem não foi usada" ;;
  esac
  got=$(sum_of "$1" "${#want}") || die "preciso do sha256sum, shasum ou openssl para conferir a imagem"
  [ "$got" = "$want" ] || die "o checksum de $2 não confere (esperado $want, veio $got); a imagem baixada foi descartada"
}

fetch() { # url dest
  if command -v curl >/dev/null 2>&1; then curl -fL --retry 2 -sS -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then wget -q -O "$2" "$1"
  else die "preciso do curl ou do wget para baixar a imagem"; fi
}

base_image() {
  img=${GV_IMAGE:-debian-12}
  # shellcheck disable=SC2088
  case "$img" in "~/"*) img=$HOME/${img#"~/"} ;; esac
  case "$img" in
    /* | ./* | ~/*) [ -f "$img" ] || die "imagem não encontrada: $img"; echo "$img"; return ;;
  esac
  url=$(image_url "$img")
  case "$img" in
    nixos-* | cisco-*) [ -n "$url" ] || die "a imagem $img ainda não foi configurada nesta máquina; ponha o arquivo numa das pastas de imagens (página da máquina › Configurações › Imagens)" ;;
  esac
  [ -n "$url" ] || die "imagem desconhecida: $img (use debian-12, debian-13, ubuntu-24.04, ubuntu-22.04, rocky-9, alma-9, nixos-lab-26.05, cisco-iosvl2, cisco-iosv, cisco-csr1000v, uma URL ou um caminho)"
  # shellcheck disable=SC2088
  case "$url" in "~/"*) url=$HOME/${url#"~/"} ;; esac
  case "$url" in
    /*) [ -f "$url" ] || die "a imagem $img não está em $url (confira em Configurações › Geral › Imagens de VM)"; echo "$url"; return ;;
  esac
  mkdir -p "$CACHE"
  f=$CACHE/$(basename "${url%%\?*}")
  if [ ! -s "$f" ]; then
    say "baixando $(basename "$f") (só na primeira vez)…" >&2
    fetch "$url" "$f.part" || { rm -f "$f.part"; die "falha ao baixar $url"; }
    sums=$(image_sums "$url")
    if [ -n "$sums" ]; then
      (verify "$f.part" "$(basename "$f")" "$sums") || { rm -f "$f.part"; exit 1; }
      say "checksum de $(basename "$f") conferido" >&2
    fi
    mv "$f.part" "$f"
  fi
  echo "$f"
}

# iso_file finds or downloads the installer ISO.
iso_file() {
  f=$GV_ISO
  case "$f" in "~/"*) f=$HOME/${f#"~/"} ;; esac
  case "$f" in
    http://* | https://*)
      mkdir -p "$CACHE"
      c=$CACHE/$(basename "${f%%\?*}" | tr -c 'A-Za-z0-9._\n-' _)
      if [ ! -s "$c" ]; then
        say "baixando $(basename "$c") (só na primeira vez)…" >&2
        fetch "$f" "$c.part" || { rm -f "$c.part"; die "falha ao baixar $f"; }
        mv "$c.part" "$c"
      fi
      echo "$c" ;;
    *) plain "$f" "o caminho da ISO"; [ -f "$f" ] || die "ISO não encontrada: $f"; echo "$f" ;;
  esac
}

# write_seed writes the cloud-init files of the VM in a folder: user-data,
# meta-data, network-config and, for a clone, vendor-data (a script that
# gives the copy a machine-id and SSH host keys of its own).
write_seed() {
  k=$(key) || return 1
  mkdir -p "$1"
  printf '%s' "${GV_USERDATA:-}" | b64d | sed "s|__GANTRY_PUBKEY__|$(cat "$k.pub")|" >"$1/user-data"
  # instance-id is fixed for the disk, so cloud-init runs once per VM (a
  # clone gets its own, so it is configured as a new machine).
  [ -f "$D/instance-id" ] || echo "gantry-$P-$N-$(date +%s)" >"$D/instance-id"
  printf 'instance-id: %s\nlocal-hostname: %s\n' "$(cat "$D/instance-id")" "$N" >"$1/meta-data"
  rm -f "$1/network-config" "$1/vendor-data"
  [ -n "${GV_NETCFG:-}" ] && printf '%s' "$GV_NETCFG" | b64d >"$1/network-config"
  if [ -f "$D/clone" ]; then
    cat >"$1/vendor-data" <<'VD'
#!/bin/sh
# Gantry: this VM is a copy of another; it gets an identity of its own.
rm -f /etc/machine-id /var/lib/dbus/machine-id
systemd-machine-id-setup >/dev/null 2>&1 || dbus-uuidgen --ensure=/etc/machine-id
if [ -d /var/lib/dbus ] && [ ! -e /var/lib/dbus/machine-id ]; then ln -s /etc/machine-id /var/lib/dbus/machine-id; fi
# NixOS: sshd makes the host keys that are missing when it starts.
if [ -e /etc/NIXOS ]; then rm -f /etc/ssh/ssh_host_*; systemctl restart sshd 2>/dev/null || systemctl restart ssh; fi
VD
  fi
  return 0
}

make_iso() { # dir iso
  rm -f "$2"
  set -- "$1" "$2" "$1"/user-data "$1"/meta-data
  [ -f "$1/network-config" ] && set -- "$@" "$1/network-config"
  [ -f "$1/vendor-data" ] && set -- "$@" "$1/vendor-data"
  if command -v hdiutil >/dev/null 2>&1; then hdiutil makehybrid -quiet -iso -joliet -default-volume-name cidata -o "$2" "$1"
  elif command -v genisoimage >/dev/null 2>&1; then d=$1; o=$2; shift 2; genisoimage -quiet -output "$o" -volid cidata -joliet -rock "$@"
  elif command -v mkisofs >/dev/null 2>&1; then d=$1; o=$2; shift 2; mkisofs -quiet -output "$o" -volid cidata -joliet -rock "$@"
  elif command -v xorriso >/dev/null 2>&1; then d=$1; o=$2; shift 2; xorriso -as mkisofs -quiet -output "$o" -volid cidata -joliet -rock "$@"
  else die "preciso do genisoimage, mkisofs ou xorriso para o cloud-init. $(hint)"; fi
}

b64d() { base64 -d 2>/dev/null || base64 -D; }

key() {
  k=$ROOT/id_ed25519
  if [ ! -f "$k" ]; then
    mkdir -p "$ROOT" && chmod 700 "$ROOT"
    ssh-keygen -q -t ed25519 -N "" -C "gantry-vms" -f "$k" || die "não consegui criar a chave SSH das VMs"
  fi
  echo "$k"
}

# mon sends a command to the QEMU monitor.
mon() {
  s=$D/mon.sock
  [ -S "$s" ] || return 1
  if command -v socat >/dev/null 2>&1; then printf '%s\n' "$1" | socat - "UNIX-CONNECT:$s" >/dev/null 2>&1
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import socket,sys,time
s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); time.sleep(0.2); s.recv(4096)
s.sendall((sys.argv[2]+"\n").encode()); time.sleep(0.3); s.close()' "$s" "$1" 2>/dev/null
  elif command -v nc >/dev/null 2>&1; then printf '%s\n' "$1" | nc -U -w 1 "$s" >/dev/null 2>&1
  else return 1; fi
}

# mon_out sends a command to the QEMU monitor and prints its answer.
mon_out() {
  s=$D/mon.sock
  [ -S "$s" ] || return 1
  command -v python3 >/dev/null 2>&1 || return 1
  python3 -c 'import socket,sys,time
s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.settimeout(1)
def read():
    out=b""
    try:
        while not out.endswith(b"(qemu) "):
            d=s.recv(4096)
            if not d: break
            out+=d
    except socket.timeout: pass
    return out
read()
s.sendall((sys.argv[2]+"\n").encode())
out=read().decode("latin-1").replace("\r","")
lines=[l for l in out.split("\n") if l.strip() and not l.startswith("(qemu)") and sys.argv[2] not in l]
print("\n".join(lines))' "$s" "$1"
}

# port_free tells whether a port of the host is free (proto ip port).
port_free() {
  python3 -c 'import socket,sys
t=socket.SOCK_DGRAM if sys.argv[1]=="udp" else socket.SOCK_STREAM
s=socket.socket(socket.AF_INET,t); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
try: s.bind((sys.argv[2] or "0.0.0.0", int(sys.argv[3])))
except OSError: sys.exit(1)' "$1" "$2" "$3"
}

# port_live publishes (GV_OP=add) or takes out (remove) a port of a VM
# (GV_PORT=proto:ip:host:guest) without restarting it, through the
# monitor's hostfwd; a stopped VM just gets it in its definition.
port_live() {
  need_disk
  p=${GV_PORT:-}
  proto=${p%%:*}; rest=${p#*:}; ip=${rest%%:*}; rest=${rest#*:}; hp=${rest%%:*}; gp=${rest#*:}
  case "$proto" in tcp | udp) ;; *) die "protocolo inválido: $proto" ;; esac
  case "$hp$gp" in '' | *[!0-9]*) die "porta inválida: $p" ;; esac
  have=$(sed -n 's/^ports=//p' "$D/vm.env")
  if [ "${GV_OP:-}" = add ]; then
    case " $have " in *" $p "*) say "$N: a porta $hp já está publicada"; return 0 ;; esac
    port_free "$proto" "$ip" "$hp" || die "a porta $hp do host já está em uso; escolha outra"
    if alive "$D"; then
      r=$(mon_out "hostfwd_add n0 $proto:$ip:$hp-:$gp") || die "não consegui falar com o monitor do QEMU (precisa do python3)"
      [ -z "$r" ] || die "o QEMU recusou a porta $hp: $r"
    fi
    new=$(printf '%s %s' "$have" "$p" | xargs)
    msg="$N: porta $hp → $gp publicada"
  else
    if alive "$D"; then
      r=$(mon_out "hostfwd_remove n0 $proto:$ip:$hp") || die "não consegui falar com o monitor do QEMU (precisa do python3)"
      case "$r" in *"removed"* | "") ;; *) die "o QEMU não tirou a porta $hp: $r" ;; esac
    fi
    new=$(for x in $have; do [ "$x" = "$p" ] || printf '%s ' "$x"; done | xargs)
    msg="$N: porta $hp tirada"
  fi
  sed -i.bak "s|^ports=.*|ports=$new|" "$D/vm.env" && rm -f "$D/vm.env.bak"
  # The next start keeps it too (start.sh has the forwards of the VM).
  python3 - "$D/start.sh" "${GV_OP:-}" "$proto:$ip:$hp-:$gp" <<'PY' || die "não consegui atualizar o start.sh de $N"
import sys
path, op, rule = sys.argv[1:4]
s = open(path).read()
if op == "add":
    if "hostfwd=" + rule not in s:
        s = s.replace("id=n0,", "id=n0,hostfwd=" + rule + ",", 1)
else:
    s = s.replace(",hostfwd=" + rule, "").replace("hostfwd=" + rule + ",", "")
open(path, "w").write(s)
PY
  alive "$D" && say "$msg (sem reiniciar)" || say "$msg (vale quando a VM subir)"
}

# conf_get reads the machine's choice of VM network, $ROOT/rede.conf
# (written by net-set): modo=bridge puts the VMs on the network the machine
# itself is on (socket_vmnet bridged on macOS, a bridge of the system on
# Linux), with fixed addresses from a range outside the router's DHCP.
conf_get() { [ -f "$ROOT/rede.conf" ] && sed -n "s/^$1=//p" "$ROOT/rede.conf" | head -1; return 0; }

# default_route prints the interface and the gateway of the way out of this
# machine (what the bridge mode suggests).
default_route() {
  if [ "$(uname -s)" = Darwin ]; then
    route -n get default 2>/dev/null | awk '/interface:/ {print "lan_iface=" $2} /gateway:/ {print "lan_gw=" $2}'
  elif command -v ip >/dev/null 2>&1; then
    ip -4 route show default 2>/dev/null | awk '{for (i = 1; i < NF; i++) {if ($i == "dev") print "lan_iface=" $(i+1); if ($i == "via") print "lan_gw=" $(i+1)}; exit}'
  fi
}

# netinfo tells which VM network this machine has: socket_vmnet (macOS) or
# the gantry0 bridge (Linux, WSL2), with its gateway and /24 prefix; with
# modo=bridge, the machine's own network.
netinfo() {
  default_route
  if [ "$(conf_get modo)" = bridge ]; then
    netinfo_bridge
    return 0
  fi
  echo "net=compartilhada"; echo "first=100"; echo "last=250"
  if [ "$(uname -s)" = Darwin ]; then
    bp=$(brew --prefix 2>/dev/null)
    for sk in "${GV_VMNET_SOCK:-}" "$bp/var/run/socket_vmnet" /var/run/socket_vmnet; do
      [ -n "$sk" ] && [ -S "$sk" ] || continue
      for cl in "$bp/opt/socket_vmnet/bin/socket_vmnet_client" /opt/socket_vmnet/bin/socket_vmnet_client; do
        [ -x "$cl" ] || continue
        gw=${GV_VMNET_GW:-192.168.105.1}
        echo "mode=vmnet"; echo "sock=$sk"; echo "client=$cl"; echo "gateway=$gw"; echo "prefix=${gw%.*}"
        [ -w "$sk" ] || echo "problem=sem permissão no socket $sk"
        return 0
      done
    done
    echo "mode=none"
    echo "hint=brew install socket_vmnet && sudo brew services start socket_vmnet"
    return 0
  fi
  helper=""
  for h in /usr/lib/qemu/qemu-bridge-helper /usr/libexec/qemu-bridge-helper /usr/lib/qemu-bridge-helper; do [ -x "$h" ] && helper=$h && break; done
  # One script turns it on and keeps it across reboots (systemd, when there is one).
  fix="sudo sh -c 'mkdir -p /etc/qemu && (grep -qs \"^allow gantry0\" /etc/qemu/bridge.conf || echo \"allow gantry0\" >> /etc/qemu/bridge.conf)"
  [ -n "$helper" ] && fix="$fix && chmod u+s $helper"
  fix="$fix && (ip link add gantry0 type bridge 2>/dev/null; ip addr add 10.213.0.1/24 dev gantry0 2>/dev/null; ip link set gantry0 up) && if [ -d /run/systemd/system ]; then printf \"[Unit]\\nDescription=Rede de VMs do Gantry\\nAfter=network.target\\n[Service]\\nType=oneshot\\nRemainAfterExit=yes\\nExecStart=/bin/sh -c \\\"ip link add gantry0 type bridge 2>/dev/null; ip addr add 10.213.0.1/24 dev gantry0 2>/dev/null; ip link set gantry0 up\\\"\\n[Install]\\nWantedBy=multi-user.target\\n\" > /etc/systemd/system/gantry-net.service && systemctl daemon-reload && systemctl enable gantry-net; fi'"
  if command -v ip >/dev/null 2>&1 && ip link show gantry0 >/dev/null 2>&1; then
    gw=$(ip -4 -o addr show gantry0 2>/dev/null | awk '{split($4, a, "/"); print a[1]; exit}')
    echo "mode=bridge"; echo "bridge=gantry0"; echo "gateway=$gw"; echo "prefix=${gw%.*}"
    bad=""
    [ -n "$gw" ] || { echo "problem=a bridge gantry0 não tem IP"; bad=1; }
    if [ -z "$helper" ]; then echo "problem=não achei o qemu-bridge-helper (pacote qemu-system-common)"; bad=1
    elif [ ! -u "$helper" ]; then echo "problem=o qemu-bridge-helper precisa de permissão de administrador (setuid)"; bad=1; fi
    grep -qsE '^allow (gantry0|all)' /etc/qemu/bridge.conf || { echo "problem=falta 'allow gantry0' em /etc/qemu/bridge.conf"; bad=1; }
    [ -n "$bad" ] && printf "%s\n" "hint=$fix"
    return 0
  fi
  echo "mode=none"
  if [ -z "$helper" ]; then
    printf "%s\n" "hint=Instale o QEMU com o qemu-system-common (tem o qemu-bridge-helper) e depois: $fix"
  else
    printf "%s\n" "hint=$fix"
  fi
}

# netinfo_bridge is netinfo for modo=bridge.
netinfo_bridge() {
  ifc=$(conf_get interface); gw=$(conf_get gateway)
  echo "net=bridge"; echo "interface=$ifc"
  echo "first=$(conf_get inicio)"; echo "last=$(conf_get fim)"
  if [ "$(uname -s)" = Darwin ]; then
    bp=$(brew --prefix 2>/dev/null)
    sk=$bp/var/run/socket_vmnet.bridged.$ifc
    cl=""
    for c in "$bp/opt/socket_vmnet/bin/socket_vmnet_client" /opt/socket_vmnet/bin/socket_vmnet_client; do [ -x "$c" ] && cl=$c && break; done
    if [ -S "$sk" ] && [ -n "$cl" ]; then
      echo "mode=vmnet"; echo "sock=$sk"; echo "client=$cl"; echo "gateway=$gw"; echo "prefix=${gw%.*}"
      [ -w "$sk" ] || echo "problem=sem permissão no socket $sk"
      return 0
    fi
    echo "mode=none"
    if [ -z "$cl" ]; then
      echo "hint=brew install socket_vmnet"
      return 0
    fi
    # A second socket_vmnet, bridged to the interface, as a launchd service
    # (the one of brew services is the shared network). The plist goes in
    # base64 so the command is one line to paste.
    lbl=io.github.lima-vm.socket_vmnet.bridged.$ifc
    plist="<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">
<plist version=\"1.0\"><dict>
<key>Label</key><string>$lbl</string>
<key>ProgramArguments</key><array><string>$bp/opt/socket_vmnet/bin/socket_vmnet</string><string>--vmnet-mode=bridged</string><string>--vmnet-interface=$ifc</string><string>$sk</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>UserName</key><string>root</string>
</dict></plist>"
    b64=$(printf '%s\n' "$plist" | base64 | tr -d '\n')
    echo "hint=echo $b64 | base64 --decode | sudo tee /Library/LaunchDaemons/$lbl.plist >/dev/null && sudo launchctl bootstrap system /Library/LaunchDaemons/$lbl.plist"
    return 0
  fi
  helper=""
  for h in /usr/lib/qemu/qemu-bridge-helper /usr/libexec/qemu-bridge-helper /usr/lib/qemu-bridge-helper; do [ -x "$h" ] && helper=$h && break; done
  fix="sudo sh -c 'mkdir -p /etc/qemu && (grep -qs \"^allow $ifc\" /etc/qemu/bridge.conf || echo \"allow $ifc\" >> /etc/qemu/bridge.conf)"
  [ -n "$helper" ] && fix="$fix && chmod u+s $helper"
  fix="$fix'"
  if command -v ip >/dev/null 2>&1 && ip -d link show "$ifc" 2>/dev/null | grep -q ' bridge '; then
    echo "mode=bridge"; echo "bridge=$ifc"; echo "gateway=$gw"; echo "prefix=${gw%.*}"
    bad=""
    if [ -z "$helper" ]; then echo "problem=não achei o qemu-bridge-helper (pacote qemu-system-common)"; bad=1
    elif [ ! -u "$helper" ]; then echo "problem=o qemu-bridge-helper precisa de permissão de administrador (setuid)"; bad=1; fi
    grep -qsE "^allow ($ifc|all)" /etc/qemu/bridge.conf || { echo "problem=falta 'allow $ifc' em /etc/qemu/bridge.conf"; bad=1; }
    [ -n "$bad" ] && printf "%s\n" "hint=$fix"
    return 0
  fi
  echo "mode=none"
  echo "problem=$ifc não é uma bridge: no Linux, o modo bridge usa uma bridge com a placa de rede (por exemplo br0, criada no NetworkManager ou no netplan)"
  printf "%s\n" "hint=$fix"
}

# net_set writes $ROOT/rede.conf: GV_NET_MODE=bridge with GV_NET_IFACE,
# GV_NET_GW and the range GV_NET_FIRST..GV_NET_LAST, or shared (the file
# goes away).
net_set() {
  mkdir -p "$ROOT"
  if [ "${GV_NET_MODE:-}" != bridge ]; then
    rm -f "$ROOT/rede.conf"
    say "rede de VMs: compartilhada"
    return 0
  fi
  ifc=${GV_NET_IFACE:-}; gw=${GV_NET_GW:-}; a=${GV_NET_FIRST:-}; b=${GV_NET_LAST:-}
  printf '%s' "$ifc" | grep -Eq '^[A-Za-z0-9._-]{1,15}$' || die "interface inválida: $ifc"
  printf '%s' "$gw" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$' || die "gateway inválido: $gw"
  printf '%s%s' "$a" "$b" | grep -Eq '^[0-9]+$' || die "faixa inválida: $a a $b"
  [ "$a" -ge 2 ] && [ "$b" -le 254 ] && [ "$a" -le "$b" ] || die "faixa inválida: $a a $b (de 2 a 254)"
  printf 'modo=bridge\ninterface=%s\ngateway=%s\ninicio=%s\nfim=%s\n' "$ifc" "$gw" "$a" "$b" >"$ROOT/rede.conf.tmp"
  mv "$ROOT/rede.conf.tmp" "$ROOT/rede.conf"
  say "rede de VMs: bridge em $ifc, endereços ${gw%.*}.$a a ${gw%.*}.$b"
}

# addr_taken says whether an address is in use on the network (a device the
# router gave it to answers a ping).
# The machine's own addresses and the gateway count as taken.
addr_taken() {
  [ "$1" = "$(conf_get gateway)" ] && return 0
  if [ "$(uname -s)" = Darwin ]; then
    ifconfig 2>/dev/null | awk '$1 == "inet" {print $2}' | grep -qx "$1" && return 0
    ping -c 1 -t 1 "$1" >/dev/null 2>&1
  else
    { ip -4 -o addr 2>/dev/null | awk '{split($4, a, "/"); print a[1]}'; hostname -I 2>/dev/null | tr ' ' '\n'; } | grep -qx "$1" && return 0
    ping -c 1 -W 1 "$1" >/dev/null 2>&1
  fi
}

net_get() { netinfo | sed -n "s/^$1=//p" | head -1; }

# lan_ip gives the VM its fixed address on the VM network (.100 to .250, or
# the range of the bridge mode), keeping it in $ROOT/lan.ips across
# recreations. An address of another network (the mode changed) or out of
# the range is given again; in the bridge mode, an address that answers on
# the network (a device of the router) is skipped.
lan_ip() {
  info=$(netinfo)
  pre=$(printf '%s\n' "$info" | sed -n 's/^prefix=//p' | head -1)
  first=$(printf '%s\n' "$info" | sed -n 's/^first=//p' | head -1)
  last=$(printf '%s\n' "$info" | sed -n 's/^last=//p' | head -1)
  bridged=$(printf '%s\n' "$info" | sed -n 's/^net=//p' | head -1)
  first=${first:-100}; last=${last:-250}
  [ -n "$pre" ] || die "esta máquina não tem rede de VMs. $(printf '%s\n' "$info" | sed -n 's/^hint=//p' | head -1)"
  mkdir -p "$ROOT"
  i=0
  while ! mkdir "$ROOT/lan.lock" 2>/dev/null; do
    i=$((i + 1)); [ $i -gt 50 ] && rm -rf "$ROOT/lan.lock"; sleep 0.1
  done
  touch "$ROOT/lan.ips"
  ip=$(awk -v k="$P/$N" '$1 == k {print $2; exit}' "$ROOT/lan.ips")
  if [ -n "$ip" ]; then
    n=${ip##*.}
    if [ "${ip%.*}" != "$pre" ] || [ "$n" -lt "$first" ] || [ "$n" -gt "$last" ]; then
      awk -v k="$P/$N" '$1 != k' "$ROOT/lan.ips" >"$ROOT/lan.ips.tmp" && mv "$ROOT/lan.ips.tmp" "$ROOT/lan.ips"
      ip=""
    fi
  fi
  if [ -z "$ip" ]; then
    n=$first
    while [ $n -le "$last" ]; do
      if ! grep -q " $pre\.$n\$" "$ROOT/lan.ips"; then
        [ "$bridged" = bridge ] && addr_taken "$pre.$n" || break
      fi
      n=$((n + 1))
    done
    [ $n -le "$last" ] || { rmdir "$ROOT/lan.lock"; die "a rede de VMs está cheia (de .$first a .$last)"; }
    ip=$pre.$n
    echo "$P/$N $ip" >>"$ROOT/lan.ips"
  fi
  rmdir "$ROOT/lan.lock"
  echo "$ip"
}

lan_free() {
  [ -f "$ROOT/lan.ips" ] || return 0
  awk -v k="$P/$N" '$1 != k' "$ROOT/lan.ips" >"$ROOT/lan.ips.tmp" && mv "$ROOT/lan.ips.tmp" "$ROOT/lan.ips"
}

start_vm() {
  [ -f "$D/start.sh" ] || die "a VM $N ainda não foi criada; use Subir"
  if alive "$D"; then say "$N: já está rodando"; return 0; fi
  rm -f "$D/qemu.pid" "$D/mon.sock"
  : >"$D/console.log"
  # An ISO VM boots the installer while its disk is still (almost) empty.
  BOOT=order=c
  if [ -n "$(sed -n 's/^vnc=//p' "$D/vm.env" 2>/dev/null)" ] && [ "$(du -k "$D/disk.qcow2" | cut -f1)" -lt 102400 ]; then BOOT=order=c,once=d; fi
  export BOOT
  sh "$D/start.sh" 2>"$D/qemu.err" || die "o QEMU não iniciou: $(tail -n 3 "$D/qemu.err")"
  i=0
  while [ $i -lt 20 ] && [ ! -f "$D/qemu.pid" ]; do sleep 0.2; i=$((i + 1)); done
  alive "$D" || die "o QEMU parou logo depois de iniciar: $(tail -n 3 "$D/qemu.err")"
  links_down
  say "$N: iniciada (pid $(cat "$D/qemu.pid"))"
}

stop_vm() {
  alive "$D" || { say "$N: já está parada"; rm -f "$D/qemu.pid"; return 0; }
  pid=$(cat "$D/qemu.pid")
  if isprog "$D"; then # IOL, Dynamips: no ACPI to ask, the program just stops
    kill "$pid" 2>/dev/null
    i=0; while [ $i -lt 10 ] && kill -0 "$pid" 2>/dev/null; do sleep 0.5; i=$((i + 1)); done
    kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
    rm -f "$D/qemu.pid"
    say "$N: desligado"
    return 0
  fi
  mon system_powerdown || true
  i=0
  while [ $i -lt "${GV_TIMEOUT:-45}" ] && kill -0 "$pid" 2>/dev/null; do sleep 1; i=$((i + 1)); done
  if kill -0 "$pid" 2>/dev/null; then
    mon quit || kill "$pid" 2>/dev/null
    sleep 1
    kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
    say "$N: desligada à força (não respondeu ao desligamento)"
  else
    say "$N: desligada"
  fi
  rm -f "$D/qemu.pid" "$D/mon.sock"
}

up() {
  [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"
  case "${GV_KIND:-}" in iol | dyn | vpcs) ;; *) need_qemu ;; esac # IOL, Dynamips and VPCS are programs, not QEMU VMs
  mkdir -p "$D"
  if alive "$D"; then
    if [ "$(cat "$D/spec.hash" 2>/dev/null)" != "${GV_HASH:-}" ]; then
      if [ -n "${GV_NIXHASH:-}" ] && [ "$(cat "$D/base.hash" 2>/dev/null)" = "${GV_BASEHASH:-}" ]; then
        say "$N: a configuração Nix mudou; use Aplicar na página do projeto (sem reiniciar a VM)"
      else
        say "$N: rodando com a definição antiga; Reiniciar aplica CPU, memória e portas (pacotes e cloud-init só valem ao recriar a VM)"
      fi
    else
      say "$N: já está rodando"
    fi
    return 0
  fi
  if [ "${GV_KIND:-}" = iol ]; then iol_up; return; fi
  if [ "${GV_KIND:-}" = dyn ]; then dyn_up; return; fi
  if [ "${GV_KIND:-}" = vpcs ]; then vpcs_up; return; fi
  extra=""
  fresh=""
  [ -f "$D/disk.qcow2" ] || fresh=1
  if [ -n "${GV_ISO:-}" ]; then
    # Installer: blank disk, boot the ISO once, screen over VNC with a password.
    iso=$(iso_file) || exit 1
    if [ ! -f "$D/disk.qcow2" ]; then
      if [ -n "${GV_IMAGE:-}" ]; then base=$(base_image) || exit 1
        qemu-img create -q -f qcow2 -b "$base" -F qcow2 "$D/disk.qcow2" "${GV_DISK:-10G}" || die "não consegui criar o disco"
      else qemu-img create -q -f qcow2 "$D/disk.qcow2" "${GV_DISK:-10G}" || die "não consegui criar o disco"; fi
      say "$N: disco vazio criado (${GV_DISK:-10G}); a VM inicia pelo instalador $(basename "$iso")"
    fi
    [ -s "$D/vnc.pass" ] || { od -An -N4 -tx1 /dev/urandom | tr -d ' \n' >"$D/vnc.pass"; chmod 600 "$D/vnc.pass"; }
    disp=$((GV_VNC - 5900))
    extra="-drive 'file=$iso,media=cdrom,readonly=on' -object 'secret,id=vncpw,file=$D/vnc.pass' -vnc '127.0.0.1:$disp,password-secret=vncpw'"
    [ "$ARCH" = aarch64 ] && extra="$extra -device virtio-gpu-pci -device qemu-xhci -device usb-kbd -device usb-tablet"
  else
    base=$(base_image) || exit 1
    if [ ! -f "$D/disk.qcow2" ]; then
      qemu-img create -q -f qcow2 -b "$base" -F qcow2 "$D/disk.qcow2" "${GV_DISK:-10G}" || die "não consegui criar o disco"
      say "$N: disco criado (${GV_DISK:-10G}, a partir de $(basename "$base"))"
    fi
    # cloud-init: false (network appliances): no seed, no gantry user.
    if [ "${GV_NOCI:-}" != 1 ]; then
      t=$D/seed
      write_seed "$t" || exit 1
      make_iso "$t" "$D/seed.iso" || exit 1
      extra="-drive 'if=virtio,file=$D/seed.iso,format=raw,readonly=on'"
    else
      rm -rf "$D/seed" "$D/seed.iso"
    fi
    # tela: true — the screen over VNC, for systems without cloud-init.
    if [ -n "${GV_VNC:-}" ]; then
      [ -s "$D/vnc.pass" ] || { od -An -N4 -tx1 /dev/urandom | tr -d ' \n' >"$D/vnc.pass"; chmod 600 "$D/vnc.pass"; }
      disp=$((GV_VNC - 5900))
      extra="$extra -object 'secret,id=vncpw,file=$D/vnc.pass' -vnc '127.0.0.1:$disp,password-secret=vncpw'"
      [ "$ARCH" = aarch64 ] && extra="$extra -device virtio-gpu-pci -device qemu-xhci -device usb-kbd -device usb-tablet"
    fi
  fi

  acc=$(accel)
  cpu=host; [ "$acc" = tcg ] && cpu=max
  fwd="hostfwd=tcp:127.0.0.1:${GV_SSH}-:22"
  for p in ${GV_PORTS:-}; do # proto:ip:host:guest
    proto=${p%%:*}; rest=${p#*:}; ip=${rest%%:*}; rest=${rest#*:}; hp=${rest%%:*}; gp=${rest#*:}
    fwd="$fwd,hostfwd=$proto:$ip:$hp-:$gp"
  done
  m=""
  if [ "$ARCH" = aarch64 ]; then m="-machine virt -bios '$(firmware)'"
  elif [ "${GV_EFI:-}" = 1 ]; then fw=$(firmware_x86) || exit 1; m="-machine q35 -drive 'if=pflash,format=raw,readonly=on,file=$fw'"
  else m="-machine ${GV_MACHINE:-q35}"; fi
  nic=${GV_NIC:-virtio-net-pci}
  # The management NIC: internet, the SSH of Gantry and the published
  # ports. management: false leaves it out (the appliance's first port is
  # then its first link).
  mgmt=""; ssh=""
  if [ "${GV_NOMGMT:-}" != 1 ]; then
    mgmt="-netdev 'user,id=n0,$fwd' -device $nic,netdev=n0,mac=52:54:00:12:34:56"
    ssh=$GV_SSH
  fi
  # The extra interfaces (interfaces:): a UDP cable to the other end of a
  # link (127.0.0.1, either side may start first) or a bridge of the machine.
  ifs=""; i=0
  for f in ${GV_IFACES:-}; do
    kind=${f%%:*}; rest=${f#*:}
    case "$kind" in
      relay) rp=${rest%%:*}; rest=${rest#*:}; vp=${rest%%:*}; mac=${rest#*:}
        # The VM's socket on vp talks to the relay's socket rp; the relay
        # sends it to the other end of the cable (see relay below).
        ifs="$ifs -netdev socket,id=if$i,udp=127.0.0.1:$rp,localaddr=127.0.0.1:$vp -device $nic,netdev=if$i,mac=$mac,id=nic$i" ;;
      bridge) br=${rest%%:*}; mac=${rest#*:}
        if [ "$(uname -s)" = Darwin ]; then die "$N: interfaces com bridge: só no Linux (no macOS use link:)"; fi
        ifs="$ifs -netdev bridge,id=if$i,br=$br -device $nic,netdev=if$i,mac=$mac,id=nic$i" ;;
    esac
    i=$((i + 1))
  done
  # The serial console: a TCP socket on 127.0.0.1 for the interactive
  # Console, and everything it prints in console.log too.
  if [ -n "${GV_SERIAL:-}" ]; then
    serial="-chardev 'socket,id=ser0,host=127.0.0.1,port=$GV_SERIAL,server=on,wait=off,logfile=$D/console.log' -serial chardev:ser0"
  else
    serial="-serial 'file:$D/console.log'"
  fi
  # The project network: a second NIC, on socket_vmnet or the gantry0 bridge.
  pre=""; lan=""; lanip=""
  if [ "${GV_LAN:-}" = 1 ]; then
    mode=$(net_get mode)
    pr=$(netinfo | sed -n 's/^problem=//p' | head -1)
    [ "$mode" != none ] || die "$N usa a rede de VMs (lan: true), mas esta máquina não tem uma. Ative com: $(net_get hint)"
    [ -z "$pr" ] || die "rede de VMs: $pr"
    lanip=${GV_LANIP:-}
    [ -n "$lanip" ] || lanip=$(lan_ip) || exit 1
    dev="-device $nic,netdev=lan,mac=${GV_LANMAC:-52:54:01:00:00:01}"
    if [ "$mode" = vmnet ]; then
      pre="'$(net_get client)' '$(net_get sock)' "
      lan="-netdev socket,id=lan,fd=3 $dev"
    else
      lan="-netdev bridge,id=lan,br=$(net_get bridge) $dev"
    fi
  fi
  cat >"$D/start.sh" <<EOS
exec $pre qemu-system-$ARCH -name 'gantry-$P-$N' $m -accel $acc -cpu $cpu -smp ${GV_CPUS:-1} -m ${GV_MEM:-1024} \\
  -drive 'if=virtio,file=$D/disk.qcow2,format=qcow2' $extra -boot "\${BOOT:-order=c}" \\
  $mgmt $lan $ifs -device virtio-rng-pci \\
  -display none $serial -monitor 'unix:$D/mon.sock,server,nowait' \\
  -daemonize -pidfile '$D/qemu.pid'
EOS
  printf '%s\n' "${GV_HASH:-}" >"$D/spec.hash"
  printf '%s\n' "${GV_BASEHASH:-}" >"$D/base.hash"
  # nix.hash is the Nix configuration the VM has: the one of the seed when
  # the disk is created, then the one of each Aplicar that worked (spec-set).
  # Starting an existing disk keeps it: cloud-init does not deliver the files
  # again, so the VM still has the old ones.
  if [ -z "${GV_NIXHASH:-}" ]; then rm -f "$D/nix.hash"
  elif [ -n "$fresh" ] || [ ! -f "$D/nix.hash" ]; then printf '%s\n' "$GV_NIXHASH" >"$D/nix.hash"; fi
  ci=1; [ "${GV_NOCI:-}" = 1 ] && ci=0
  printf 'ssh=%s\nports=%s\nimage=%s\ncpus=%s\nmem=%s\naccel=%s\nvnc=%s\nlan=%s\nserial=%s\nci=%s\n' "$ssh" "${GV_PORTS:-}" "${GV_IMAGE:-${GV_ISO:-}}" "${GV_CPUS:-1}" "${GV_MEM:-1024}" "$acc" "${GV_VNC:-}" "$lanip" "${GV_SERIAL:-}" "$ci" >"$D/vm.env"
  [ "$acc" = tcg ] && say "$N: aviso: sem aceleração (KVM/HVF), a VM vai ficar lenta"
  printf '%s\n' "${GV_DOWN:-}" >"$D/down"
  start_vm
}

# --- IOL (Cisco IOS on Linux): a Linux program, not a VM ---
#
# Each node runs in its folder (its NVRAM stays there) under a small python3
# runner that gives it a terminal, writes what it prints to console.log and
# serves the console on 127.0.0.1:GV_SERIAL, like the serial of the VMs. Its
# ports are in the NETMAP, all pointing to the cables' relay of the project,
# which is the other end of every cable.

# iol_elfclass prints 32 or 64, the ELF class of a binary.
iol_elfclass() {
  case "$(od -An -tx1 -j4 -N1 "$1" 2>/dev/null | tr -d ' ')" in
  01) echo 32 ;;
  02) echo 64 ;;
  esac
}

# iol_libcrypto finds a libcrypto of the system with the ELF class of the
# IOL binary, the oldest OpenSSL first (closest to what IOL was built with).
iol_libcrypto() {
  want=$(iol_elfclass "$1")
  if [ "$want" = 32 ]; then
    dirs=${GV_IOLLIBDIRS:-/usr/lib/i386-linux-gnu /lib/i386-linux-gnu /usr/lib32 /lib32 /usr/lib /lib}
  else
    dirs=${GV_IOLLIBDIRS:-/usr/lib/x86_64-linux-gnu /lib/x86_64-linux-gnu /usr/lib64 /lib64}
  fi
  for v in 1.0.0 10 1.0.2 1.1 3; do
    for d in $dirs; do
      f=$d/libcrypto.so.$v
      [ -e "$f" ] || continue
      r=$(readlink -f "$f") || continue
      [ "$(iol_elfclass "$r")" = "$want" ] && { echo "$r"; return 0; }
    done
  done
  return 1
}

iol_runner_py() {
  cat <<'PY'
import os, pty, select, signal, socket, subprocess, sys, tty
d, cport = sys.argv[1:3]
cmd = sys.argv[3:]
os.chdir(d)
master, slave = pty.openpty()
tty.setraw(slave)
env = dict(os.environ)
if os.path.isfile(os.path.join(d, "iourc")):
    env["IOURC"] = os.path.join(d, "iourc")
child = subprocess.Popen(cmd, stdin=slave, stdout=slave, stderr=slave, cwd=d, env=env, start_new_session=True)
os.close(slave)
with open("qemu.pid", "w") as f:
    f.write(str(child.pid))
log = open("console.log", "ab", buffering=0)
srv = socket.socket()
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", int(cport)))
srv.listen(1)
client = None
signal.signal(signal.SIGTERM, lambda *_: child.terminate())
# Startup configuration (x-vms config:), on the first boot only: the runner
# answers the setup dialog and types the lines at the console, each after
# the prompt comes back, then saves (write memory).
import re, time
cfg = None
if os.path.isfile("startup.cfg") and not os.path.exists("startup.done"):
    with open("startup.cfg", encoding="utf-8", errors="replace") as f:
        cfg = [l.rstrip() for l in f.read().splitlines()]
    cfg = [l for l in cfg if l.strip() and not l.lstrip().startswith("!")]
    plan = ["", "enable", "terminal length 0", "configure terminal"] + cfg + ["end", "write memory"]
tail = b""
seen = False
step = 0
sent_at = 0.0
last_out = time.time()
prompt = re.compile(rb"[\w.()/-]+[>#]\s*$")
def feed():
    global step, sent_at, tail
    if cfg is None or step > len(plan):
        return
    now = time.time()
    if step == len(plan):
        if b"[OK]" in tail or (prompt.search(tail) and now - sent_at > 3):
            open("startup.done", "w").close()
            step += 1
        return
    if re.search(rb"initial configuration dialog\?.*$", tail[-200:]) and b"[yes/no]" in tail[-60:]:
        os.write(master, b"no\r")
        tail = b""
        sent_at = now
        return
    if b"terminate autoinstall?" in tail[-80:]:
        os.write(master, b"yes\r")
        tail = b""
        return
    if step == 0:
        # booted: it asks for RETURN, or it went quiet
        if (b"Press RETURN to get started" in tail and now - last_out > 1) or (seen and now - last_out > 6):
            os.write(master, b"\r")
            tail = b""
            sent_at = now
            step = 1
        return
    ready = prompt.search(tail) and now - last_out > 0.3
    if step < 4:
        # Until configure terminal, only on a real prompt (IOS may still be
        # booting); a RETURN now and then brings the prompt back.
        if not ready:
            if now - sent_at > 15:
                os.write(master, b"\r")
                sent_at = now
            return
    else:
        # In configuration: the prompt came back, or an async message
        # (%LINK…) came after it, or nothing came back for a while.
        ready = ready or (now - last_out > 2 and now - sent_at > 1) or now - sent_at > 20
    if ready and now - sent_at > 0.2:
        os.write(master, plan[step].encode() + b"\r")
        tail = b""
        sent_at = now
        step += 1
# Lines IOS refused while it read the startup configuration (at boot, or
# typed above): "% Invalid input…" after the line; kept in boot-rejected.txt
# as "line<TAB>message", one per line.
REJ = "boot-rejected.txt"
try:
    os.remove(REJ)
except OSError:
    pass
booting = True
linebuf = b""
recent = []
errline = re.compile(r"^% ?(Invalid input detected|Incomplete command|Ambiguous command|Unrecognized command|Invalid command)")
promptpfx = re.compile(r"^[\w.-]+(\([^)]*\))?[>#]")
def scan(data):
    global linebuf, booting, recent
    linebuf += data
    parts = linebuf.split(b"\n")
    linebuf = parts.pop()[-4096:]
    for raw in parts:
        l = raw.decode("utf-8", "replace").replace("\r", "").rstrip()
        s = l.strip()
        if not s:
            continue
        if (booting or (cfg is not None and step <= len(plan))) and errline.match(s):
            cmd = ""
            for prev in reversed(recent):
                if prev.strip().strip("^ ") == "":
                    continue
                cmd = promptpfx.sub("", prev.strip()).strip()
                break
            with open(REJ, "a", encoding="utf-8") as f:
                f.write(cmd.replace("\t", " ") + "\t" + s + "\n")
        recent = (recent + [l])[-3:]
        if "Press RETURN to get started" in s:
            booting = False
while child.poll() is None:
    fds = [master, srv] + ([client] if client else [])
    try:
        ready, _, _ = select.select(fds, [], [], 0.2 if cfg is not None and step <= len(plan) else 1)
    except InterruptedError:
        continue
    if master in ready:
        try:
            data = os.read(master, 4096)
        except OSError:
            break
        log.write(data)
        scan(data)
        tail = (tail + data)[-4096:]
        last_out = time.time()
        seen = True
        if client:
            try:
                client.sendall(data)
            except OSError:
                client.close()
                client = None
    if srv in ready:
        c, _ = srv.accept()
        if client:
            client.close()
        client = c
    feed()
    if client and client in ready:
        try:
            data = client.recv(4096)
        except OSError:  # a terminal closed abruptly: the node goes on
            data = b""
        if not data:
            client.close()
            client = None
        else:
            os.write(master, data)
child.wait()
PY
}

# iol_nvram_py writes an IOL NVRAM file holding a startup configuration, so
# the node boots with it exactly as saved (show running-config of another
# lab, PNETLab, GNS3), instead of having it typed at the console.
# Layout of the configuration area (the first half of the file, big
# endian): a 36-byte header (magic 0xABCD, format 1 = raw, checksum, IOS
# version, start and end address, length), the text padded to 4 bytes, then
# a 16-byte header of an empty private configuration (magic 0xFEDC). The
# checksum is the one's complement sum of the 16-bit words of the area.
iol_nvram_py() {
  cat <<'PY'
import struct, sys
out, cfg, kb = sys.argv[1], sys.argv[2], int(sys.argv[3])
text = open(cfg, "rb").read().replace(b"\r\n", b"\n")
if not text.endswith(b"\n"):
    text += b"\n"
text += b"\n" * (-len(text) % 4)
area = kb * 1024 // 2
base, ios = 0x10000000, 0x0F04
hdr = struct.pack(">HHHHIII", 0xABCD, 1, 0, ios, base + 36, base + 36 + len(text), len(text)).ljust(36, b"\0")
priv_at = 36 + len(text)
priv = struct.pack(">HHIII", 0xFEDC, 1, base + priv_at + 16, base + priv_at + 16, 0)
data = bytearray(hdr + text + priv)
if len(data) > area - 2048:
    sys.exit("configuração grande demais para a NVRAM (%d KB)" % kb)
data += bytes(area - len(data))
s = sum(struct.unpack(">%dH" % (area // 2), bytes(data)))
while s >> 16:
    s = (s & 0xFFFF) + (s >> 16)
struct.pack_into(">H", data, 4, s ^ 0xFFFF)
open(out, "wb").write(bytes(data) + bytes(kb * 1024 - area))
PY
}

iol_up() {
  [ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || die "$N: o IOL (IOS on Linux) só roda em Linux x86_64; use um host Linux (ssh://) para este nó"
  command -v python3 >/dev/null 2>&1 || die "o IOL precisa do python3 nesta máquina"
  bin=${GV_IMAGE_SRC:-}
  # shellcheck disable=SC2088
  case "$bin" in "~/"*) bin=$HOME/${bin#"~/"} ;; esac
  [ -n "$bin" ] || die "a imagem $GV_IMAGE ainda não foi configurada nesta máquina; ponha o arquivo numa das pastas de imagens (página da máquina › Configurações › Imagens)"
  [ -f "$bin" ] || die "a imagem do IOL não está em $bin"
  plain "$bin" "o caminho da imagem"
  if [ ! -x "$bin" ]; then
    # Not executable (often the case in /opt/unetlab, owned by root): an
    # executable copy, kept once per file, is what runs.
    [ -r "$bin" ] || die "não consigo ler $bin; dê permissão de leitura a este usuário (chmod a+r)"
    c=$ROOT/.iol-bin/$(basename "$bin")
    if [ ! -x "$c" ] || [ "$(wc -c <"$bin")" != "$(wc -c <"$c")" ] || [ "$bin" -nt "$c" ]; then
      mkdir -p "$ROOT/.iol-bin" && cp "$bin" "$c.tmp" && chmod +x "$c.tmp" && mv -f "$c.tmp" "$c" || die "não consegui fazer uma cópia executável de $bin em $ROOT/.iol-bin"
    fi
    bin=$c
  fi
  missing=$(ldd "$bin" 2>/dev/null | awk '/not found/ { print $1 }' | tr '\n' ' ')
  iol_ldp=
  case " $missing" in
  *" libcrypto.so.4 "*)
    # The older IOL images (i86bi_linux) ask for libcrypto.so.4, the name
    # of an OpenSSL from 2005; the libcrypto of the system does what they
    # use. A link with that name in a folder of Gantry, no root needed.
    if lc=$(iol_libcrypto "$bin"); then
      mkdir -p "$ROOT/.iol-lib" && ln -sfn "$lc" "$ROOT/.iol-lib/libcrypto.so.4" || die "não consegui criar $ROOT/.iol-lib"
      iol_ldp=$ROOT/.iol-lib
      LD_LIBRARY_PATH=$iol_ldp${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}
      export LD_LIBRARY_PATH
      missing=$(ldd "$bin" 2>/dev/null | awk '/not found/ { print $1 }' | tr '\n' ' ')
    fi
    ;;
  esac
  if [ -n "$missing" ]; then
    case "$(iol_elfclass "$bin")" in
    32) die "faltam bibliotecas de 32 bits para o IOL nesta máquina: $missing(Fedora: sudo dnf install glibc.i686 libstdc++.i686 openssl-libs.i686; Debian/Ubuntu: sudo dpkg --add-architecture i386 && sudo apt update && sudo apt install libc6:i386 libstdc++6:i386 libssl3:i386)" ;;
    *) die "faltam bibliotecas para o IOL nesta máquina: $missing(instale os pacotes que as trazem)" ;;
    esac
  fi
  # IOL checks its own image in the folder it runs in (else IOL-L2 says
  # SIGNATURE_FAILED and turns Ethernet switching off): it runs from the
  # node's folder, through a link with the image's name.
  img=$(basename "$bin")
  rm -f "$D/$img"; ln -s "$bin" "$D/$img" || die "não consegui ligar a imagem em $D"
  for lnk in "$D"/*; do
    [ -L "$lnk" ] && [ "$lnk" != "$D/$img" ] && case "$(basename "$lnk")" in *.bin | i86bi* | *.iol | *crb_linux*) rm -f "$lnk" ;; esac
  done
  mkdir -p "/tmp/netio$(id -u)"
  printf '%s' "${GV_NETMAP:-}" | b64d >"$D/NETMAP"
  if [ -n "${GV_IOURC:-}" ]; then
    lic=$GV_IOURC
    # shellcheck disable=SC2088
    case "$lic" in "~/"*) lic=$HOME/${lic#"~/"} ;; esac
    cp "$lic" "$D/iourc" 2>/dev/null || die "não consegui ler a licença do IOL em $lic"
  else
    rm -f "$D/iourc"
  fi
  # The NVRAM is named by the instance number; a node carried from another
  # machine (or renumbered here) keeps its configuration under the new one.
  want=$(printf '%05d' "${GV_IOLID:-1}")
  for pre in nvram_ vlan.dat-; do
    set -- "$D"/"$pre"*
    if [ $# = 1 ] && [ -f "$1" ] && [ "$(basename "$1")" != "$pre$want" ]; then mv "$1" "$D/$pre$want"; fi
  done
  : >"$D/iol"
  # The startup configuration goes in on the first boot (no NVRAM yet): a
  # node already configured keeps what it saved. It is written as the
  # NVRAM, the size this image uses (its -h says the default); an image
  # that does not say has it typed at the console instead (startup.cfg).
  if [ -n "${GV_CONFIG:-}" ] && [ ! -f "$D/nvram_$want" ] && [ ! -f "$D/startup.done" ]; then
    printf '%s' "$GV_CONFIG" | b64d >"$D/startup-config"
    kb=$(cd "$D" && timeout 10 "./$img" -h 2>&1 | sed -n 's/.*Size of nvram in Kb (default \([0-9][0-9]*\)KB).*/\1/p' | head -n 1)
    iol_nvram_py >"$D/iol_nvram.py"
    if [ -n "$kb" ] && python3 "$D/iol_nvram.py" "$D/nvram_$want" "$D/startup-config" "$kb" 2>"$D/nvram.err"; then
      rm -f "$D/startup.cfg"
      : >"$D/startup.done"
    else
      cp "$D/startup-config" "$D/startup.cfg"
    fi
  elif [ -z "${GV_CONFIG:-}" ]; then
    rm -f "$D/startup.cfg" "$D/startup-config"
  fi
  iol_runner_py >"$D/iol_runner.py"
  bays=$(((${GV_IOLPORTS:-4} + 3) / 4)); [ "$bays" -ge 1 ] || bays=1
  cat >"$D/start.sh" <<EOS
${iol_ldp:+LD_LIBRARY_PATH='$iol_ldp' }nohup python3 '$D/iol_runner.py' '$D' '${GV_SERIAL:-17000}' './$img' -e $bays -s 0 -m '${GV_MEM:-512}' '${GV_IOLID:-1}' >'$D/qemu.err' 2>&1 &
EOS
  printf '%s\n' "${GV_HASH:-}" >"$D/spec.hash"
  printf '%s\n' "${GV_BASEHASH:-}" >"$D/base.hash"
  printf 'ssh=\nports=\nimage=%s\ncpus=1\nmem=%s\naccel=iol\nvnc=\nlan=\nserial=%s\nci=0\n' "$GV_IMAGE" "${GV_MEM:-512}" "${GV_SERIAL:-}" >"$D/vm.env"
  printf '\n' >"$D/down"
  start_vm
}

# --- Dynamips: the classic routers (7200, 3725, 2691) run as a program,
# like IOL, under the same runner (console on GV_SERIAL, console.log). Its
# ports are UDP NIOs to the cables' relay (GV_DYNARGS); the idle-PC of the
# image, once found, keeps an idle router from using a whole core. ---

# dyn_port: the name of port $2 of model $1 (FastEthernet slot/port); the
# node's portnames (from its adapters) when there are.
dyn_port() {
  if [ -n "${3:-}" ] && [ -s "$3" ]; then
    n=$(awk -v i="$2" '{ print $(i + 1) }' "$3")
    [ -n "$n" ] && { echo "$n"; return; }
  fi
  if [ "$1" = 7200 ]; then
    if [ "$2" = 0 ]; then echo f0/0; else echo "f$((1 + ($2 - 1) / 2))/$((($2 - 1) % 2))"; fi
  elif [ "$2" -lt 2 ]; then echo "f0/$2"
  else echo "f$((1 + ($2 - 2) / 16))/$((($2 - 2) % 16))"; fi
}

idle_dir() { echo "$(dirname "$ROOT")/idlepc"; }
idle_file() { echo "$(idle_dir)/$(basename "$1" | tr -c 'A-Za-z0-9._\n-' _)"; }

dyn_up() {
  command -v dynamips >/dev/null 2>&1 || die "o Dynamips não está instalado nesta máquina (Fedora: sudo dnf install dynamips; Debian/Ubuntu: sudo apt install dynamips; Mac: brew install dynamips, ou o que vem com o GNS3)"
  command -v python3 >/dev/null 2>&1 || die "o Dynamips do Gantry precisa do python3 nesta máquina"
  bin=${GV_IMAGE_SRC:-}
  # shellcheck disable=SC2088
  case "$bin" in "~/"*) bin=$HOME/${bin#"~/"} ;; esac
  [ -n "$bin" ] || die "a imagem $GV_IMAGE ainda não foi configurada nesta máquina; ponha o arquivo do IOS numa das pastas de imagens (página da máquina › Configurações › Imagens)"
  [ -f "$bin" ] || die "a imagem do IOS não está em $bin"
  plain "$bin" "o caminho da imagem"
  plat=${GV_DYNPLAT:-7200}
  npe=""; [ "$plat" = 7200 ] && npe="-t ${GV_DYNNPE:-npe-400}"
  printf '%s\n%s\n' "$plat" "$bin" >"$D/dyn"
  printf '%s\n' "${GV_DYNPORTS:-}" >"$D/portnames"
  iol_runner_py >"$D/iol_runner.py"
  # The startup configuration: Dynamips puts it in the NVRAM itself (-C)
  # when the router has none yet; one already configured keeps its own.
  if [ -n "${GV_CONFIG:-}" ]; then
    printf '%s' "$GV_CONFIG" | b64d >"$D/startup-config"
  else
    rm -f "$D/startup-config"
  fi
  # start.sh reads the idle-PC each time: one found later counts at the next start.
  cat >"$D/start.sh" <<EOS
ip=\$(cat '$(idle_file "$bin")' 2>/dev/null)
cfg=; if [ -f '$D/startup-config' ] && ! ls '$D'/*_nvram >/dev/null 2>&1; then cfg='$D/startup-config'; fi
nohup python3 '$D/iol_runner.py' '$D' '${GV_SERIAL:-17000}' dynamips -P '$plat' $npe -r '${GV_MEM:-256}' -X \${ip:+--idle-pc=\$ip} \${cfg:+-C \$cfg} ${GV_DYNARGS:-} '$bin' >'$D/qemu.err' 2>&1 &
EOS
  printf '%s\n' "${GV_HASH:-}" >"$D/spec.hash"
  printf '%s\n' "${GV_BASEHASH:-}" >"$D/base.hash"
  printf 'ssh=\nports=\nimage=%s\ncpus=1\nmem=%s\naccel=dynamips\nvnc=\nlan=\nserial=%s\nci=0\n' "$GV_IMAGE" "${GV_MEM:-256}" "${GV_SERIAL:-}" >"$D/vm.env"
  printf '\n' >"$D/down"
  start_vm
}

# --- VPCS: the simple computer of PNETLab, EVE-NG and GNS3, a program
# under the same runner (console on GV_SERIAL); its one port a UDP NIO to
# the cables' relay (GV_VPCSARGS). Its configuration is startup.vpc in its
# folder: the one of the compose on the first start, then what "save" wrote.
# The program: GV_VPCSBIN (the one Gantry brings), else a vpcs of the machine. ---

vpcs_up() {
  command -v python3 >/dev/null 2>&1 || die "o VPCS do Gantry precisa do python3 nesta máquina"
  bin=${GV_VPCSBIN:-}
  # shellcheck disable=SC2088
  case "$bin" in "~/"*) bin=$HOME/${bin#"~/"} ;; esac
  [ -n "$bin" ] && [ -x "$bin" ] || bin=$(command -v vpcs 2>/dev/null) || die "o VPCS não está nesta máquina (o Gantry leva o dele para Linux x86_64; em outra arquitetura instale o vpcs)"
  : >"$D/vpcs"
  if [ -n "${GV_CONFIG:-}" ] && [ ! -f "$D/startup.vpc" ]; then
    printf '%s' "$GV_CONFIG" | b64d >"$D/startup.vpc"
  fi
  iol_runner_py >"$D/iol_runner.py"
  cat >"$D/start.sh" <<EOS
nohup python3 '$D/iol_runner.py' '$D' '${GV_SERIAL:-17000}' '$bin' -i 1 -R ${GV_VPCSARGS:-} startup.vpc >'$D/qemu.err' 2>&1 &
EOS
  printf '%s\n' "${GV_HASH:-}" >"$D/spec.hash"
  printf '%s\n' "${GV_BASEHASH:-}" >"$D/base.hash"
  printf 'ssh=\nports=\nimage=vpcs\ncpus=1\nmem=16\naccel=vpcs\nvnc=\nlan=\nserial=%s\nci=0\n' "${GV_SERIAL:-}" >"$D/vm.env"
  printf '\n' >"$D/down"
  start_vm
}

# dyn_idlepc finds the idle-PC of the node's image: with the router at its
# prompt, the console's Ctrl-] i makes Dynamips list the candidates; the
# one it suggests is kept for the image (GV_RESTART=1 restarts the node to
# use it now).
dyn_idlepc() {
  need_vm
  [ -f "$D/dyn" ] || die "$N não é um nó Dynamips"
  alive "$D" || die "inicie $N e espere o prompt do roteador antes de calcular o idle-PC"
  port=$(sed -n 's/^serial=//p' "$D/vm.env")
  bin=$(sed -n 2p "$D/dyn")
  val=$(python3 - "$port" <<'PY'
import re, socket, sys, time
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=5)
s.sendall(b"\r")
time.sleep(1)
s.sendall(b"\x1di")
out = b""
end = time.time() + 60
while time.time() < end and b"Restart the emulator" not in out and b"No idle PC" not in out:
    s.settimeout(max(0.1, end - time.time()))
    try:
        d = s.recv(4096)
    except socket.timeout:
        break
    if not d:
        break
    out += d
text = out.decode("latin-1")
best, score = "", None
for m in re.finditer(r"(0x[0-9a-fA-F]{8})\s*[\[(](?:count=)?(\d+)[\])]", text):
    pc, n = m.group(1), int(m.group(2))
    d = abs(n - 50) if 20 <= n <= 70 else 1000 + abs(n - 50)
    if score is None or d < score:
        best, score = pc, d
if not best:
    m = re.search(r"--idle-pc=(0x[0-9a-fA-F]+)", text)
    best = m.group(1) if m else ""
print(best)
PY
) || die "$N: não consegui falar com o console"
  [ -n "$val" ] || die "$N: o Dynamips não sugeriu um idle-PC (espere o roteador chegar ao prompt, sem tráfego, e tente de novo)"
  mkdir -p "$(idle_dir)"
  printf '%s\n' "$val" >"$(idle_file "$bin")" || die "não consegui guardar o idle-PC em $(idle_dir)"
  say "$N: idle-PC $val guardado para $(basename "$bin")"
  if [ "${GV_RESTART:-}" = 1 ]; then stop_vm && start_vm; fi
  return 0
}

# links_down: the ports with no cable start with the link down, as an
# unplugged port of a real switch.
links_down() {
  for n in $(cat "$D/down" 2>/dev/null); do mon "set_link nic$n off" || true; done
}

# --- the relay of the cables (interfaces: of the VMs of a project) ---
#
# One small process per project, in python3, with a socket per port of the
# VMs; each line of relay.map is "relay-port vm-port peer vm n", the peer the
# relay port at the other end of the cable or seg:NAME, a lab network: a
# learning switch, with a small DHCP server when "#seg" gives it a pool
# (its leases go to dhcp.leases).
# What a VM sends to its relay port goes to the VM port of the peer. Given a
# new table it reloads (SIGHUP) without stopping, and the ports whose cable
# changed get their link taken down and brought back up through the monitor
# of their VMs.

relay_py() {
  cat <<'PY'
import os, select, signal, socket, struct, sys, time
path = sys.argv[1]
cap_list = os.path.join(os.path.dirname(path), "capture.list")
caps = {}       # relay port or seg:NAME -> [pcap files] (captures of the cable at that port, of the network)
socks = {}      # relay port -> UDP socket (the ports of QEMU VMs and containers)
bound = {}      # relay port -> the address its socket listens on
target = {}     # relay port -> ("udp", vm port), ("iol", instance, port byte) or ("ctr",)
ctr_at = {}     # relay port of a container -> its address (learned from its packets)
peer = {}       # relay port -> relay port at the other end (0: no cable) or "seg:NAME"
by_iol = {}     # (instance, port byte) -> relay port
who = {}        # relay port -> (node, port index)
segs = {}       # network -> its settings and MAC table
members = {}    # network -> relay ports on it
lease_file = os.path.join(os.path.dirname(path), "dhcp.leases")
leases = {}     # network -> {mac: (ip, node, port index)}
SRV_MAC = bytes.fromhex("02474e000001")  # the DHCP server of the networks
netio = "/tmp/netio%d" % os.getuid()
usock = [None, 0]  # the relay as an IOL instance (#rid), for IOL nodes
reload = [True]
signal.signal(signal.SIGHUP, lambda *_: reload.__setitem__(0, True))
def ipn(s):
    try:
        a = [int(x) for x in s.split(".")]
        return (a[0] << 24) | (a[1] << 16) | (a[2] << 8) | a[3] if len(a) == 4 else 0
    except ValueError:
        return 0
def ipb(n):
    return struct.pack("!I", n)
def ips(n):
    return "%d.%d.%d.%d" % (n >> 24, (n >> 16) & 255, (n >> 8) & 255, n & 255)
def load_leases():
    try:
        for line in open(lease_file):
            f = line.split()
            if len(f) >= 3:
                leases.setdefault(f[0], {})[bytes.fromhex(f[1].replace(":", ""))] = (ipn(f[2]), f[3] if len(f) > 3 else "-", f[4] if len(f) > 4 else "-")
    except (OSError, ValueError):
        pass
def save_leases():
    try:
        with open(lease_file + ".new", "w") as fh:
            for name, L in leases.items():
                for mac, (ip, node, n) in L.items():
                    fh.write("%s %s %s %s %s\n" % (name, mac.hex(":"), ips(ip), node, n))
        os.replace(lease_file + ".new", lease_file)
    except OSError:
        pass
load_leases()
def load():
    want, rid, seen, names = {}, 0, {}, {}
    try:
        for line in open(path):
            f = line.split()
            if len(f) >= 2 and f[0] == "#rid":
                rid = int(f[1])
            elif len(f) >= 8 and f[0] == "#seg":
                net, _, bits = f[2].partition("/")
                seen[f[1]] = {"net": ipn(net), "bits": int(bits or 0) if net != "-" else 0, "gw": ipn(f[3]),
                              "lo": ipn(f[4]), "hi": ipn(f[5]), "srv": ipn(f[6]),
                              "dns": [ipn(x) for x in f[7].split(",") if x != "-"]}
            elif len(f) >= 3 and not f[0].startswith("#"):
                p = f[2] if f[2].startswith("seg:") else int(f[2])
                if f[1].startswith("iol:"):
                    _, inst, port = f[1].split(":")
                    want[int(f[0])] = (("iol", int(inst), int(port)), p)
                elif f[1] == "ctr":
                    want[int(f[0])] = (("ctr",), p)
                else:
                    want[int(f[0])] = (("udp", int(f[1])), p)
                names[int(f[0])] = (f[3] if len(f) > 3 else "-", f[4] if len(f) > 4 else "-")
    except OSError:
        return
    for name, S in seen.items():
        S["macs"] = segs[name]["macs"] if name in segs else {}
    segs.clear(); segs.update(seen)
    members.clear()
    for r, (t, p) in want.items():
        if isinstance(p, str):
            members.setdefault(p[4:], []).append(r)
    for name, S in segs.items():
        # A port that left the network takes its MACs with it.
        S["macs"] = {m: r for m, r in S["macs"].items() if r in members.get(name, [])}
    who.clear(); who.update(names)
    if rid and rid != usock[1]:
        if usock[0]:
            usock[0].close()
        os.makedirs(netio, exist_ok=True)
        sp = os.path.join(netio, str(rid))
        try:
            os.unlink(sp)
        except OSError:
            pass
        u = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
        u.bind(sp)
        u.setblocking(False)
        usock[0], usock[1] = u, rid
    def addr_of(t):
        # A container reaches the relay from its own network: the port
        # listens on every address (only private sources are accepted).
        return "0.0.0.0" if t[0] == "ctr" else "127.0.0.1"
    for r in list(socks):
        if r not in want or want[r][0][0] == "iol" or bound.get(r) != addr_of(want[r][0]):
            socks.pop(r).close()
            bound.pop(r, None)
            ctr_at.pop(r, None)
    target.clear(); peer.clear(); by_iol.clear()
    for r, (t, p) in want.items():
        target[r], peer[r] = t, p
        if t[0] == "iol":
            by_iol[(t[1], t[2])] = r
        elif r not in socks:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                s.bind((addr_of(t), r))
            except OSError as e:
                print("porta %d: %s" % (r, e), file=sys.stderr)
                continue
            s.setblocking(False)
            socks[r] = s
            bound[r] = addr_of(t)
def private(src):
    ip = src[0] if isinstance(src, tuple) else ""
    try:
        a = [int(x) for x in ip.split(".")]
    except ValueError:
        return False
    return len(a) == 4 and (a[0] in (10, 127) or (a[0] == 172 and 16 <= a[1] <= 31) or (a[0] == 192 and a[1] == 168) or (a[0] == 169 and a[1] == 254) or (a[0] == 100 and 64 <= a[1] <= 127))
def load_caps():
    want = {}
    try:
        for line in open(cap_list):
            f = line.split(None, 1)
            if len(f) == 2:
                # a relay port, or seg:NAME (everything that goes through a network)
                k = f[0] if f[0].startswith("seg:") else int(f[0])
                want.setdefault(k, []).append(f[1].strip())
    except (OSError, ValueError):
        pass
    have = {fh.name: fh for fs in caps.values() for fh in fs}
    caps.clear()
    for r, paths in want.items():
        for pth in paths:
            fh = have.pop(pth, None)
            if fh is None:
                try:
                    fh = open(pth, "ab")
                    if fh.tell() == 0:
                        # pcap: magic, version 2.4, no zone, sigfigs, snaplen, Ethernet
                        fh.write(struct.pack("<IHHiIII", 0xA1B2C3D4, 2, 4, 0, 0, 65535, 1))
                        fh.flush()
                except OSError as e:
                    print("captura %s: %s" % (pth, e), file=sys.stderr)
                    continue
            caps.setdefault(r, []).append(fh)
    for fh in have.values():
        fh.close()
def capture(r, p, data):
    write(caps.get(r, []) + (caps.get(p, []) if p != r else []), data)
def write(fs, data):
    if not fs:
        return
    now = time.time()
    rec = struct.pack("<IIII", int(now), int((now % 1) * 1e6), len(data), len(data)) + data
    for fh in fs:
        try:
            fh.write(rec)
            fh.flush()
        except OSError:
            pass
def deliver(r, data):
    p = peer.get(r, 0)
    if isinstance(p, str):
        seg_in(r, p[4:], data)
        return
    if not p or p not in target:
        return
    capture(r, p, data)
    send(p, data)
def seg_in(r, name, data):
    S = segs.get(name)
    if S is None or len(data) < 14:
        return
    write(caps.get(r, []) + caps.get("seg:" + name, []), data)
    dst, src = data[0:6], data[6:12]
    if not src[0] & 1:
        S["macs"][src] = r
    if S["srv"] and server_in(S, name, r, data):
        return
    if dst[0] & 1:
        outs = [q for q in members.get(name, []) if q != r]
    else:
        q = S["macs"].get(dst)
        if q is None:
            outs = [q for q in members.get(name, []) if q != r]
        else:
            outs = [q] if q != r else []
    for q in outs:
        write(caps.get(q, []), data)
        send(q, data)
def seg_out(S, name, r, frame):
    # A frame of the network's own server (DHCP, ARP) to the port r.
    write(caps.get(r, []) + caps.get("seg:" + name, []), frame)
    send(r, frame)
def csum(b):
    if len(b) % 2:
        b += b"\0"
    s = sum(struct.unpack("!%dH" % (len(b) // 2), b))
    while s >> 16:
        s = (s & 0xFFFF) + (s >> 16)
    return ~s & 0xFFFF
def server_in(S, name, r, data):
    # The network's DHCP server: it answers ARP for its address and DHCP
    # requests. True when the frame was for it alone.
    et = data[12:14]
    if et == b"\x08\x06" and len(data) >= 42:
        a = data[14:42]
        if a[6:8] == b"\0\x01" and struct.unpack("!I", a[24:28])[0] == S["srv"]:
            rep = a[0:6] + b"\0\x02" + SRV_MAC + ipb(S["srv"]) + a[8:14] + a[14:18]
            seg_out(S, name, r, a[8:14] + SRV_MAC + b"\x08\x06" + rep)
            return True
        return False
    if et != b"\x08\x00" or len(data) < 34:
        return False
    ihl = (data[14] & 15) * 4
    if data[23] != 17 or len(data) < 14 + ihl + 8 + 240:
        return data[0:6] == SRV_MAC
    u = 14 + ihl
    sport, dport = struct.unpack("!HH", data[u:u + 4])
    if dport != 67:
        return data[0:6] == SRV_MAC
    b = data[u + 8:]
    if b[0] != 1 or b[236:240] != b"\x63\x82\x53\x63":
        return True
    opts, i = {}, 240
    while i < len(b) and b[i] != 255:
        if b[i] == 0:
            i += 1
            continue
        if i + 1 >= len(b):
            break
        opts[b[i]] = b[i + 2:i + 2 + b[i + 1]]
        i += 2 + b[i + 1]
    mt = opts.get(53, b"\0")[0]
    if 54 in opts and len(opts[54]) == 4 and struct.unpack("!I", opts[54])[0] != S["srv"]:
        return False  # for another server of the network
    mac = b[28:34]
    L = leases.setdefault(name, {})
    ciaddr = struct.unpack("!I", b[12:16])[0]
    want = struct.unpack("!I", opts[50])[0] if len(opts.get(50, b"")) == 4 else ciaddr
    node = who.get(r, ("-", "-"))
    def free(ip):
        return S["lo"] <= ip <= S["hi"] and all(x[0] != ip for m, x in L.items() if m != mac)
    if mt == 1:  # DISCOVER
        ip = L[mac][0] if mac in L else (want if want and free(want) else 0)
        n = S["lo"]
        while not ip and n <= S["hi"]:
            if free(n):
                ip = n
            n += 1
        if ip:
            reply(S, name, r, b, 2, ip)
        return True
    if mt == 3:  # REQUEST
        if want and (L.get(mac, (0,))[0] == want or free(want)):
            L[mac] = (want, node[0], node[1])
            save_leases()
            reply(S, name, r, b, 5, want)
        elif 54 in opts or (mac in L and want != L[mac][0]):
            reply(S, name, r, b, 6, 0)
        return True
    if mt == 7 and mac in L:  # RELEASE
        del L[mac]
        save_leases()
    return True
def reply(S, name, r, req, mt, yi):
    srv = S["srv"]
    flags = struct.unpack("!H", req[10:12])[0]
    ciaddr = req[12:16]
    b = bytes([2, 1, 6, 0]) + req[4:8] + b"\0\0" + req[10:12] + (ciaddr if mt != 6 else b"\0" * 4)
    b += ipb(yi) + ipb(srv) + b"\0" * 4 + req[28:44] + b"\0" * 192 + b"\x63\x82\x53\x63"
    o = [(53, bytes([mt])), (54, ipb(srv))]
    if mt != 6:
        mask = (0xFFFFFFFF << (32 - S["bits"])) & 0xFFFFFFFF if S["bits"] else 0xFFFFFF00
        o += [(51, ipb(86400)), (58, ipb(43200)), (59, ipb(75600)), (1, ipb(mask))]
        if S["gw"]:
            o.append((3, ipb(S["gw"])))
        if S["dns"]:
            o.append((6, b"".join(ipb(x) for x in S["dns"])))
    for k, v in o:
        b += bytes([k, len(v)]) + v
    b += b"\xff"
    b += b"\0" * max(0, 300 - len(b))
    bcast = flags & 0x8000 or ciaddr == b"\0" * 4 or mt == 6
    dip = 0xFFFFFFFF if bcast else struct.unpack("!I", ciaddr)[0]
    udp = struct.pack("!HHHH", 67, 68, 8 + len(b), 0) + b
    ip = struct.pack("!BBHHHBBH4s4s", 0x45, 0, 20 + len(udp), 0, 0, 64, 17, 0, ipb(srv), ipb(dip))
    ip = ip[:10] + struct.pack("!H", csum(ip)) + ip[12:]
    dst = b"\xff" * 6 if bcast else req[28:34]
    seg_out(S, name, r, dst + SRV_MAC + b"\x08\x00" + ip + udp)
def send(p, data):
    t = target.get(p)
    if not t:
        return
    try:
        if t[0] == "udp" and p in socks:
            socks[p].sendto(data, ("127.0.0.1", t[1]))
        elif t[0] == "ctr" and p in socks and p in ctr_at:
            socks[p].sendto(data, ctr_at[p])
        elif t[0] == "iol" and usock[0]:
            # The IOL header: destination and source instance (16 bits, big
            # endian), destination and source port, type 1 (data), channel 0.
            hdr = struct.pack(">HHBBBB", t[1], usock[1], t[2], t[2], 1, 0)
            usock[0].sendto(hdr + data, os.path.join(netio, str(t[1])))
    except OSError:
        pass
while True:
    if reload[0]:
        reload[0] = False
        load()
        load_caps()
    fds = list(socks.values()) + ([usock[0]] if usock[0] else [])
    try:
        ready, _, _ = select.select(fds, [], [], 1)
    except (InterruptedError, ValueError, OSError):
        continue
    if reload[0]:
        # A HUP while waiting (select resumes after signals): the new table
        # and captures count before the frames that woke it.
        reload[0] = False
        load()
        load_caps()
    for s in ready:
        try:
            data, src = s.recvfrom(65535)
        except OSError:
            continue
        if s is usock[0]:
            if len(data) < 8:
                continue
            dst, src, dport, sport, mtype, _ = struct.unpack(">HHBBBB", data[:8])
            r = by_iol.get((src & 0x7FF, sport))
            if mtype == 1 and r is not None:
                deliver(r, data[8:])
        else:
            r = s.getsockname()[1]
            if target.get(r, ("",))[0] == "ctr":
                if not private(src):
                    continue
                ctr_at[r] = src
                if not data:
                    continue  # a keepalive: only to learn where the container is
            deliver(r, data)
PY
}

# Captures of a cable: the relay writes the frames of the port GV_PORT (both
# directions) to a pcap file, listed in capture.list.
capture_start() {
  R=$ROOT/$P
  # GV_PORT: a relay port, or seg:NAME for a whole lab network.
  case "${GV_PORT:-}" in
    seg:*) case "${GV_PORT#seg:}" in ''|*[!A-Za-z0-9_.-]*) die "rede inválida" ;; esac ;;
    ''|*[!0-9]*) die "porta inválida" ;;
  esac
  relay_alive "$R" || die "o relay dos cabos não está rodando; suba o projeto"
  mkdir -p "$R/capture"
  find "$R/capture" -name '*.pcap' -mmin +1440 -exec rm -f {} + 2>/dev/null
  f=$R/capture/$(printf '%s' "$GV_PORT" | tr ':' '-')-$(date +%Y%m%d-%H%M%S)-$$.pcap
  : >"$f"
  echo "$GV_PORT $f" >>"$R/capture.list"
  kill -HUP "$(cat "$R/relay.pid")"
  echo "$f"
}

capture_stop() {
  R=$ROOT/$P
  [ -f "$R/capture.list" ] || exit 0
  case "${GV_FILE:-}" in "$R"/capture/*.pcap) ;; *) die "arquivo de captura inválido" ;; esac
  grep -vF " $GV_FILE" "$R/capture.list" >"$R/capture.list.new" || true
  mv -f "$R/capture.list.new" "$R/capture.list"
  relay_alive "$R" && kill -HUP "$(cat "$R/relay.pid")"
  [ "${GV_DELETE:-}" = 1 ] && rm -f "$GV_FILE"
  true
}

# The addresses the DHCP of the lab networks handed out: "network mac ip
# node port" per line.
dhcp_leases() { [ -f "$ROOT/$P/dhcp.leases" ] && cat "$ROOT/$P/dhcp.leases"; true; }

relay_alive() { [ -f "$1/relay.pid" ] && kill -0 "$(cat "$1/relay.pid")" 2>/dev/null; }

relay_start() { # project dir
  command -v python3 >/dev/null 2>&1 || die "os cabos entre VMs precisam do python3 nesta máquina"
  relay_py >"$1/relay.py"
  nohup python3 "$1/relay.py" "$1/relay.map" >"$1/relay.log" 2>&1 &
  echo $! >"$1/relay.pid"
}

# mon_of sends a monitor command to another VM of the project.
mon_of() { (D=$ROOT/$P/$1; alive "$D" && mon "$2") || true; }

relay() {
  [ -n "$P" ] || die "falta GV_PROJECT"
  R=$ROOT/$P
  mkdir -p "$R"
  new=$R/relay.map.new
  printf '%s' "${GV_RELAY:-}" | b64d >"$new"
  # The ports whose peer changed: link down now, up after the new table.
  changed=$(awk '/^#/ { next } NR == FNR { old[$1] = $3; next } ($1 in old) && old[$1] != $3 { print $4, $5, $3 }' "$R/relay.map" "$new" 2>/dev/null)
  printf '%s\n' "$changed" | while read -r vm n p; do [ -n "$vm" ] && mon_of "$vm" "set_link nic$n off"; done
  mv "$new" "$R/relay.map"
  # A relay from an older Gantry (other code): restarted with this one.
  if relay_alive "$R" && ! relay_py | cmp -s - "$R/relay.py"; then
    kill "$(cat "$R/relay.pid")" 2>/dev/null
    i=0; while relay_alive "$R" && [ $i -lt 20 ]; do sleep 0.1; i=$((i + 1)); done
    rm -f "$R/relay.pid"
  fi
  if relay_alive "$R"; then kill -HUP "$(cat "$R/relay.pid")"; else relay_start "$R"; fi
  if [ -n "$changed" ]; then
    sleep 1
    printf '%s\n' "$changed" | while read -r vm n p; do [ -n "$vm" ] && [ "$p" != 0 ] && mon_of "$vm" "set_link nic$n on"; done
    # An IOL node names its ports Ethernet<slot>/<port>: e0/0, e0/1…
    say "cabos trocados: $(printf '%s\n' "$changed" | while read -r vm n p; do
      [ -n "$vm" ] || continue
      if [ -f "$ROOT/$P/$vm/iol" ]; then echo "$vm e$((n / 4))/$((n % 4))"
      elif [ -f "$ROOT/$P/$vm/dyn" ]; then echo "$vm $(dyn_port "$(head -n 1 "$ROOT/$P/$vm/dyn")" "$n" "$ROOT/$P/$vm/portnames")"
      elif [ "$(awk -v v="$vm" -v n="$n" '$4 == v && $5 == n { print $2 }' "$R/relay.map")" = ctr ]; then echo "$vm eth$((n + 1))"
      else echo "$vm placa $n"; fi
    done | awk 'NF { printf "%s%s", s, $0; s = ", " }')"
  fi
  # Ports with no cable stay down; the rest of the VMs keep theirs.
  awk '!/^#/ && $3 == 0 { print $4, $5 }' "$R/relay.map" | while read -r vm n; do mon_of "$vm" "set_link nic$n off"; done
}

# --- snapshots (internal qcow2 snapshots; with the VM running they include
# its memory, through the monitor) ---

need_vm() { [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"; [ -f "$D/disk.qcow2" ] || isprog "$D" || die "a VM $N ainda não foi criada"; }
# need_disk: snapshots, export, resize, clone and SSH need a QEMU disk; an
# IOL node is a program with its own files (nvram), not a disk.
need_disk() { need_vm; isprog "$D" && die "$N é um nó IOL/Dynamips: não tem disco nem snapshots (use Salvar estado do lab)"; return 0; }
snap_name() {
  case "${GV_SNAP:-}" in ''|*[!A-Za-z0-9._-]*) die "nome de snapshot inválido (use letras, números, ponto, - e _)";; esac
}

# snap_list: one line per snapshot: name has_memory(0|1) date time. The
# VM SIZE column is "0", "1.9M" or "1.89 MiB" depending on the qemu-img.
snap_list() {
  need_vm; isprog "$D" && return 0
  qemu-img snapshot -l -U "$D/disk.qcow2" 2>/dev/null | awk 'NR > 2 && NF >= 5 {
    i = 4; sz = $3
    if ($4 ~ /^[KMGTP]?i?B$/) { sz = $3 $4; i = 5 }
    mem = (sz ~ /^0(\.0+)?[KMGTP]?i?B?$/) ? 0 : 1
    print $2, mem, $i, $(i + 1)
  }'
}
snap_has() { snap_list | awk -v n="$1" '$1 == n { f=1 } END { exit !f }'; }

# wait_snap waits for a snapshot to appear (or disappear: GV_GONE=1).
wait_snap() {
  i=0
  while [ $i -lt 600 ]; do
    if [ "${GV_GONE:-}" = 1 ]; then snap_has "$1" || return 0
    else snap_has "$1" && return 0; fi
    sleep 0.5; i=$((i + 1))
  done
  return 1
}

snap_create() {
  need_disk; snap_name
  snap_has "$GV_SNAP" && die "já existe um snapshot $GV_SNAP"
  if alive "$D"; then
    mon "savevm $GV_SNAP" || die "não consegui falar com o monitor do QEMU (instale socat ou python3)"
    wait_snap "$GV_SNAP" || die "o snapshot não terminou a tempo"
    say "$N: snapshot $GV_SNAP criado com a VM rodando (inclui a memória)"
  else
    qemu-img snapshot -c "$GV_SNAP" "$D/disk.qcow2" || die "não consegui criar o snapshot"
    say "$N: snapshot $GV_SNAP criado (VM parada)"
  fi
}

snap_restore() {
  need_disk; snap_name
  snap_has "$GV_SNAP" || die "não existe o snapshot $GV_SNAP"
  state=$(snap_list | awk -v n="$GV_SNAP" '$1 == n { print $2 }')
  if alive "$D" && [ "$state" = 1 ]; then
    mon "loadvm $GV_SNAP" || die "não consegui falar com o monitor do QEMU"
    sleep 1
    say "$N: voltou ao snapshot $GV_SNAP (com a memória, sem reiniciar)"
    return 0
  fi
  was=0; alive "$D" && was=1
  [ $was = 1 ] && stop_vm
  qemu-img snapshot -a "$GV_SNAP" "$D/disk.qcow2" || die "não consegui voltar ao snapshot"
  say "$N: disco voltou ao snapshot $GV_SNAP"
  [ $was = 1 ] && start_vm
  return 0
}

snap_delete() {
  need_disk; snap_name
  snap_has "$GV_SNAP" || { say "$N: não existe o snapshot $GV_SNAP"; return 0; }
  if alive "$D"; then
    mon "delvm $GV_SNAP" || die "não consegui falar com o monitor do QEMU"
    GV_GONE=1 wait_snap "$GV_SNAP" || die "o snapshot não foi apagado a tempo"
  else
    qemu-img snapshot -d "$GV_SNAP" "$D/disk.qcow2" || die "não consegui apagar o snapshot"
  fi
  say "$N: snapshot $GV_SNAP apagado"
}

# --- states of a lab (Salvar estado): a VM keeps an internal snapshot
# (estado-<name>); an IOL node a copy of its NVRAM (and vlan.dat of the L2
# images) in estados/<name>/. GV_STATE names the state. ---

state_name() {
  case "${GV_STATE:-}" in ''|*[!A-Za-z0-9._-]*) die "nome de estado inválido (use letras, números, ponto, - e _)";; esac
}

# iol_files: the files an IOL or Dynamips node keeps its configuration in.
iol_files() {
  for f in "$D"/nvram_* "$D"/vlan.dat-* "$D"/*_nvram "$D"/startup.vpc; do [ -f "$f" ] && basename "$f"; done
  return 0
}

state_save() {
  need_vm; state_name
  if isprog "$D"; then
    S=$D/estados/$GV_STATE
    rm -rf "$S"; mkdir -p "$S"
    n=0
    for f in $(iol_files); do cp "$D/$f" "$S/" && n=$((n + 1)); done
    date +%s >"$S/.quando"
    if [ $n = 0 ]; then say "$N: estado $GV_STATE salvo sem NVRAM (o nó ainda não gravou a configuração: write memory)"
    else say "$N: estado $GV_STATE salvo (NVRAM)"; fi
  else
    GV_SNAP=estado-$GV_STATE
    if snap_has "$GV_SNAP"; then snap_delete >/dev/null; fi
    snap_create
  fi
}

# state_list: one line per state: name unix-time.
state_list() {
  need_vm
  if isprog "$D"; then
    for s in "$D"/estados/*/; do
      [ -d "$s" ] || continue
      echo "$(basename "$s") $(cat "$s/.quando" 2>/dev/null || echo 0)"
    done
  else
    snap_list | awk '$1 ~ /^estado-/ { print substr($1, 8), $3 " " $4 }' | while read -r n d t; do
      echo "$n $(date -d "$d $t" +%s 2>/dev/null || date -j -f '%Y-%m-%d %H:%M:%S' "$d $t" +%s 2>/dev/null || echo 0)"
    done
  fi
}

state_restore() {
  need_vm; state_name
  if isprog "$D"; then
    S=$D/estados/$GV_STATE
    [ -d "$S" ] || die "$N não tem o estado $GV_STATE"
    was=0; alive "$D" && was=1
    [ $was = 1 ] && stop_vm
    for f in $(iol_files); do rm -f "$D/$f"; done
    for f in "$S"/*; do [ -f "$f" ] && cp "$f" "$D/"; done
    say "$N: voltou ao estado $GV_STATE"
    [ $was = 1 ] && start_vm
    return 0
  fi
  GV_SNAP=estado-$GV_STATE snap_restore
}

state_delete() {
  need_vm; state_name
  if isprog "$D"; then rm -rf "$D/estados/$GV_STATE"; say "$N: estado $GV_STATE apagado"; return 0; fi
  GV_SNAP=estado-$GV_STATE snap_delete
}

# state_zero: an IOL node starts again with no configuration.
state_zero() {
  need_vm
  isprog "$D" || die "$N é uma VM: para recomeçar, apague a VM (o disco volta da imagem ao subir)"
  was=0; alive "$D" && was=1
  [ $was = 1 ] && stop_vm
  for f in $(iol_files); do rm -f "$D/$f"; done
  say "$N: sem configuração (NVRAM apagada)"
  [ $was = 1 ] && start_vm
  return 0
}

# console_save: "write memory" on the console of a running node (IOL, or a
# VM with a serial console), so its NVRAM has the running configuration.
console_save() {
  need_vm
  alive "$D" || { say "$N: parado, nada a gravar"; return 0; }
  port=$(sed -n 's/^serial=//p' "$D/vm.env" 2>/dev/null)
  [ -n "$port" ] || { say "$N: sem console serial, pulado"; return 0; }
  command -v python3 >/dev/null 2>&1 || die "gravar a configuração pelo console precisa do python3 nesta máquina"
  r=$(python3 - "$port" <<'PY'

import socket, sys, time
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=5)
out = b""
def send(b, wait, until=None):
    global out
    s.sendall(b)
    end = time.time() + wait
    while time.time() < end and not (until and until in out):
        s.settimeout(max(0.05, end - time.time()))
        try:
            d = s.recv(4096)
            if not d:
                break
            out += d
        except socket.timeout:
            break
send(b"\r", 0.5)
send(b"\x1a\r", 0.5)   # leaves configuration mode, if in it
send(b"write memory\r", 8, b"[OK]")
send(b"\r", 0.3)
tail = out.decode("latin-1").replace("\r", "")
ok = "[OK]" in tail or "Building configuration" in tail
print(("configuração gravada (write memory)" if ok else "write memory enviado; resposta: " + " ".join(tail.split()[-12:])))
PY
) || die "$N: não consegui falar com o console"
  say "$N: $r"
}

# nvram_pack: the configuration files of an IOL node in one tar (GV_FILE).
nvram_pack() {
  need_vm
  isprog "$D" || die "$N não é um nó IOL nem Dynamips"
  out=$D/nvram.tar
  # shellcheck disable=SC2046
  tar -cf "$out" -C "$D" $(iol_files) 2>/dev/null || tar -cf "$out" -T /dev/null
  echo "$out"
}

# nvram_unpack: puts the files of GV_FILE (a tar) in the node's folder,
# creating it when the node does not exist yet (a lab just imported).
nvram_unpack() {
  [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"
  [ -f "${GV_FILE:-}" ] || die "o arquivo da NVRAM não chegou"
  mkdir -p "$D"
  was=0; alive "$D" && was=1
  [ $was = 1 ] && stop_vm
  # The tar may come from a lab package of someone else: it is opened apart,
  # and only regular files with the names of a NVRAM come in (no links, no
  # other names, nothing in subfolders).
  t=$D/.nvram-in
  rm -rf "$t" && mkdir -p "$t" || die "não consegui preparar $t"
  tar -xf "$GV_FILE" -C "$t" || { rm -rf "$t"; die "não consegui abrir a NVRAM"; }
  for f in $(iol_files); do rm -f "$D/$f"; done
  for x in "$t"/nvram_* "$t"/vlan.dat-* "$t"/*_nvram "$t"/startup.vpc; do
    [ -f "$x" ] && [ ! -L "$x" ] || continue
    rm -f "$D/$(basename "$x")"
    mv "$x" "$D/" || die "não consegui colocar $(basename "$x")"
  done
  rm -rf "$t" "$GV_FILE"
  say "$N: NVRAM colocada"
  [ $was = 1 ] && start_vm
  return 0
}

# --- disk copy for backups: a standalone, compressed qcow2 (without the
# cached base image). A running VM is copied from a temporary snapshot, so
# the copy is consistent without stopping it. ---

disk_export() {
  need_disk
  out=$D/export.qcow2
  rm -f "$out"
  if alive "$D"; then
    GV_SNAP=gantry-backup
    snap_has "$GV_SNAP" && { mon "delvm $GV_SNAP"; GV_GONE=1 wait_snap "$GV_SNAP"; }
    mon "savevm $GV_SNAP" || die "não consegui falar com o monitor do QEMU"
    wait_snap "$GV_SNAP" || die "o snapshot do backup não terminou"
    qemu-img convert -U -q -O qcow2 -c -l snapshot.name=gantry-backup "$D/disk.qcow2" "$out" || { mon "delvm gantry-backup"; die "não consegui copiar o disco"; }
    mon "delvm gantry-backup" || true
  else
    qemu-img convert -q -O qcow2 -c "$D/disk.qcow2" "$out" || die "não consegui copiar o disco"
  fi
  echo "$out"
}

# disk_import puts $D/import.qcow2 (sent by the app) in place of the disk.
disk_import() {
  [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"
  # GV_FILE: a disk already on this machine (a lab package just unpacked).
  if [ -n "${GV_FILE:-}" ]; then mkdir -p "$D" && mv "$GV_FILE" "$D/import.qcow2" || die "o disco $GV_FILE não chegou"; fi
  [ -f "$D/import.qcow2" ] || die "o disco a restaurar não chegou"
  qemu-img check -q "$D/import.qcow2" >/dev/null 2>&1 || { rm -f "$D/import.qcow2"; die "o disco do backup está corrompido; nada foi alterado"; }
  # A disk from someone else may point at a file of this machine (a backing
  # file or an external data file, ~/.ssh/… say) for the VM to read: refused.
  if qemu-img info --output=json "$D/import.qcow2" 2>/dev/null | grep -Eq '"(backing-filename|data-file)"'; then
    rm -f "$D/import.qcow2"; die "o disco aponta para outro arquivo (backing file ou data-file); só discos completos são aceitos; nada foi alterado"
  fi
  was=0; alive "$D" && was=1
  [ $was = 1 ] && stop_vm
  [ -f "$D/disk.qcow2" ] && mv "$D/disk.qcow2" "$D/disk.antes-do-restauro.qcow2"
  mv "$D/import.qcow2" "$D/disk.qcow2"
  rm -f "$D/disk.antes-do-restauro.qcow2"
  say "$N: disco restaurado"
  if [ $was = 1 ] && [ -f "$D/start.sh" ]; then start_vm; fi
  return 0
}

# --- disk size, clone ---

disk_resize() {
  need_disk
  case "${GV_SIZE:-}" in [0-9]*[GMT]) ;; *) die "tamanho inválido (ex.: 40G)";; esac
  alive "$D" && die "pare a VM $N antes de aumentar o disco"
  cur=$(qemu-img info -U "$D/disk.qcow2" | sed -n 's/^virtual size: .*(\([0-9]*\) bytes).*/\1/p')
  qemu-img resize -q "$D/disk.qcow2" "$GV_SIZE" 2>"$D/resize.err" || die "não consegui aumentar o disco: $(cat "$D/resize.err") (só dá para aumentar)"
  say "$N: disco agora com $GV_SIZE (era $((cur / 1073741824))G); o sistema da VM aumenta a partição sozinho no próximo início (cloud-init)"
}

clone_vm() {
  need_disk
  case "${GV_TO:-}" in ''|*[!A-Za-z0-9_-]*) die "nome inválido para a cópia";; esac
  T=$ROOT/$P/$GV_TO
  [ -e "$T" ] && die "já existe uma VM $GV_TO neste projeto"
  alive "$D" && die "pare a VM $N antes de clonar (o disco precisa estar parado)"
  mkdir -p "$T"
  cp "$D/disk.qcow2" "$T/disk.qcow2" || { rm -rf "$T"; die "não consegui copiar o disco"; }
  # Marks the copy: its seed gives it a machine-id and host keys of its own;
  # it has no instance-id (nor nix.hash) of the origin.
  : >"$T/clone"
  # A new instance-id makes cloud-init configure the copy as a new machine
  # (hostname, SSH keys, network).
  say "$N: disco copiado para $GV_TO"
}

# --- start with the machine ---

autostart_set() {
  need_vm
  if [ "${GV_ON:-}" = 1 ]; then : >"$D/autostart"; say "$N: inicia junto com a máquina"
  else rm -f "$D/autostart"; say "$N: não inicia mais sozinha"; fi
}

# autostart starts every VM marked to start with the machine (run by the
# boot hook the app installs: systemd --user or a macOS LaunchAgent).
autostart() {
  # The cables first, so the VMs come up already connected.
  for m in "$ROOT"/*/relay.map; do
    [ -f "$m" ] || continue
    r=$(dirname "$m"); relay_alive "$r" || relay_start "$r"
  done
  for f in "$ROOT"/*/*/autostart; do
    [ -f "$f" ] || continue
    d=$(dirname "$f"); n=$(basename "$d"); p=$(basename "$(dirname "$d")")
    ( P=$p; N=$n; D=$d; start_vm ) || echo "$p/$n: não iniciou" >&2
  done
}

# --- other disks: OVA, VMDK, VDI, VHD(X), raw -> qcow2 ---

convert_disk() {
  src=${GV_SRC:-}; dst=${GV_DST:-}
  [ -f "$src" ] || die "não achei o arquivo $src"
  [ -n "$dst" ] || die "falta o destino"
  command -v qemu-img >/dev/null 2>&1 || die "o qemu-img não está instalado nesta máquina. $(hint)"
  # VMs use these disks as their base: never write over one.
  [ -e "$dst" ] && die "já existe um disco com este nome ($dst); escolha outro nome"
  mkdir -p "$(dirname "$dst")"
  part=$dst.part
  rm -f "$part"
  case "$src" in
    *.ova|*.OVA)
      t=$dst.ova.d
      rm -rf "$t"; mkdir -p "$t"
      tar -xf "$src" -C "$t" || { rm -rf "$t"; die "não consegui abrir o OVA"; }
      v=$(find "$t" -iname '*.vmdk' | head -1)
      [ -n "$v" ] || { rm -rf "$t"; die "o OVA não tem disco VMDK"; }
      qemu-img convert -p -O qcow2 "$v" "$part" || { rm -rf "$t" "$part"; die "não consegui converter o disco"; }
      rm -rf "$t" ;;
    *) qemu-img convert -p -O qcow2 "$src" "$part" || { rm -f "$part"; die "não consegui converter o disco"; } ;;
  esac
  mv "$part" "$dst" || die "não consegui gravar $dst"
  say "disco pronto: $dst ($(du -h "$dst" | cut -f1))"
}

status() { # one line per VM of the project: name state pid ssh accel ports vnc lan autostart spec nixspec basespec
  [ -d "$ROOT/$P" ] || exit 0
  for d in "$ROOT/$P"/*/; do
    [ -f "$d/vm.env" ] || continue
    n=$(basename "$d")
    st=stopped; pid=-
    if alive "$d"; then st=running; pid=$(cat "$d/qemu.pid"); fi
    ssh=$(sed -n 's/^ssh=//p' "$d/vm.env"); acc=$(sed -n 's/^accel=//p' "$d/vm.env")
    ports=$(sed -n 's/^ports=//p' "$d/vm.env" | tr ' ' ',')
    vnc=$(sed -n 's/^vnc=//p' "$d/vm.env")
    lip=$(sed -n 's/^lan=//p' "$d/vm.env")
    auto=0; [ -f "$d/autostart" ] && auto=1
    spec=$(cat "$d/spec.hash" 2>/dev/null)
    nixs=$(cat "$d/nix.hash" 2>/dev/null)
    base=$(cat "$d/base.hash" 2>/dev/null)
    ser=$(sed -n 's/^serial=//p' "$d/vm.env"); ci=$(sed -n 's/^ci=//p' "$d/vm.env")
    rej=0; [ -s "$d/boot-rejected.txt" ] && rej=$(wc -l <"$d/boot-rejected.txt" | tr -d ' ')
    # Dynamips: whether its image has an idle-PC yet (without one the
    # router keeps a CPU thread busy).
    idle=-; if [ "$acc" = dynamips ]; then idle=0; b=$(sed -n 2p "$d/dyn" 2>/dev/null); [ -n "$b" ] && [ -s "$(idle_file "$b")" ] && idle=1; fi
    echo "$n $st $pid ${ssh:--} ${acc:--} ${ports:--} ${vnc:--} ${lip:--} $auto ${spec:--} ${nixs:--} ${base:--} serial=${ser:-} ci=${ci:-1} rej=$rej idle=$idle"
  done
}

# cputime prints the CPU seconds a process has used: from /proc on Linux
# (ps rounds to whole seconds there), from ps elsewhere (0:01.23).
cputime() {
  if [ -r "/proc/$1/stat" ]; then
    sed 's/.*) //' "/proc/$1/stat" | awk -v hz="$(getconf CLK_TCK 2>/dev/null || echo 100)" '{printf "%.2f", ($12 + $13) / hz}'
  else
    ps -o time= -p "$1" 2>/dev/null | tr -d ' '
  fi
}

# memkb prints the memory a process uses, in KB. On macOS the RSS of a VM
# drops as the system compresses its pages, so use what top (and the Activity
# Monitor) call memory; elsewhere, and if that fails, the RSS.
memkb() {
  k=""
  if [ "$(uname -s)" = Darwin ]; then
    k=$(top -l 1 -pid "$1" -stats mem 2>/dev/null | tail -1 | awk '{
      v = $1; sub(/[+-]$/, "", v); u = substr(v, length(v)); n = substr(v, 1, length(v) - 1)
      if (n !~ /^[0-9.]+$/ && v !~ /^[0-9]+$/) exit
      if (u == "K") printf "%d", n; else if (u == "M") printf "%d", n * 1024
      else if (u == "G") printf "%d", n * 1048576; else if (u == "B") printf "%d", n / 1024
      else if (v ~ /^[0-9]+$/) printf "%d", v / 1024 }')
  fi
  [ -n "$k" ] && [ "$k" != 0 ] || k=$(ps -o rss= -p "$1" 2>/dev/null | tr -d ' ')
  echo "${k:-0}"
}

stats() { # one line per running VM: project name cpu_seconds rss_kb disk_kb mem_mb cpus
  [ -d "$ROOT" ] || exit 0
  for d in "$ROOT"/*/*/; do
    d=${d%/}
    [ -f "$d/vm.env" ] || continue
    alive "$d" || continue
    p=$(basename "$(dirname "$d")"); n=$(basename "$d")
    [ -n "$P" ] && [ "$p" != "$P" ] && continue
    pid=$(cat "$d/qemu.pid")
    t=$(cputime "$pid")
    r=$(memkb "$pid")
    k=$(du -k "$d/disk.qcow2" 2>/dev/null | cut -f1)
    m=$(sed -n 's/^mem=//p' "$d/vm.env"); c=$(sed -n 's/^cpus=//p' "$d/vm.env")
    echo "$p $n ${t:--} ${r:-0} ${k:-0} ${m:-0} ${c:-1}"
  done
}

case "${1:-}" in
  up) up ;;
  relay) relay ;;
  relay-map) cat "$ROOT/$P/relay.map" 2>/dev/null ;;
  capture-start) capture_start ;;
  capture-stop) capture_stop ;;
  dhcp-leases) dhcp_leases ;;
  image) base_image ;; # downloads (and checks) the base image only
  start) start_vm ;;
  stop) stop_vm ;;
  restart) stop_vm && start_vm ;;
  rm)
    [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"
    GV_TIMEOUT=${GV_TIMEOUT:-5} # the disk is deleted anyway
    [ -d "$D" ] && stop_vm
    lan_free
    rm -rf "$D" && say "$N: VM e disco apagados"
    rmdir "$ROOT/$P" 2>/dev/null || true ;;
  status) status ;;
  stats) stats ;;
  keyfile) key ;;
  netinfo) netinfo ;;
  net-set) net_set ;;
  lan-ip) [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"; lan_ip ;;
  ssh) echo "ssh -i $(key) -p $(sed -n 's/^ssh=//p' "$D/vm.env") -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null gantry@127.0.0.1" ;;
  vnc) v=$(sed -n 's/^vnc=//p' "$D/vm.env" 2>/dev/null); [ -n "$v" ] && [ -f "$D/vnc.pass" ] && echo "$v $(cat "$D/vnc.pass")" ;;
  rejected) f=$D/boot-rejected.txt; [ -f "$f" ] && cat "$f"; exit 0 ;;
  console-tail) f=$D/console.log; [ -f "$f" ] && tail -c 4096 "$f" ;;
  console) f=$D/console.log; [ -f "$f" ] || exit 0; tail -c "+$((${GV_FROM:-0} + 1))" "$f" | head -c 65536 ;;
  snap-list) snap_list ;;
  snap-create) snap_create ;;
  snap-restore) snap_restore ;;
  snap-delete) snap_delete ;;
  port-live) port_live ;;
  state-save) state_save ;;
  state-list) state_list ;;
  state-restore) state_restore ;;
  state-delete) state_delete ;;
  state-zero) state_zero ;;
  console-save) console_save ;;
  nvram-pack) nvram_pack ;;
  nvram-unpack) nvram_unpack ;;
  idle-pc) dyn_idlepc ;;
  export) disk_export ;;
  export-clean) rm -f "$D/export.qcow2" ;;
  import) disk_import ;;
  resize) disk_resize ;;
  clone) clone_vm ;;
  autostart-set) autostart_set ;;
  autostart) autostart ;;
  convert) convert_disk ;;
  autostart-state) [ -f "$D/autostart" ] && echo 1 || echo 0 ;;
  seed) # the cloud-init files of the VM, in GV_DIR (for tests and checks)
    [ -n "$P" ] && [ -n "$N" ] || die "faltam GV_PROJECT e GV_NAME"
    mkdir -p "$D"; write_seed "${GV_DIR:?}" ;;
  hash-set) # the definition changed and the VM got it live (ports)
    need_vm
    printf '%s\n' "${GV_HASH:-}" >"$D/spec.hash"
    printf '%s\n' "${GV_BASEHASH:-}" >"$D/base.hash" ;;
  spec-set) # the Nix configuration was applied inside the VM (Aplicar)
    need_vm
    printf '%s\n' "${GV_HASH:-}" >"$D/spec.hash"
    printf '%s\n' "${GV_BASEHASH:-}" >"$D/base.hash"
    printf '%s\n' "${GV_NIXHASH:-}" >"$D/nix.hash" ;;
  exec) # runs GV_CMD (base64) inside the VM over SSH, without prompts
    need_disk
    alive "$D" || die "a VM $N não está rodando"
    c=$(printf '%s' "${GV_CMD:-}" | base64 -d 2>/dev/null || printf '%s' "${GV_CMD:-}" | base64 -D)
    exec ssh -i "$(key)" -p "$(sed -n 's/^ssh=//p' "$D/vm.env")" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o BatchMode=yes -o ConnectTimeout=8 -o LogLevel=ERROR gantry@127.0.0.1 "$c" ;;
  *) die "comando desconhecido: ${1:-}" ;;
esac
