# Testes do Gantry

## Automáticos

| Comando | O que cobre | Precisa de |
|---|---|---|
| `make test` | Unidade e API inteira contra um engine falso; driver do nerdctl com CLI falso; compose, x-gantry, x-vms, métricas, Loki/Prometheus; operações em segundo plano, conexão direta do agente (TLS mútuo, recusa de certificado não pareado), métricas de VMs no agente, serviços compartilhados, backup, segredos, versões das imagens (registro falso) e detecção do WSL | só Go |
| `make e2e` | Ciclo de vida de um lab em cada engine real encontrado, lab misto, VM, criação e edição de containers; com `GANTRY_E2E_KUBECONFIG`, as telas do cluster com pods de verdade (logs que seguem, shell, encaminhar porta, escalar, reiniciar, voltar versão, esvaziar nó) | os engines instalados; um cluster (kind, k3d) |
| `python3 tests/ui_smoke.py '<link>'` | Todas as telas, nos dois temas, sem erro de JavaScript | Playwright e o app rodando com `-web` |

Exemplos:

```bash
make e2e                                          # Docker/Podman daqui
GANTRY_E2E_SSH=ssh://usuario@127.0.0.1:2222 make e2e   # inclui a VM de teste
GANTRY_E2E_VM_IMAGE=debian-12 make e2e            # inclui uma VM QEMU (baixa ~350 MB)
GANTRY_E2E_ONLY=podman make e2e                   # só um host
kind create cluster && GANTRY_E2E_KUBECONFIG=~/.kube/config make e2e   # inclui o Kubernetes
```

No GitHub, cada push na `main` roda os testes com `-race`, todas as telas (Playwright), o ciclo de projetos no Docker do runner e as telas do cluster num kind (`.github/workflows/ci.yml`).

## Antes de cada versão (15 minutos)

O que o CI não alcança: o app de verdade no Mac, SSH e VMs.

- [ ] `make run` abre a janela; a versão do rodapé é a nova
- [ ] Esta máquina: os engines aparecem com o estado certo
- [ ] Um servidor SSH conecta (com a chave com senha, se for o caso) e lista os projetos
- [ ] Criar projeto pelo modelo Postgres + Adminer, subir, abrir o Adminer, Remover
- [ ] Num projeto com `build:`, mudar o código e Reconstruir
- [ ] Logs e shell de um container local e de um do servidor
- [ ] Um projeto com VM sobe, a VM aparece com console e SSH
- [ ] Um cluster real (o k3s-lab ou o do trabalho): visão geral, detalhes de um pod, logs, shell
- [ ] Configuração → Geral → Ver diagnóstico abre sem erro
- [ ] Fechar e abrir de novo: a janela volta no mesmo tamanho e lugar

O roteiro completo, abaixo, é para quando uma área inteira mudar.

## Roteiro manual no Mac

Marque conforme for testando. Se algo falhar, anote o que apareceu na tela ou no painel de baixo.

### 1. Build e abertura

- [ ] `make run` compila e abre o app, com o ícone do guindaste e a janela sem barra de título
- [ ] O tema claro/escuro alterna e fica salvo ao reabrir

### 2. Esta máquina

- [ ] A tela lista os engines instalados (Podman, OrbStack, Docker Desktop, Colima) com o estado certo
- [ ] Iniciar/Parar de um engine mostra a saída no painel e o host aparece/some da barra lateral
- [ ] Ocultar um host detectado e mostrar de novo pela seção Hosts ocultos

### 3. Servidor SSH (um servidor Linux ou a VM de teste)

- [ ] Adicionar `ssh://…`; chave com senha: o app pergunta a frase; senha do servidor: o app pergunta
- [ ] Cancelar a senha mostra o erro e Atualizar tenta de novo
- [ ] Podman sem socket: aparece o botão para ativar e ele resolve
- [ ] Editar o host (nome, socket, compose) e remover

### 3b. Máquinas e modelos

