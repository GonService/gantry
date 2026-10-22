#!/usr/bin/env python3
"""Atualiza a seção de downloads do Gantry nas páginas do homebrew-tap.

    scripts/downloads.py VERSAO arquivo.md [arquivo.en.md ...]

Troca o que estiver entre <!-- downloads:gantry --> e <!-- /downloads:gantry -->
pela tabela com os links da versão. Arquivo terminado em .en.md recebe a
versão em inglês. A pipeline de Release chama isto a cada versão, para os
links diretos (que levam o número da versão no nome) nunca ficarem velhos.
"""
import re
import sys

REPO = "https://github.com/GonService/gantry"
INICIO, FIM = "<!-- downloads:gantry -->", "<!-- /downloads:gantry -->"

TEXTOS = {
    "pt": {
        "sistema": "Sistema",
        "baixar": "Baixar",
        "brew": "macOS, pelo Homebrew",
        "mac": "macOS (Intel e Apple Silicon)",
        "win": "Windows (experimental)",
        "linuxapp": "Linux, app desktop (x86_64)",
        "linux": "Servidor Linux, sem tela (abre no navegador)",
        "atual": "Versão atual",
        "todas": "todas as versões",
    },
    "en": {
        "sistema": "System",
        "baixar": "Download",
        "brew": "macOS, with Homebrew",
        "mac": "macOS (Intel and Apple Silicon)",
        "win": "Windows (experimental)",
        "linuxapp": "Linux, desktop app (x86_64)",
        "linux": "Headless Linux server (opens in the browser)",
        "atual": "Current version",
        "todas": "all versions",
    },
}


def bloco(versao: str, idioma: str) -> str:
    t = TEXTOS[idioma]
    base = f"{REPO}/releases/download/v{versao}"
    srv = f"gantry-servidor-{versao}-linux"
    return "\n".join(
        [
            INICIO,
            f"| {t['sistema']} | {t['baixar']} |",
            "|---|---|",
            f"| {t['brew']} | `brew install --cask gonservice/tap/gantry` |",
            f"| {t['mac']} | [Gantry-{versao}-macos.zip]({base}/Gantry-{versao}-macos.zip) |",
            f"| {t['win']} | [Gantry-{versao}-windows.zip]({base}/Gantry-{versao}-windows.zip) |",
            f"| {t['linuxapp']} | [Gantry-{versao}-linux-amd64.tar.gz]({base}/Gantry-{versao}-linux-amd64.tar.gz) |",
            f"| {t['linux']} | [amd64]({base}/{srv}-amd64.tar.gz) · [arm64]({base}/{srv}-arm64.tar.gz) |",
            "",
            f"{t['atual']}: **{versao}** · [{t['todas']}]({REPO}/releases)",
            FIM,
        ]
    )


def main() -> int:
    if len(sys.argv) < 3 or not re.fullmatch(r"\d+\.\d+\.\d+", sys.argv[1]):
        print(__doc__)
        return 2
    versao = sys.argv[1]
    padrao = re.compile(re.escape(INICIO) + r".*?" + re.escape(FIM), re.S)
    for caminho in sys.argv[2:]:
        idioma = "en" if caminho.endswith(".en.md") else "pt"
        try:
            with open(caminho, encoding="utf-8") as f:
                texto = f.read()
        except FileNotFoundError:
            print(f"aviso: {caminho} não existe no homebrew-tap", file=sys.stderr)
            continue
        if not padrao.search(texto):
            print(f"aviso: {caminho} não tem a seção {INICIO}", file=sys.stderr)
            continue
        novo = padrao.sub(lambda _: bloco(versao, idioma), texto, count=1)
        with open(caminho, "w", encoding="utf-8") as f:
            f.write(novo)
        print(f"✓ {caminho} → {versao}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
