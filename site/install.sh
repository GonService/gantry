#!/bin/sh
# Installs Gantry (https://gonservice.github.io/gantry/) for this user.
# Instala o Gantry para este usuário.
#
#   curl -fsSL https://gonservice.github.io/gantry/install.sh | sh
#   curl -fsSL https://gonservice.github.io/gantry/install.sh | sh -s -- --server
#
# Linux with a desktop: the app (x86_64), with its entry in the applications
# menu. Linux without a desktop, or --server: the server version (the UI
# opens in a browser). macOS: Homebrew when there is one, otherwise the app
# in Applications. Running it again updates.
#
# GANTRY_VERSION=0.48.0 picks a version. Every file is checked against the
# SHA256SUMS of the release before it is installed. Nothing needs sudo.
set -eu

REPO=GonService/gantry
case "${LC_ALL:-}${LANG:-}" in pt*|*:pt*) PT=1 ;; *) PT=0 ;; esac
say() { if [ "$PT" = 1 ]; then printf '%s\n' "$1"; else printf '%s\n' "$2"; fi; }
die() { if [ "$PT" = 1 ]; then printf 'Erro: %s\n' "$1" >&2; else printf 'Error: %s\n' "$2" >&2; fi; exit 1; }

MODE=auto
for a in "$@"; do
  case "$a" in
    --server|--servidor) MODE=server ;;
    --desktop) MODE=desktop ;;
    -h|--help)
      say "Uso: install.sh [--server | --desktop]   (GANTRY_VERSION=X.Y.Z para uma versão)" \
          "Usage: install.sh [--server | --desktop]   (GANTRY_VERSION=X.Y.Z for a version)"
      exit 0 ;;
    *) die "opção desconhecida: $a" "unknown option: $a" ;;
  esac
done

command -v curl >/dev/null 2>&1 || die "precisa do curl" "curl is required"
command -v tar >/dev/null 2>&1 || die "precisa do tar" "tar is required"
if command -v sha256sum >/dev/null 2>&1; then SHA="sha256sum"
elif command -v shasum >/dev/null 2>&1; then SHA="shasum -a 256"
else die "precisa do sha256sum ou do shasum" "sha256sum or shasum is required"; fi

OS=$(uname -s)
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "processador não suportado: $(uname -m)" "unsupported processor: $(uname -m)" ;;
esac

# The newest version: where github.com/…/releases/latest redirects to (no
# API call, so no rate limit).
V=${GANTRY_VERSION:-}
if [ -z "$V" ]; then
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
    die "não consegui ver a versão mais nova no GitHub" "could not find the newest version on GitHub"
  V=${url##*/tag/v}
fi
V=${V#v}
case "$V" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) die "versão inválida: $V" "invalid version: $V" ;;
esac
BASE="https://github.com/$REPO/releases/download/v$V"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

# fetch downloads a file of the release and checks it against SHA256SUMS.
fetch() {
  say "Baixando $1…" "Downloading $1…"
  curl -fSL --progress-bar "$BASE/$1" -o "$TMP/$1" || die "não consegui baixar $BASE/$1" "could not download $BASE/$1"
  want=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$TMP/SHA256SUMS")
  [ -n "$want" ] || die "$1 não está no SHA256SUMS" "$1 is not in SHA256SUMS"
  got=$(cd "$TMP" && $SHA "$1" | awk '{print $1}')
  [ "$want" = "$got" ] || die "o arquivo $1 não confere com o SHA256SUMS" "$1 does not match SHA256SUMS"
}

curl -fsSL "$BASE/SHA256SUMS" -o "$TMP/SHA256SUMS" ||
  die "a versão $V não existe ou não tem o SHA256SUMS" "version $V does not exist or has no SHA256SUMS"
say "Gantry $V" "Gantry $V"

BIN=${XDG_BIN_HOME:-$HOME/.local/bin}
in_path() { case ":$PATH:" in *":$BIN:"*) return 0 ;; esac; return 1; }

install_server() {
  F="gantry-servidor-$V-linux-$ARCH.tar.gz"
  fetch "$F"
  tar -xzf "$TMP/$F" -C "$TMP" gantry
  mkdir -p "$BIN"
  install -m 755 "$TMP/gantry" "$BIN/gantry"
  say "" ""
  say "Gantry (versão de servidor) instalado em $BIN/gantry." "Gantry (server version) installed in $BIN/gantry."
  say "Rode:  gantry -no-browser     (escuta em 127.0.0.1:7777 e mostra um link)" \
      "Run:   gantry -no-browser     (listens on 127.0.0.1:7777 and prints a link)"
  say "Do seu computador:  ssh -L 7777:127.0.0.1:7777 este-servidor   e abra o link." \
      "From your computer: ssh -L 7777:127.0.0.1:7777 this-server   and open the link."
  in_path || say "Obs.: $BIN não está no PATH." "Note: $BIN is not in your PATH."
}

install_linux_desktop() {
  [ "$ARCH" = amd64 ] || die "o app desktop do Linux é só x86_64; use --server" "the Linux desktop app is x86_64 only; use --server"
  F="Gantry-$V-linux-amd64.tar.gz"
  fetch "$F"
  tar -xzf "$TMP/$F" -C "$TMP"
  sh "$TMP/Gantry-$V-linux-amd64/instalar.sh"
}

install_mac() {
  if command -v brew >/dev/null 2>&1; then
    say "Instalando pelo Homebrew…" "Installing with Homebrew…"
    if brew list --cask gantry >/dev/null 2>&1; then
      brew upgrade --cask gonservice/tap/gantry || true
    else
      brew install --cask gonservice/tap/gantry
    fi
    return
  fi
  F="Gantry-$V-macos.zip"
  fetch "$F"
  command -v ditto >/dev/null 2>&1 || die "precisa do ditto (macOS)" "ditto is required (macOS)"
  ditto -x -k "$TMP/$F" "$TMP/app"
  DEST=/Applications
  [ -w "$DEST" ] || DEST="$HOME/Applications"
  mkdir -p "$DEST"
  rm -rf "$DEST/Gantry.app"
  ditto "$TMP/app/Gantry.app" "$DEST/Gantry.app"
  # Only this app, which you asked for: without it macOS blocks an app
  # that is not signed yet.
  xattr -dr com.apple.quarantine "$DEST/Gantry.app" 2>/dev/null || true
  say "Gantry instalado em $DEST/Gantry.app." "Gantry installed in $DEST/Gantry.app."
}

case "$OS" in
  Linux)
    if [ "$MODE" = auto ]; then
      if [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] && [ "$ARCH" = amd64 ]; then MODE=desktop; else MODE=server; fi
    fi
    if [ "$MODE" = desktop ]; then install_linux_desktop; else install_server; fi
    ;;
  Darwin)
    [ "$MODE" = server ] && die "a versão de servidor é para Linux; no Mac, use o app" "the server version is for Linux; on a Mac, use the app"
    install_mac
    ;;
  *)
    die "sistema não suportado: $OS (no Windows: irm https://gonservice.github.io/gantry/install.ps1 | iex)" \
        "unsupported system: $OS (on Windows: irm https://gonservice.github.io/gantry/install.ps1 | iex)"
    ;;
esac