- [ ] Ao abrir o app, os servidores aparecem como "não conectado" e nenhum pedido de senha surge; clicar num deles conecta
- [ ] Um servidor com Docker e Podman (ou nerdctl) aparece uma vez na barra lateral, com uma sub-aba por engine
- [ ] Página da máquina: CPU, memória e disco batem com o `top`/`df` (no Mac, com o Monitor de Atividade); atualizam a cada 5 s
- [ ] Aba VMs lista as VMs dos projetos; Console e SSH funcionam
- [ ] Últimos projetos: o que você acabou de subir aparece primeiro; clicar abre o projeto
- [ ] Desconectar fecha o SSH (a máquina volta a "não conectado")
- [ ] Com muitos containers (`for i in $(seq 40); do docker create --name t$i alpine; done`): a lista pagina, o filtro acha `t1`, trocar para 50 por página vale para as outras listas, e o filtro não perde o foco enquanto a tela atualiza
- [ ] A barra lateral só tem as máquinas (sem menu de modelos)
- [ ] Em Criar projeto, os modelos que você guardou aparecem primeiro como "seu"; o × exclui
- [ ] Salvar como modelo na página de um projeto: o modelo aparece em Criar projeto

### 3c. Criar projeto

- [ ] A página da máquina abre na aba Projetos; Criar projeto pergunta containers, VM ou os dois
- [ ] Containers no Podman colando `podman run -d --name web -p 8080:80 -e COR=${COR:-azul} nginx:alpine`: Converter mostra o serviço; no passo 3 aparece a variável COR; o projeto sobe no Podman e vira um modelo
- [ ] Colar um docker-compose seu (ou Abrir arquivo…) no Docker
- [ ] VM por modelo (VM Ubuntu com Docker): sobe e o Docker está instalado (SSH na aba VMs)
- [ ] VM setup próprio com Debian 13, 2 CPUs, porta 8081:80 e o exemplo Nginx: a página abre em http://localhost:8081
- [ ] VM por ISO (um .iso de instalação): a VM sobe, Tela abre o Compartilhamento de Tela com a senha, e dá para instalar
- [ ] VM + containers (Postgres + setup próprio): os dois aparecem no projeto; Parar e Subir valem para ambos
- [ ] Outras formas → Container avulso abre o formulário completo

### 3d. Lab completo de observabilidade (no Mac)

Preparar uma vez:

```bash
brew install podman qemu
podman machine init --now          # se ainda não tiver
```

- [ ] Esta máquina mostra o IP do Mac na faixa de hardware
- [ ] Esta máquina → Criar projeto → VM + containers → Modelo pronto → Lab de observabilidade, engine Podman
- [ ] Passo 3: portas já em uso (ex.: 9090 de um cluster kind) aparecem trocadas pela próxima livre, com o motivo embaixo do campo
- [ ] Preencha GRAFANA_SENHA; Gerar projeto. A VM baixa o Debian na primeira vez (alguns minutos) e instala Nginx e node_exporter no primeiro boot
- [ ] De outro aparelho na mesma rede (celular): http://IP-do-Mac/ mostra "Olá do Gantry" (Traefik → Nginx da VM)
- [ ] http://localhost:8090: painel do Traefik com a rota nginx e o serviço nginx-vm verde
- [ ] http://localhost:9090/targets (ou a porta escolhida): 5 alvos UP — prometheus, node (podman), node (vm-web), nginx e traefik
- [ ] http://localhost:3000 (admin + a senha): pasta Gantry com Máquinas, Prometheus, Nginx e Traefik
- [ ] Rode `for i in $(seq 200); do curl -s localhost >/dev/null; done`: Nginx e Traefik mostram as requisições; Traefik mostra códigos e tempo de resposta
- [ ] Parar a VM: o Traefik marca o backend como fora e conta os 503
- [ ] Na página do projeto: 5 containers no Podman e 1 VM; Parar e Subir valem para tudo

### 3d2. Modelos novos

