#!/bin/sh
# Instala o Gantry para este usuário (sem sudo): o programa em
# ~/.local/bin, o atalho no menu de aplicativos e o ícone.
#   ./instalar.sh            instala ou atualiza
#   ./instalar.sh remover    tira o programa, o atalho e o ícone
# Os hosts, labs e clusters (~/.config/gantry) ficam como estão.
set -e
cd "$(dirname "$0")"
BIN=${XDG_BIN_HOME:-$HOME/.local/bin}
APPS=${XDG_DATA_HOME:-$HOME/.local/share}/applications
ICONS=${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/256x256/apps

if [ "${1:-}" = remover ]; then
  rm -f "$BIN/gantry" "$APPS/gantry.desktop" "$ICONS/gantry.png"
  echo "Gantry removido (as configurações em ~/.config/gantry ficaram)."
  exit 0
fi

mkdir -p "$BIN" "$APPS" "$ICONS"
install -m 755 gantry "$BIN/gantry"
install -m 644 gantry.png "$ICONS/gantry.png"
sed "s|^Exec=gantry|Exec=$BIN/gantry|" gantry.desktop >"$APPS/gantry.desktop"
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPS" >/dev/null 2>&1 || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q "${ICONS%/256x256/apps}" >/dev/null 2>&1 || true

echo "Gantry instalado em $BIN/gantry e no menu de aplicativos."

# The app needs WebKitGTK 4.1 (the window) at run time.
if ! ldconfig -p 2>/dev/null | grep -q 'libwebkit2gtk-4.1.so'; then
  echo
  echo "Falta o WebKitGTK 4.1, que desenha a janela do Gantry. Instale com:"
  echo "  Fedora:        sudo dnf install webkit2gtk4.1"
  echo "  Debian/Ubuntu: sudo apt install libwebkit2gtk-4.1-0"
  echo "  Arch:          sudo pacman -S webkit2gtk-4.1"
fi
case ":$PATH:" in *":$BIN:"*) ;; *) echo; echo "Obs.: $BIN não está no PATH; abra pelo menu de aplicativos ou acrescente $BIN ao PATH." ;; esac
