#!/usr/bin/env bash
# Gera a receita do Homebrew (Casks/gantry.rb do GonService/homebrew-tap).
#   scripts/cask.sh VERSAO SHA256 [true]
# O terceiro argumento (true) diz que o app é assinado e notarizado: sem
# ele, a receita tira a quarentena (o macOS bloquearia um app sem assinatura).
# A pipeline de Release chama isto; também serve para gerar à mão.
#
# O download aponta para as Releases do repositório do Gantry.
set -euo pipefail
VERSAO="$1"
SHA="$2"
ASSINADO="${3:-false}"
REPO="GonService/gantry"

cat <<RB
cask "gantry" do
  version "$VERSAO"
  sha256 "$SHA"

  url "https://github.com/$REPO/releases/download/v#{version}/Gantry-#{version}-macos.zip"
  name "Gantry"
  desc "Manage containers, VMs and Kubernetes clusters on local and remote machines"
  homepage "https://gonservice.github.io/gantry/"

  depends_on :macos

  app "Gantry.app"
RB
if [ "$ASSINADO" != true ]; then
cat <<RB

  # O Gantry não tem assinatura da Apple (a GonService não usa a conta paga
  # de desenvolvedor). Sem isto o macOS bloqueia a primeira abertura com
  # "não foi possível verificar o desenvolvedor". Tira a marca de "baixado
  # da internet" só deste app, que a pessoa pediu para instalar.
  # Roda na caixa de areia do Homebrew: só pode escrever no Gantry.app.
  postflight_steps do
    run "/usr/bin/xattr",
        args:           ["-dr", "com.apple.quarantine", "{{appdir}}/Gantry.app"],
        writable_paths: ["Gantry.app"],
        writable_base:  :appdir
  end
RB
fi
cat <<RB

  # Só com --zap: apaga máquinas, projetos e preferências salvas. Os
  # containers e as VMs continuam nas máquinas.
  zap trash: [
    "~/Library/Application Support/gantry",
    "~/Library/Caches/com.wails.Gantry",
    "~/Library/Preferences/com.wails.Gantry.plist",
    "~/Library/WebKit/com.wails.Gantry",
  ]
end
RB
