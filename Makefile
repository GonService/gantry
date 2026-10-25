# Gantry — atalhos de build
#
#   make            compila o app desktop (no Mac, build/bin/Gantry.app; no Linux, build/bin/gantry)
#   make run        compila e abre o app
#   make linux      app desktop para Linux e o pacote dist/Gantry-VERSAO-linux-ARQ.tar.gz (num Linux com o WebKitGTK 4.1)
#   make dev        roda o app desktop com recarga automática (wails dev)
#   make web        compila a versão de navegador (dist/gantry)
#   make run-web    roda a versão de navegador
#   make release    binários de navegador para Linux e macOS (amd64/arm64)
#   make agents     binários do gantry-agent (Linux amd64/arm64) embutidos no app
#   make ui         compila a interface React (frontend/ → web/ui/); os outros alvos já rodam este
#   make ui-watch   recompila a interface a cada mudança (use junto com make run-web)
#   make test       go vet + testes de unidade e da API
#   make e2e        testes de ponta a ponta contra os engines desta máquina (veja docs/testes.md)
#   make e2e-real   VM de verdade e Dynamips/IOL com as imagens reais (veja docs/testes.md)
#   make deps       instala o Wails e baixa as dependências
#   make clean      apaga build/ e dist/

VERSION  ?= 0.50.1
APP      := Gantry
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOBIN    := $(shell go env GOPATH)/bin
WAILS    := $(GOBIN)/wails
ARCH     := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.DEFAULT_GOAL := desktop
.PHONY: desktop run dev web run-web release test e2e e2e-real deps clean check agents ui ui-watch linux
UNAME    := $(shell uname)

check:
	@command -v go >/dev/null || { echo "Go não encontrado. Instale com: brew install go"; exit 1; }
	@if [ "$$(uname)" = "Darwin" ]; then \
		xcode-select -p >/dev/null 2>&1 || { echo "Faltam as Command Line Tools. Rode: xcode-select --install"; exit 1; }; \
	fi

$(WAILS): | check
	go install github.com/wailsapp/wails/v2/cmd/wails@latest

deps: $(WAILS)
	@grep -q 'wailsapp/wails/v2 ' go.mod || go get github.com/wailsapp/wails/v2@latest
	go mod tidy

# A interface em React (frontend/) é compilada para web/ui/, que o binário
# embute junto com o resto de web/. Precisa do Node (brew install node).
ui:
	@command -v npm >/dev/null || { echo "Node não encontrado. Instale com: brew install node"; exit 1; }
	@cd frontend && { [ -d node_modules ] && [ node_modules/.package-lock.json -nt package-lock.json ] || npm ci --no-audit --no-fund; } && npm run build

ui-watch:
	cd frontend && npm run dev

# O app carrega o agente para instalar nos servidores via SSH.
AGENT_ARCHES := amd64 arm64
agents: check
	@for a in $(AGENT_ARCHES); do \
		echo "-> gantry-agent linux/$$a"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags '$(LDFLAGS)' -o internal/agentbin/bin/gantry-agent-linux-$$a ./cmd/gantry-agent || exit 1; \
		gzip -9f internal/agentbin/bin/gantry-agent-linux-$$a; \
	done

ifeq ($(UNAME),Linux)
desktop: linux

run: linux
	@pkill -x gantry 2>/dev/null || true
	./build/bin/gantry &
else
desktop: deps agents ui
	$(WAILS) build -clean -skipbindings -tags desktop -ldflags '-X main.version=$(VERSION)' -platform darwin/$(ARCH)
	@echo
	@echo "Pronto: build/bin/$(APP).app"

run: desktop
	@# Fecha o app aberto: senão o macOS só traz a janela antiga para frente.
	@osascript -e 'quit app "$(APP)"' >/dev/null 2>&1 || true
	@i=0; while pgrep -x $(APP) >/dev/null && [ $$i -lt 30 ]; do sleep 0.3; i=$$((i+1)); done; pkill -x $(APP) 2>/dev/null || true
	open build/bin/$(APP).app
endif

# O app desktop para Linux: a janela é o WebKitGTK 4.1 (Fedora: dnf install
# webkit2gtk4.1-devel gtk3-devel; Debian/Ubuntu: apt install
# libwebkit2gtk-4.1-dev libgtk-3-dev). O pacote leva o programa, o atalho do
# menu, o ícone e o instalar.sh (instala para o usuário, sem sudo).
LINUX_PKG := Gantry-$(VERSION)-linux-$(ARCH)
linux: check agents ui
	@pkg-config --exists webkit2gtk-4.1 gtk+-3.0 || { echo "Falta o WebKitGTK 4.1 de desenvolvimento (Fedora: sudo dnf install webkit2gtk4.1-devel gtk3-devel; Debian/Ubuntu: sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev)"; exit 1; }
	@mkdir -p build/bin
	CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 -ldflags '$(LDFLAGS)' -o build/bin/gantry .
	@rm -rf dist/$(LINUX_PKG) && mkdir -p dist/$(LINUX_PKG)
	cp build/bin/gantry packaging/linux/gantry.desktop packaging/linux/instalar.sh dist/$(LINUX_PKG)/
	@if command -v convert >/dev/null; then convert build/appicon.png -resize 256x256 dist/$(LINUX_PKG)/gantry.png; else cp build/appicon.png dist/$(LINUX_PKG)/gantry.png; fi
	tar -czf dist/$(LINUX_PKG).tar.gz -C dist $(LINUX_PKG)
	@echo
	@echo "Pronto: build/bin/gantry e dist/$(LINUX_PKG).tar.gz"

dev: deps agents ui
	$(WAILS) dev -tags desktop

web: check agents ui
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/gantry .
	@echo "Pronto: dist/gantry"

run-web: web
	./dist/gantry

release: check agents ui
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "-> $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/gantry-$$os-$$arch . || exit 1; \
	done

test: check ui
	cd frontend && npm run typecheck
	go vet ./...
	go test ./...

e2e: check
	go test -tags e2e -count=1 -v -timeout 30m ./e2e/

# Só os testes com as coisas reais: uma VM (GANTRY_E2E_VM_IMAGE=debian-12) e
# os equipamentos de rede com as imagens de verdade (GANTRY_E2E_C7200,
# GANTRY_E2E_IOL: caminho da imagem). Sem a variável, o teste é pulado.
e2e-real: check
	go test -tags e2e -count=1 -v -timeout 60m -run 'TestVMReal|TestNetworkReal' ./e2e/

clean:
	rm -rf build/bin dist
	find web/ui -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
	rm -f internal/agentbin/bin/*.gz
