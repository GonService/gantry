#!/usr/bin/env bash
# VM Fedora com Podman no QEMU, para testar o Gantry contra um host remoto.
#
#   scripts/test-vm.sh start | stop | ssh [cmd] | status | console | reset
#
# Variáveis opcionais:
#   GANTRY_VM_DIR       pasta da VM (padrão ~/.cache/gantry-vm)
#   GANTRY_VM_USER      usuário criado na VM (padrão: o seu)
#   GANTRY_VM_PASSWORD  senha desse usuário (padrão gantry)
#   GANTRY_VM_PORT      porta local do SSH (padrão 2222)
#   GANTRY_VM_MEM       memória em MB (padrão 2048)
#   GANTRY_VM_CPUS      CPUs (padrão 2)
#   GANTRY_VM_SOCKET    on|off: liga o podman.socket do usuário (padrão on;
#                       off serve para testar o botão de ativar no Gantry)
#   FEDORA_VERSION      força uma versão do Fedora (padrão: a mais nova achada)
set -euo pipefail

DIR=${GANTRY_VM_DIR:-$HOME/.cache/gantry-vm}
VM_USER=${GANTRY_VM_USER:-$USER}
VM_PASS=${GANTRY_VM_PASSWORD:-gantry}
PORT=${GANTRY_VM_PORT:-2222}
MEM=${GANTRY_VM_MEM:-2048}
CPUS=${GANTRY_VM_CPUS:-2}
SOCKET=${GANTRY_VM_SOCKET:-on}

PIDFILE=$DIR/qemu.pid
DISK=$DIR/disk.qcow2
SEED=$DIR/seed.iso
LOG=$DIR/console.log

case "$(uname -m)" in
  arm64 | aarch64) ARCH=aarch64 ;;
  x86_64) ARCH=x86_64 ;;
  *) echo "arquitetura não suportada: $(uname -m)" >&2; exit 1 ;;
esac

say() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merro:\033[0m %s\n' "$*" >&2; exit 1; }

ssh_opts=(-p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=3)

running() { [[ -f $PIDFILE ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 não encontrado. $2"; }

qemu_share() {
  local p
  if command -v brew >/dev/null 2>&1 && p=$(brew --prefix qemu 2>/dev/null) && [[ -d $p/share/qemu ]]; then
    echo "$p/share/qemu"; return
  fi
  for p in /opt/homebrew/share/qemu /usr/local/share/qemu /usr/share/qemu /usr/share/AAVMF; do
    [[ -d $p ]] && { echo "$p"; return; }
  done
  die "não achei os arquivos do QEMU (firmware UEFI)."
}

accel() {
  case "$(uname -s)" in
    Darwin) echo hvf ;;
    Linux) [[ -w /dev/kvm ]] && echo kvm || echo tcg ;;
    *) echo tcg ;;
  esac
}

find_image() {
  local base=https://download.fedoraproject.org/pub/fedora/linux/releases
  local versions=("${FEDORA_VERSION:-}")
  [[ -n ${FEDORA_VERSION:-} ]] || versions=(46 45 44 43 42 41)
  local v name
  for v in "${versions[@]}"; do
    name=$(curl -fsSL "$base/$v/Cloud/$ARCH/images/" 2>/dev/null |
      grep -o "Fedora-Cloud-Base-Generic[-.][^\"<>]*\.$ARCH\.qcow2" | head -1) || true
    if [[ -n $name ]]; then
      echo "$base/$v/Cloud/$ARCH/images/$name"
      return
    fi
  done
  die "não achei a imagem cloud do Fedora. Defina FEDORA_VERSION ou baixe à mão para $DIR/base.qcow2"
}

pubkeys() {
  local k out=""
  if command -v ssh-add >/dev/null 2>&1; then out=$(ssh-add -L 2>/dev/null | grep -v '^The agent' || true); fi
  for k in "$HOME"/.ssh/id_ed25519.pub "$HOME"/.ssh/id_ecdsa.pub "$HOME"/.ssh/id_rsa.pub; do
    [[ -f $k ]] && out+=$'\n'$(cat "$k")
  done
  printf '%s\n' "$out" | awk 'NF && !seen[$0]++'
}

make_seed() {
  local tmp keys
  tmp=$(mktemp -d)
  keys=$(pubkeys)
  [[ -n $keys ]] || say "nenhuma chave SSH achada; o login vai ser só por senha"
  {
    echo "#cloud-config"
    echo "hostname: gantry-vm"
    echo "users:"
    echo "  - name: $VM_USER"
    echo "    groups: [wheel]"
    echo "    sudo: ALL=(ALL) NOPASSWD:ALL"
    echo "    shell: /bin/bash"
    echo "    lock_passwd: false"
    echo "    plain_text_passwd: '$VM_PASS'"
    if [[ -n $keys ]]; then
      echo "    ssh_authorized_keys:"
      while IFS= read -r k; do echo "      - '$k'"; done <<<"$keys"
    fi
    echo "ssh_pwauth: true"
    echo "packages: [podman, podman-compose]"
    echo "runcmd:"
    echo "  - [loginctl, enable-linger, '$VM_USER']"
    if [[ $SOCKET == on ]]; then
      echo "  - [bash, -c, 'for i in \$(seq 30); do systemctl --machine=$VM_USER@ --user enable --now podman.socket && break; sleep 2; done']"
    fi
    echo "  - [bash, -c, 'mkdir -p ~$VM_USER/labs/web && printf \"services:\\n  web:\\n    image: docker.io/library/nginx:alpine\\n    ports: [\\\"8080:80\\\"]\\n\" > ~$VM_USER/labs/web/compose.yaml && chown -R $VM_USER: ~$VM_USER/labs']"
  } >"$tmp/user-data"
  printf 'instance-id: gantry-vm-%s\nlocal-hostname: gantry-vm\n' "$(date +%s)" >"$tmp/meta-data"

  rm -f "$SEED"
  if command -v hdiutil >/dev/null 2>&1; then
    hdiutil makehybrid -quiet -iso -joliet -default-volume-name cidata -o "$SEED" "$tmp"
  elif command -v genisoimage >/dev/null 2>&1; then
    genisoimage -quiet -output "$SEED" -volid cidata -joliet -rock "$tmp/user-data" "$tmp/meta-data"
  elif command -v mkisofs >/dev/null 2>&1; then
    mkisofs -quiet -output "$SEED" -volid cidata -joliet -rock "$tmp/user-data" "$tmp/meta-data"
  else
    die "preciso de hdiutil, genisoimage ou mkisofs para gerar o cloud-init"
  fi
  rm -rf "$tmp"
}

prepare() {
  mkdir -p "$DIR"
  if [[ ! -f $DIR/base.qcow2 ]]; then
    local url
    url=$(find_image)
    say "baixando $(basename "$url")"
    curl -fL --progress-bar -o "$DIR/base.qcow2.part" "$url"
    mv "$DIR/base.qcow2.part" "$DIR/base.qcow2"
  fi
  if [[ ! -f $DISK ]]; then
    say "criando o disco da VM (20 GB, cópia sob demanda)"
    qemu-img create -q -f qcow2 -b "$DIR/base.qcow2" -F qcow2 "$DISK" 20G
    make_seed
  fi
}

# vm_ssh roda um comando sem nunca perguntar senha. Sai com 0 se rodou,
# 2 se o SSH respondeu mas pediu senha (chave com frase fora do agente),
# 1 se o SSH ainda não responde e 3 se o comando remoto falhou.
vm_ssh() {
  local err rc=0
  { err=$(ssh "${ssh_opts[@]}" -o BatchMode=yes "$VM_USER@127.0.0.1" "$@" 2>&1 1>&3 3>&-) || rc=$?; } 3>&1
  ((rc == 0)) && return 0
  [[ $err == *"Permission denied"* ]] && return 2
  # Saída 255 é falha do próprio ssh; outro código é do comando remoto.
  ((rc == 255)) && return 1
  return 3
}

wait_ssh() {
  local i rc
  printf 'esperando o SSH '
  for ((i = 0; i < 120; i++)); do
    rc=0; vm_ssh true </dev/null >/dev/null 2>&1 || rc=$?
    if ((rc != 1)); then
      echo " ok"; return 0
    fi
    running || { echo; die "o QEMU parou; veja $LOG"; }
    printf '.'; sleep 3
  done
  echo; die "o SSH não respondeu em 6 minutos; veja $LOG"
}

cmd_start() {
  need qemu-system-"$ARCH" "Instale com: brew install qemu"
  need qemu-img "Instale com: brew install qemu"
  running && { say "a VM já está rodando"; cmd_status; return; }
  prepare

  local share acc args
  share=$(qemu_share)
  acc=$(accel)
  args=(-name gantry-vm -m "$MEM" -smp "$CPUS" -accel "$acc"
    -drive "if=virtio,file=$DISK,format=qcow2"
    -drive "if=virtio,file=$SEED,format=raw,media=cdrom,readonly=on"
    -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$PORT-:22"
    -device "virtio-net-pci,netdev=n0"
    -device virtio-rng-pci
    -display none -serial "file:$LOG"
    -daemonize -pidfile "$PIDFILE")
  if [[ $ARCH == aarch64 ]]; then
    local fw=$share/edk2-aarch64-code.fd
    [[ -f $fw ]] || fw=$(ls "$share"/*AAVMF*CODE*.fd "$share"/QEMU_EFI.fd 2>/dev/null | head -1) || true
    [[ -f $fw ]] || die "firmware UEFI aarch64 não encontrado em $share"
    args+=(-machine virt -cpu "$([[ $acc == tcg ]] && echo max || echo host)" -bios "$fw")
  else
    args+=(-machine q35 -cpu "$([[ $acc == tcg ]] && echo max || echo host)")
  fi

  say "subindo a VM ($ARCH, $acc, ${MEM} MB, $CPUS CPUs)"
  qemu-system-"$ARCH" "${args[@]}"
  wait_ssh
  say "esperando o cloud-init terminar (instala o Podman; na primeira vez leva alguns minutos)"
  local i rc
  for ((i = 0; i < 300; i++)); do
    grep -q 'Cloud-init .* finished' "$LOG" 2>/dev/null && break
    rc=0; vm_ssh 'cloud-init status 2>/dev/null | grep -Eq "done|error|degraded"' </dev/null >/dev/null 2>&1 || rc=$?
    ((rc == 0)) && break
    sleep 3
  done
  cmd_status
  cat <<EOF

No Gantry: Adicionar host
  Nome:      vm-teste
  Endereço:  ssh://$VM_USER@127.0.0.1:$PORT
  Senha:     $VM_PASS  (usada só se a chave não servir)

Um compose de exemplo está em ~/labs/web dentro da VM (use "Abrir pasta").
EOF
}

cmd_stop() {
  running || { say "a VM não está rodando"; return; }
  say "desligando a VM"
  vm_ssh 'sudo systemctl poweroff' </dev/null >/dev/null 2>&1 || true
  local i
  for ((i = 0; i < 30; i++)); do running || break; sleep 1; done
  if running; then kill "$(cat "$PIDFILE")" 2>/dev/null || true; fi
  rm -f "$PIDFILE"
}

cmd_status() {
  if ! running; then echo "parada"; return; fi
  echo "rodando (pid $(cat "$PIDFILE")), SSH em 127.0.0.1:$PORT"
  local rc=0
  vm_ssh \
    'echo "  $(. /etc/os-release; echo $PRETTY_NAME)"; echo "  $(podman --version 2>/dev/null || echo podman ainda não instalado)"; echo "  podman.socket: $(systemctl --user is-active podman.socket 2>/dev/null)"; echo "  cloud-init: $(cloud-init status 2>/dev/null | sed "s/status: //")"' 2>/dev/null </dev/null || rc=$?
  ((rc == 2)) && echo "  (o SSH pede senha ou a frase da chave; para detalhes use: $0 ssh)"
  ((rc == 1)) && echo "  (SSH ainda não responde)"
}

cmd_ssh() {
  running || die "a VM não está rodando; use: $0 start"
  exec ssh "${ssh_opts[@]}" "$VM_USER@127.0.0.1" "$@"
}

cmd_reset() {
  cmd_stop
  rm -f "$DISK" "$SEED" "$LOG"
  # A VM nova gera outra chave de host; tira a antiga do known_hosts para
  # o ssh (e o Gantry) não recusarem a conexão.
  ssh-keygen -R "[127.0.0.1]:$PORT" >/dev/null 2>&1 || true
  say "disco apagado (a imagem base continua em $DIR/base.qcow2). Rode: $0 start"
}

case "${1:-}" in
  start) cmd_start ;;
  stop) cmd_stop ;;
  status) cmd_status ;;
  ssh) shift; cmd_ssh "$@" ;;
  console) tail -f "$LOG" ;;
  reset) cmd_reset ;;
  *) echo "uso: $0 start | stop | status | ssh [comando] | console | reset"; exit 1 ;;
esac