- [ ] Containers: Kafka + Kafka UI (tópico criado na UI em :8085), Vault (entra com o token em :8200), Keycloak (admin em :8180), WordPress (instalação em :8086), VS Code no navegador (:8443), Jupyter (:8888 com o token), Ollama + Open WebUI (:3080; baixe um modelo pequeno)
- [ ] VMs: Debian 13; PostgreSQL (`psql -h localhost -p 15432 -U postgres`); Podman (adicionar como host ssh:// na porta SSH da VM); LAMP (http://localhost:8088/info.php)
- [ ] Lab de observabilidade: http://IP-do-Mac/grafana abre o Grafana pelo Traefik; :3000/grafana também
- [ ] Aba Arquivo do lab: escolher traefik/dinamico.yml, mudar e Salvar e reiniciar (reinicia só o traefik); prometheus.yml reinicia só o prometheus

### 3d3. Aplicações, rede, exportar e alertas

- [ ] Projeto com Grafana: a faixa Abrir mostra o Grafana (`/grafana`); as portas web nas listas viram links ↗; Copiar link traz o IP da máquina; a porta de um banco não aparece
- [ ] Página do container: botão Abrir e o endereço na rede
- [ ] Aba VMs da máquina sem a rede: aparece o comando para ativar. No Mac: `brew install socket_vmnet && sudo brew services start socket_vmnet`; Verificar de novo mostra "ativa (socket_vmnet, 192.168.105.0/24)"
- [ ] Modelo Rede: VMs e containers: as VMs mostram o IP na coluna Rede; Logs do container verificador mostram a página da VM web; na VM cliente, `cat /root/resposta-do-web.html`
- [ ] Modelo Cluster k3s: `sudo k3s kubectl get nodes` no servidor mostra 3 nós Ready
- [ ] Exportar um projeto (com e sem .env) e Importar pacote em outra pasta: sobe igual
- [ ] Enviar para… o servidor Linux: o projeto aparece lá e sobe; Criar projeto com Máquina de destino = o servidor Linux confere as portas lá
- [ ] Lab de observabilidade: parar a VM → em ~1 min o alerta VMWebFora aparece no topo da máquina e do projeto; Silenciar some; Configuração → Mostrar de novo

### 3d4. Acesso às VMs e containers

- [ ] VM rodando: Shell abre um terminal dentro do Gantry como `gantry` (sudo sem senha); `top`, `vim` e redimensionar a janela funcionam
- [ ] Logs da VM: `journalctl -f` ao vivo; `logger teste` no Shell aparece nos Logs
- [ ] Logo depois de criar a VM, Shell avisa que o SSH ainda não está pronto; Console mostra o boot
- [ ] Clicar na porta SSH copia o comando; ele funciona no Terminal do Mac
- [ ] VM num servidor (servidor Linux): Shell e Logs funcionam pelo túnel
- [ ] Containers: Shell e Logs na lista do projeto e na página do container

### 3e. Métricas de containers e VMs

- [ ] Página da máquina → Containers: CPU e memória ao vivo de cada container rodando
- [ ] Página da máquina → VMs: CPU, memória e disco de cada VM rodando (aparecem uns segundos depois de abrir); clicar no nome abre as métricas do projeto
- [ ] Projeto com VM e containers → Métricas: os containers e a "VM web" aparecem na legenda, nos gráficos e na tabela (com a coluna Disco)
- [ ] Na VM, rode `yes > /dev/null` pelo SSH: a CPU da VM sobe para perto de 100% na lista e no gráfico
- [ ] Página de um container rodando: bloco Uso com CPU e memória de agora e os gráficos; o período (15 min a 24 h) fica salvo
- [ ] Servidor SSH com VM: as mesmas métricas aparecem (o app roda um comando curto por SSH a cada 10 s enquanto a tela está aberta)

### 4. Projetos

- [ ] Abrir pasta com compose sobe o lab; ele aparece na árvore com os containers
- [ ] Shell, Logs, Parar, Iniciar, Reiniciar e Remover em um container
- [ ] Aba Arquivo: editar no app e salvar; editar no Nvim e ver a tela atualizar; conflito quando os dois editam
- [ ] Serviços × arquivo mostra o que mudou; Salvar e aplicar recria só o serviço alterado
- [ ] Parar, Reiniciar e Remover o lab inteiro (inclusive com o compose movido de pasta)

### 4b. Criar pela interface (avulsos)

- [ ] Pasta que já tem compose: o app recusa
- [ ] Container avulso (aba Containers do engine) com porta e volume; ele aparece na lista rodando
- [ ] Criar volume, Criar rede (com sub-rede) e Baixar imagem (o progresso aparece no painel)

### 4c. Como no Portainer

- [ ] Clicar no nome de um container abre a página dele; Pausar, Retomar e Matar funcionam
- [ ] Conectar o container a uma rede com IP fixo e desconectar
- [ ] Novo container com portas, variáveis em texto (.env), limite de memória 256m, 0.5 CPU, NET_ADMIN e restart "quando falhar"; a página mostra tudo
- [ ] Editar e recriar mudando uma variável: o container volta rodando com o mesmo nome; o inspect mostra o valor novo
- [ ] Editar com uma imagem que não existe: dá erro e o container antigo continua rodando
- [ ] Duplicar (mude a porta do host antes de criar)
- [ ] Construir imagem com o Dockerfile no editor; e com uma pasta do host que tenha `.dockerignore`
- [ ] Imagens: Usar abre o formulário com a imagem; Nome… cria outra tag
- [ ] Criar volume com opções (ex.: `type=tmpfs`, `device=tmpfs`) e rede com faixa de IPs
- [ ] Lab do git com um repositório seu; Arquivo → Repositório git → Atualizar do git depois de um push
- [ ] Aba Arquivo: trocar para `.env`, salvar e aplicar; o container recebe a variável

### 5. Imagens, volumes e redes

- [ ] As três abas listam, mostram quem usa, removem e limpam o que está sem uso

### 6. Agente (servidor Linux)

- [ ] Agente antigo (protocolo 1) no servidor Linux: a página do host pede Atualizar agente uma vez; depois de atualizado, não pede mais a cada versão do app
- [ ] Instalar agente com a conexão direta marcada: a saída mostra o pareamento e "ok: o app fala com o agente e com o engine direto por TLS"
- [ ] Página do host: Conexão = direta em <ip>:7781 · engine direto; banner "Conectado direto por TLS"
- [ ] Bloquear a 7781 no servidor (sudo ufw deny 7781/tcp): o app continua funcionando pelo SSH e a página diz que a porta não respondeu; liberar e Atualizar volta para TLS
- [ ] Desligar e ligar a conexão direta pela página do host
- [ ] Fechar o app por alguns minutos, reabrir: a aba Métricas mostra o período em que ele esteve fechado, para containers e VMs
- [ ] Editar o compose no servidor: a aba Arquivo atualiza na hora (Observando: pelo agente)
- [ ] Remover o agente: as VMs dos projetos do servidor continuam lá

### 6b. Operações em segundo plano

- [ ] Subir um projeto com imagem grande (Paperclip): aba embaixo com a barra de download (MB de GB, %, camadas) e o log do compose ao vivo
- [ ] Cancelar no meio do download: termina como cancelado e o app segue usável
- [ ] Recarregar a página (Cmd+R) durante uma operação: a aba volta e continua
- [ ] Gerar projeto, Importar pacote e Enviar para… com "subir": abrem a mesma aba

### 6c. Serviços compartilhados

- [ ] Aba Serviços do Mac: homelab-postgres aparece (PostgreSQL, porta 5432)
- [ ] Gerar o modelo Paperclip: a seção Serviços da máquina já escolhe o homelab-postgres; as variáveis PG_* aparecem como "do serviço compartilhado"
- [ ] Gerar: banco e usuário "paperclip" criados no homelab-postgres (psql -l), o projeto sobe e a aba Serviços mostra "usado por paperclip"
- [ ] Desmarcar "criar banco e usuário": usa o login do próprio serviço
- [ ] Aba Serviços de um projeto: Abrir psql entra logado com o usuário do projeto; Usar neste projeto… grava PG_* no .env (senha no chaveiro) e mostra os dados para copiar
- [ ] Novo serviço compartilhado… → Redis: abre o modelo Redis com o nome compartilhado-redis; depois de gerar, aparece na aba Serviços

### 6d. Backup

- [ ] Abrir o app: apresentação com o nome, a barra de carregar e "by GonService" (≥ 1 s)
- [ ] Aba Backup: o primeiro Fazer backup agora abre o Finder para escolher a pasta; o arquivo aparece lá com volumes, dump e arquivos; Trocar… muda a pasta
- [ ] Restaurar de um arquivo… com um backup de outra pasta
- [ ] Exportar abre o Finder para escolher onde salvar; Importar pacote tem Escolher… para a pasta
- [ ] Apagar dados no container, Restaurar: os dados voltam
- [ ] Agenda a cada 6 h mantendo 2: depois de rodar, só os 2 mais novos ficam
- [ ] Backup de um projeto do servidor Linux (os volumes vêm pelo engine, pelo TLS ou SSH)

### 6e. Segredos no chaveiro

- [ ] Gerar um projeto com senha (Postgres): o .env fica com "# POSTGRES_SENHA: no chaveiro do Gantry" e o projeto sobe
- [ ] Acesso ao Chaveiro (app do macOS): item "Gantry: <máquina>/<projeto>/<VAR>"
- [ ] Aba Arquivo: Devolver ao .env e Mover para o chaveiro de novo
- [ ] Projeto no servidor Linux com segredo: sobe; no servidor, ps durante o up não mostra a senha
- [ ] Enviar para outra máquina com o .env marcado: sobe lá com os segredos

### 6f. Versões das imagens

- [ ] Projeto do modelo Prometheus + Grafana: Versões das imagens mostra a mais nova da linha e a de outra versão principal
- [ ] Usar: grava no compose; num modelo com VERSAO_* grava no .env; Atualizar imagens + Subir aplica

- [ ] Imagem com tag `latest` já baixada: depois de publicarem outra, aparece "nova imagem em :latest"; a lista de projetos mostra "1 atualização"
- [ ] Projeto de um repositório git com commit novo no remoto: aba Arquivo mostra "1 commit novo"
- [ ] Rodapé: com uma release mais nova no GitHub aparece "↑ x.y.z"; clicar na versão verifica de novo

### 6j. Pastas e Configuração

- [ ] Engrenagem → Pastas: criar "Cliente" e a subpasta "Produção"; Máquinas: mover um servidor para Produção; a barra lateral mostra a árvore
- [ ] Apagar uma pasta com algo dentro não deixa; vazia, apaga
- [ ] 🔍 de uma pasta filtra só nela; a busca do topo mostra a pasta de cada resultado
- [ ] Adicionar máquina já escolhendo a pasta

### 6n. Boas-vindas e Ferramentas

- [ ] Com a pasta de configuração vazia (`-config /tmp/novo/config.json`), o app abre nas boas-vindas; marcar um servidor e um cluster e Adicionar os marcados: eles entram na barra lateral
- [ ] Criar o Postgres de teste abre o Gerar projeto com a senha já preenchida; Gerar e subir funciona
- [ ] Esta máquina → Ferramentas: o que está instalado aparece com o caminho; num Mac sem o QEMU, Instalar roda `brew install qemu` no painel
- [ ] Num servidor, Ferramentas mostra os comandos com `sudo` da distribuição dele
- [ ] Um projeto com VM numa máquina sem QEMU mostra o aviso no topo

### 6m. Log e diagnóstico

- [ ] A pasta de configuração tem `logs/gantry.log`, `formato.json` e `copias/diaria-<hoje>`
- [ ] Subir um projeto: o log mostra "operação" e "operação concluída" com a duração
- [ ] Configuração → Geral → Ver diagnóstico: máquinas sem endereço, clusters, engines e o fim do log; Copiar e Salvar… funcionam; Abrir pasta do log abre o Finder

### 6l. Homebrew

- [ ] Até uma hora depois da tag (ou rodando o workflow Gantry do GonService/homebrew-tap): a release `gantry-vX.Y.Z` aparece lá e o `Casks/gantry.rb` foi atualizado
- [ ] `brew install --cask gonservice/tap/gantry` instala e o app abre sem o aviso de desenvolvedor não verificado
- [ ] Com uma versão mais nova publicada, o aviso do rodapé mostra `brew upgrade --cask gonservice/tap/gantry` com Copiar

### 6k. Clusters Kubernetes

- [ ] Engrenagem → Clusters → Adicionar: os contextos do `~/.kube/config` aparecem com o servidor e o tipo de login; adicionar um, ele entra na barra lateral em modo Normal
- [ ] Visão geral: nós com CPU e memória (se houver metrics-server), pods com problema mostram o motivo (ex.: ImagePullBackOff)
- [ ] Pods: Logs ao vivo com filtro; ⋯ → Shell abre o terminal direto, sem perguntar o cluster
- [ ] Logs ao vivo abertos por mais de uma hora continuam chegando; `kubectl delete pod` de um pod de Deployment mostra "o pod foi apagado"; matar o processo do container (`kubectl exec … -- kill 1`) mostra "o container reiniciou" e segue
- [ ] Deployments: Escalar para 2 e a coluna Prontos muda; Reiniciar recria os pods
- [ ] Editar YAML de um ConfigMap, salvar; Aplicar YAML… com dois documentos cria os dois
- [ ] Services → Abrir num Service HTTP abre a página no navegador
- [ ] Clicar num pod abre os detalhes ao lado: containers com imagem, variáveis, montagens; Logs e Shell de um container; clicar no Secret de uma variável abre o Secret (só as chaves) e ← volta; Esc fecha
- [ ] Detalhes de um Deployment listam os pods; de um CronJob, os jobs; de um nó, CPU, memória e pods
- [ ] Fechar o grupo Armazenamento no menu, trocar de tela: continua fechado
- [ ] Pods: clicar em Idade põe os mais novos no topo; de novo, os mais velhos; Estado agrupa por estado; trocar de tela e voltar mantém a ordem
- [ ] Namespace: clicar, digitar parte do nome, Enter escolhe; o escolhido aparece em "Usados por último"
- [ ] Deixar a lista de pods aberta sem mudar nada: a tela não pisca (só muda quando algo muda no cluster)
- [ ] Customizados: as CRDs do cluster aparecem por grupo; uma lista mostra as colunas da CRD e o detalhe mostra condições, spec e status
- [ ] Pod com Postgres: ⋯ → Encaminhar porta 5432; conectar um cliente em 127.0.0.1:5432; Portas encaminhadas → Fechar; mudar o cluster para Só leitura fecha as abertas
- [ ] Pod em CrashLoopBackOff: Logs abre na execução anterior (a que morreu); o seletor troca para a atual
- [ ] Deployment: mudar a imagem, Histórico mostra 2 versões; Voltar para esta volta a imagem
- [ ] Nó (num cluster com mais de um): Esvaziar despeja os pods, mostra os que ficaram (DaemonSets) e o nó fica isolado; Liberar volta
- [ ] Helm: abrir uma release, ver values e histórico; com o helm instalado, Voltar para esta
- [ ] Com metrics-server: o detalhe de um pod mostra os gráficos de CPU e memória enchendo a cada 15 s
- [ ] Conta só de leitura (RBAC): Apagar, Editar e Shell somem no detalhe do objeto
- [ ] Fechar o Gantry com a janela num tamanho e lugar; abrir de novo: volta igual
- [ ] Mudar o modo para Só leitura: Secrets mostram `***`, Shell e Apagar não funcionam
- [ ] Criar projeto do modelo Cluster k3s (3 VMs): os agentes sobem depois do servidor; em alguns minutos o cluster aparece sozinho na pasta da máquina com 3 nós; remover o projeto tira o cluster

### 6i. Arquivos alterados

- [ ] Projeto com `build:` (ex.: tradebot): mudar o código, Reconstruir; o container novo roda o código novo. Projeto sem `build:` não mostra o botão

- [ ] Editar o compose de um projeto rodando no seu editor: a página do projeto mostra "Os arquivos do projeto mudaram" com o serviço e Aplicar agora; a lista mostra "arquivos alterados"
- [ ] Mudar a memória de uma VM rodando: Aplicar agora pergunta se reinicia a VM
- [ ] Aba Arquivo → Aplicar sozinho: editar e salvar no editor aplica em ~20 s; um YAML quebrado não é aplicado

### 6h. Containers e imagens

- [ ] Página do container → Arquivos: navegar, Baixar um arquivo e uma pasta (.tar), Enviar arquivo e pasta, Apagar
- [ ] Recursos…: mudar CPU e memória com o container rodando; o resumo mostra os novos limites
- [ ] Eventos do container e da máquina; Criar imagem… aparece em Imagens
- [ ] Logs: filtro, período, Hora e Salvar…
- [ ] Imagens → ⋯: Camadas, Salvar .tar…; Mais → Carregar .tar…
- [ ] Mais → Registros: adicionar ghcr.io com token; Enviar ao registry… uma imagem; Login em <máquina>

### 6g. Windows (WSL2)

- [ ] Windows com WSL2 e Docker ou Podman numa distribuição: a máquina "WSL Ubuntu" aparece
- [ ] Abrir: o agente é instalado sozinho; containers, criar projeto, subir, shell e logs funcionam
- [ ] VMs com KVM (Windows 11 com virtualização aninhada) e a bridge gantry0

### 7. Labs mistos

- [ ] `examples/observabilidade` com OrbStack (ou Docker Desktop) e Podman ligados: Prometheus e Loki no Podman, Grafana no Docker
- [ ] O lab aparece inteiro nos dois hosts, com o selo do engine em cada container
- [ ] Aba Topologia: colunas por engine, redes, portas no host, setas entre engines

### 8. VMs

- [ ] `examples/vm-nginx`: a VM sobe (HVF), Console mostra o boot, `http://localhost:8081` responde "Olá do Gantry"
- [ ] SSH copia o comando e ele entra na VM
- [ ] Parar, Iniciar, Reiniciar e Remover a VM
- [ ] Gerenciar…: snapshot com a VM rodando e voltar a ele (sem reiniciar); apagar snapshot
- [ ] Aumentar disco; Clonar (a cópia entra no compose e sobe); Iniciar junto com a máquina (LaunchAgent no Mac)
- [ ] Criar projeto → VM → Importar disco… (VMDK/OVA) e tela: true
- [ ] Backup com "Incluir os discos das VMs" e restaurar

### 9. Observabilidade

- [ ] `examples/monitoramento` (Docker): o bloco Observabilidade acha Prometheus, Loki e Grafana
- [ ] Aba Métricas com a fonte Prometheus; consulta PromQL desenha o gráfico
- [ ] Logs de um container: Loki · 1 h mostra o histórico
- [ ] Dashboards do Grafana abrem no navegador

### 10. containerd e k3s (servidor Linux com nerdctl)

- [ ] Com o agente instalado, a página do host lista "containerd (nerdctl)" em Outros engines; Adicionar como host funciona
- [ ] Containers do nerdctl: shell, logs, parar/iniciar; um lab com compose (nerdctl compose)
- [ ] k3s: os pods aparecem como labs `k8s-<namespace>`, com logs e shell

### 11. Roadmap 8–12

Kubernetes
- [ ] Deployment com 3 réplicas: Logs dos pods mostra as linhas das 3 com o nome de cada pod em cor; escalar para 4 e a quarta entra sozinha
- [ ] Pod distroless: Debug… com busybox abre o terminal no container efêmero, enxergando os processos do alvo
- [ ] Pod em CrashLoopBackOff, ImagePullBackOff e Pending: o detalhe mostra o diagnóstico com a causa e o próximo passo
- [ ] Busca no cluster: `image:nginx ns:loja` acha os objetos; clicar abre o detalhe
- [ ] Minhas permissões: com uma conta só de leitura, a tabela mostra ✓ só em get/list/watch; um 403 leva a ela
- [ ] Argo CD: lista, Sync, Refresh, auto-sync, Rollback e, com o servidor conectado, o diff
- [ ] Helm: `kubectl scale deploy x --replicas=5` numa release com 2 aparece em Drift só em spec.replicas; objeto apagado aparece como sumiu

Labs de rede
- [ ] Lab IOL com containers clientes (modelo 7j): ping entre os clientes pelo roteador; trocar o cabo em Cabos muda sem reiniciar
- [ ] Capturar um cabo: ao vivo no Gantry, no Wireshark e Salvar .pcap (abre no tcpdump -r)
- [ ] Topologia → Rede do lab: arrastar um nó, fechar e abrir o lab: a posição volta; arrastar de uma porta livre a outra liga o cabo ao vivo; clicar no cabo desliga
- [ ] Estados do lab: configurar OSPF, Salvar estado (com write memory), apagar a config, Restaurar: o OSPF volta
- [ ] Exportar com a configuração (.gantrylab) no Mac e Importar pacote no Fedora: o lab sobe igual
- [ ] Dynamips: c7200 com IOS de verdade sobe, tem console e pinga um IOL; Gerenciar → Calcular idle-PC; o nó ocioso fica abaixo de 10% de CPU

VMs
- [ ] VM Debian com `metrics: true`: em até 1 min a aba Métricas mostra Dentro das VMs; `stress -c 1` na VM aparece em até 30 s
- [ ] Gerenciar → Portas: Publicar 8080:80 numa VM com nginx responde sem reiniciar; Tirar para de responder; porta ocupada dá erro
- [ ] Clonar uma VM NixOS: hostname, `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` e `/etc/machine-id` diferentes da origem; Aplicar Nix numa não marca a outra

Produto
- [ ] Ctrl K: "subir <projeto>" sobe; num cluster, "logs api" abre os logs do Deployment api; "namespace x" troca; só com o teclado
- [ ] Configurações → Assistentes (MCP): ligar, colar o JSON no cliente MCP; "quais pods estão em CrashLoop no cluster X?" responde; com escrita desligada, as ferramentas de escrita não aparecem; cluster só leitura recusa escala
- [ ] Release com os segredos APPLE_*: `spctl -a -vv Gantry.app` diz Notarized Developer ID; baixar o zip e abrir com duplo clique sem aviso
- [ ] Atualizar agora: pelo Homebrew e pelo zip, abre na versão nova com hosts, projetos e clusters
