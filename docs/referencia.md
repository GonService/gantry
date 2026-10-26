# Gantry: referência

[English](reference.md) · **Português** · [Guia passo a passo](guia/README.md) · [Voltar ao início](../README.pt-BR.md)

App desktop para gerenciar máquinas locais e remotas e o que roda nelas: containers (Docker, Podman, containerd), VMs QEMU e os projetos que juntam os dois. É um binário só.

O Gantry conversa direto com a API do Docker ou do Podman, sem nada hospedado e sem agente obrigatório nos servidores. O estado vem do próprio engine, então o que você cria pelo terminal (`docker run`, `docker compose up`, `podman compose up`) aparece na interface na hora. Os projetos são os seus arquivos `compose.yaml`; o app não inventa outro formato, só duas extensões (`x-gantry` e `x-vms`).

## O que faz hoje

- **Máquinas**
  - A barra lateral lista as máquinas (esta e os servidores), não os containers. Um servidor com Docker, Podman e containerd aparece uma vez só.
  - **Pastas e subpastas** organizam a barra lateral (por cliente, ambiente…). A pasta mostra quantas máquinas tem e quantas estão com problema, mesmo fechada; a 🔍 dela filtra só lá dentro; pastas grandes abrem resumidas (as com problema primeiro) com "ver todas". A busca do topo procura em todas e mostra em que pasta está cada uma.
  - A **engrenagem** ao lado da versão abre a Configuração: **Máquinas** (adicionar, editar, remover, escolher a pasta), **Pastas** (criar, renomear, mover, apagar; só apaga pasta vazia), **Credenciais** (registros de imagens, senhas de SSH desta sessão, onde ficam os segredos) e **Geral** (tema, idioma, versão).
  - Servidores remotos só conectam quando você abre: o app não abre SSH para todos ao iniciar. **Desconectar** fecha a conexão.
  - A página da máquina mostra o hardware e o uso de CPU, memória e disco, lidos por um script curto (local ou via SSH, sem agente). Depois vêm as abas:
    - **Projetos** (a aba inicial), do mais recente para o mais antigo, com **Criar projeto**;
    - **Containers**, com uma sub-aba por engine (Docker, Podman, containerd…) e, em cada uma, Containers, Imagens, Volumes e Redes;
    - **VMs** de todos os projetos, com tela (VNC), console, SSH, iniciar, parar e remover;
    - **Serviços**: bancos e filas que outros projetos podem usar (veja Serviços compartilhados);
    - **Configuração** (conexão, outros engines, observabilidade, agente) e, nesta máquina, **Engines** (instalar, iniciar e parar).
  - Listas longas têm filtro e paginação, com 10, 25, 50 ou 100 por página. Tudo é clicável.
- **Clusters Kubernetes** (as funções do Lens, dentro do Gantry)
  - Os clusters aparecem na barra lateral, nas pastas, junto das máquinas. Os de fora entram pela engrenagem → **Clusters** (um contexto do `~/.kube/config` ou um kubeconfig colado); os criados por projetos do Gantry entram sozinhos.
  - A página do cluster tem Visão geral (nós com CPU e memória, pods com problema e o motivo, avisos recentes), as listas de workloads, rede, configuração e armazenamento, nós, namespaces, eventos e releases do Helm, com filtro por namespace.
  - Clicar num item abre os detalhes ao lado, como no Lens. Num pod: estado, nó, IPs, dono, labels e, por container, imagem, estado, reinícios, portas, comando, variáveis (as de Secret aparecem só como referência), montagens, recursos e uso, probes, com Logs e Shell de cada container; mais volumes e eventos. Deployments, Jobs, CronJobs, Services e nós mostram os pods ou jobs ligados a eles; ConfigMaps mostram os dados; Secrets mostram só as chaves.
  - O menu do cluster tem grupos que abrem e fecham (o app lembra).
  - As listas ordenam por qualquer coluna: clique no título (de novo, inverte). Por padrão, por nome; os eventos, pelos mais recentes. O app lembra a ordem de cada tipo.
  - Recursos customizados (CRDs: Certificates do cert-manager, Applications do Argo CD…) entram no menu em **Customizados**, com as colunas que o autor da CRD definiu e o detalhe com condições, spec e status. Se a conta não pode listar os CRDs, os tipos vêm da descoberta da API, sem essas colunas.
  - **Encaminhar porta** num pod ou Service abre uma porta desta máquina (127.0.0.1) que leva até ele, como `kubectl port-forward` (para conectar um DBeaver num Postgres do cluster, por exemplo); as portas abertas ficam em **Portas encaminhadas**, no topo do cluster.
  - Logs da **execução anterior** de um container (o que morreu num CrashLoopBackOff); num pod nesse estado, Logs já abre nela.
  - Logs ao vivo não param sozinhos: quando o cluster (ou um balanceador, um proxy, a rotação do arquivo de log) encerra a conexão, o Gantry reconecta a partir da última linha, sem repetir nenhuma. Se o container reinicia, o log segue na execução nova com um aviso; só acaba quando o pod termina ou é apagado.
  - Deployments mostram o **histórico de versões** com **Voltar para esta** (kubectl rollout undo); nós podem ser **esvaziados** (drain: isola e despeja os pods, respeitando os PodDisruptionBudgets).
  - Releases do **Helm** abrem com histórico, values, notas e manifest; **Voltar para esta** roda o `helm` desta máquina.
  - Gráficos de CPU e memória da última hora para pods, workloads e nós (o Gantry anota o uso a cada 15 s enquanto o cluster está aberto; precisa do metrics-server).
  - O Gantry pergunta ao cluster o que a sua conta pode fazer e esconde os botões que ela não pode usar.
  - Em clusters grandes, as listas são lidas uma vez, em páginas, e depois acompanhadas por watch, em vez de lidas inteiras a cada atualização.
  - Ações: logs e shell dos pods, escalar e reiniciar deployments, executar e suspender CronJobs, isolar nós, ver e editar o YAML, apagar, ver valores de Secrets, **Aplicar YAML…** e **Abrir** um Service no navegador.
  - **Diagnóstico** num pod com problema: o que está errado agora (não pronto, probes falhando desde o início atual, reinícios) e desde quando.
- **Criar projeto** (um assistente em três passos; todo projeto termina num compose no formato do Gantry)
  1. O que o projeto tem: **containers**, uma **máquina virtual** ou **VM + containers**.
  2. Containers: o engine (Docker, Podman, containerd) e a origem: um **modelo**, um **docker-compose colado** (ou aberto de um arquivo) ou **comandos `docker run`, `podman run` ou `nerdctl run`**, convertidos em compose (portas, volumes, variáveis, rede, limites, reinício, healthcheck…; o que não tem equivalente vira aviso).
     VM: um **modelo** (Debian, Ubuntu com Docker, k3s, Rocky, instalação por ISO…) ou um **setup próprio**: sistema (Debian, Ubuntu, Rocky, Alma, NixOS, uma imagem qcow2 sua ou uma **ISO**), CPUs, memória, disco, portas, pacotes, **script de inicialização** com exemplos prontos (Docker, Podman, Nginx, k3s, fuso horário, usuário com senha) e cloud-init avançado.
  3. Revisar: o YAML editável com o resumo ao lado, as variáveis viram campos (e vão para o `.env`), nome e pasta. Por padrão o projeto também é **guardado como modelo**, para gerar de novo igual.
  - Em **Outras formas**: container avulso (o formulário completo), projeto de um repositório git, abrir uma pasta com compose, importar pacote e importar do PNETLab/EVE-NG.
  - VMs por ISO começam com o disco vazio e iniciam pelo instalador; **Tela** abre o VNC (com senha, pelo túnel SSH nos servidores) no visualizador do sistema.
- **Modelos** (como os Custom Templates do Portainer, sem menu próprio: aparecem em Criar projeto)
  - Um modelo é um `compose.yaml` com containers em qualquer engine (`x-gantry`) e VMs (`x-vms`). Variáveis como `${SENHA:?informe}` e `${PORTA:-8080}` viram campos ao gerar o projeto; os valores vão para o `.env`.
  - Os do Gantry: Em branco, Nginx, Postgres, Postgres + Adminer, MySQL, MariaDB, MongoDB, Redis, RabbitMQ, Kafka + Kafka UI, MinIO, Prometheus + Grafana, Uptime Kuma, Vault, Keycloak, Gitea, n8n, Mailpit, WordPress, VS Code no navegador, Jupyter, Ollama + Open WebUI, Paperclip (usa um Postgres que já existe), Web + banco, e as VMs Debian 12 e 13, Ubuntu com Docker, k3s, Rocky, Nginx, PostgreSQL, RabbitMQ, Podman, LAMP, NixOS e por ISO, o cluster k3s (3 VMs) e os labs de rede Cisco (switches, roteadores, campus, IOL, Dynamips).
  - **Lab de observabilidade** (VM + containers): Traefik na porta 80 levando ao Nginx de uma VM Debian (qualquer aparelho da rede acessa pelo IP da máquina), com Prometheus, Grafana, node_exporter e o exporter do Nginx em containers (Podman por padrão). Os dashboards Máquinas, Prometheus, Nginx e Traefik já vêm provisionados no Grafana. O painel do Traefik fica em :8090. No Mac, o node_exporter em container mede a VM do Podman (`podman machine`); para medir o próprio macOS, rode `brew install node_exporter` e descomente o alvo `mac` no `prometheus.yml`.
  - Modelos podem trazer arquivos junto do compose (configs, dashboards), gravados na pasta do projeto.
  - Os seus: cada projeto criado pode ser guardado como modelo (vem marcado), e **Salvar como modelo** na página de um projeto faz o mesmo. Eles aparecem primeiro em Criar projeto, marcados como "seu", e o × os exclui.
- **Recursos avulsos**
  - **Container avulso** (aba Containers de cada engine), para casos pontuais, num formulário com abas como o do Portainer:
    - comando, entrypoint, diretório, usuário e console (`-it`);
    - portas (IP, porta do host, do container e protocolo) ou `-P`;
    - volumes, pastas do host e tmpfs;
    - rede, IP fixo, hostname, domínio, MAC, DNS e entradas extras no `/etc/hosts`;
    - variáveis (em linhas ou como texto de `.env`) e labels;
    - política de reinício, limites de memória e CPU, `/dev/shm`;
    - privilegiado, init, só leitura, capacidades, dispositivos, sysctls e driver de log;
    - engine (em hosts com mais de um) e "sempre baixar a imagem".
  - **Criar volume** (driver, opções do driver, labels), **Criar rede** (driver, sub-redes IPv4 e IPv6, gateway, faixa de IPs, interna, anexável, opções e labels), **Baixar imagem** e **Construir imagem** (Dockerfile escrito no app, pasta do host como contexto respeitando o `.dockerignore`, ou repositório git; com build args, target, sem cache), nas sub-abas de cada engine.
  - Imagens: **Usar** abre o formulário de container já com a imagem, **Nome…** dá outra tag, e no **⋯** há **Camadas** (o histórico do build), **Enviar ao registry…** (push) e **Salvar .tar…**. Em **Mais**, **Carregar .tar…** (`docker load`) e **Registros (logins)…**.
  - **Registros privados**: o login (ghcr.io, Docker Hub, um registry seu) fica no chaveiro e o Gantry o usa para baixar e enviar imagens, inclusive ao subir projetos. **Login em <máquina>** grava o login também na linha de comando dela (`docker login --password-stdin`), para o compose e o terminal de lá.
- **Projetos**
  - Os containers são agrupados pelo projeto do Compose, a partir das labels que o próprio Compose grava.
  - Containers fora de qualquer compose aparecem em "Avulsos".
  - Ações no projeto:
    - Subir (`up -d`) e atualizar imagens (`pull`) usam o compose.
    - Parar, reiniciar e remover vão direto pela API do engine. Por isso funcionam mesmo se o arquivo foi movido.
    - Toda operação roda em segundo plano, numa aba embaixo com a saída ao vivo, a barra de download das imagens (bytes, porcentagem e camadas) e **Cancelar**. Dá para seguir usando o app; se a página recarregar, as operações em andamento voltam a aparecer.
  - O `compose.yaml` (e os outros arquivos do projeto, incluindo o `.env`) pode ser editado na interface e é relido do disco a cada 3 s. Se você editar no Nvim, a tela acompanha; se editar nos dois ao mesmo tempo, o app avisa do conflito.
  - Mostra quais serviços e VMs estão diferentes do arquivo (o hash do `compose config` nos containers; a definição com que cada VM foi iniciada nas VMs).
  - Quando os arquivos mudam (pelo Gantry, pelo seu editor ou por um `git pull`), a página do projeto avisa: **Os arquivos do projeto mudaram**, com o que muda em cada serviço e VM e o botão **Aplicar agora**. A lista de projetos marca "arquivos alterados". VMs rodando com a definição antiga só reiniciam se você marcar.
  - **Aplicar sozinho** (aba Arquivo, desligado por padrão): aplica quando os arquivos param de mudar por alguns segundos. Arquivo com erro não é aplicado, e VMs nunca reiniciam sozinhas.
- **Containers**
  - Ações: iniciar, parar, reiniciar, pausar, retomar, matar e remover.
  - Página de cada container (clique no nome): resumo, portas, volumes, redes, variáveis, labels, limites e o inspect em JSON.
  - Conectar e desconectar redes, com IP fixo opcional.
  - **Editar e recriar** e **Duplicar**, como no Portainer: o formulário abre com as opções atuais. Na edição, o container atual fica de lado até o novo subir; se algo falhar, ele volta.
  - Shell interativo e logs em tempo real, com **filtro de linhas**, período (15 min a 24 h, ou tudo), hora de cada linha e **Salvar…** num arquivo.
  - CPU e memória atualizadas a cada 5 s.
  - **Arquivos**: navega pelas pastas do container (com ele rodando e com `sh`), **baixa** arquivos (pastas vêm como `.tar`), **envia** arquivos e pastas deste computador e apaga. Baixar e enviar funcionam com o container parado.
  - **Recursos…**: muda CPU, memória, memória reservada, máximo de processos e a política de reinício na hora, sem recriar. Vale no Docker, no Podman (a política de reinício só a partir do 5.1) e no containerd (`nerdctl update`).
  - **Eventos**: o que aconteceu com o container (criado, iniciado, encerrou com código, sem memória, recursos alterados…), em 1 h, 24 h ou 7 dias. A página da máquina tem **Eventos** de tudo (containers, imagens, volumes e redes).
  - **Criar imagem…**: grava o estado atual do container numa imagem nova (`docker commit`).
- **Labs mistos e topologia**
  - Com a extensão `x-gantry`, um mesmo compose divide os serviços entre Docker e Podman (veja abaixo).
  - Aba Topologia em cada lab: engines, redes, portas publicadas no host e as dependências que atravessam engines.
  - **Notas no desenho** (`x-notas` no compose): botão direito no fundo › Nova nota. Texto com **negrito**, *itálico*, `# títulos`, `- listas`, tabelas (`| a | b |`) e imagens da pasta do projeto (`![](notas/foto.png)`), em cinco cores ou sem fundo. Arraste para mover, pelo canto para mudar o tamanho, dois cliques para editar. As caixas de texto de um lab do PNETLab ou do EVE-NG viram notas no mesmo lugar, com as tabelas e imagens.
- **VMs no lab**
  - Com a extensão `x-vms`, o lab também cria máquinas virtuais no QEMU da máquina do host (veja abaixo).
  - **Gerenciar…** em cada VM: **snapshots** (com a VM rodando guardam também a memória; voltar retoma sem reiniciar), **aumentar o disco**, **clonar** (a cópia entra no compose sem as portas publicadas) e **iniciar junto com a máquina** (LaunchAgent no Mac, systemd do usuário ou crontab no Linux).
  - **Importar disco…** no Criar projeto converte OVA, VMDK, VDI, VHD(X) ou raw para qcow2; para sistemas sem cloud-init, `tela: true` liga o VNC.
  - O backup do projeto inclui os discos das VMs (opcional), copiados de um snapshot com a VM rodando.
- **Portas livres**
  - Em Criar projeto, as variáveis de porta (`${PORTA:-80}:80`) são conferidas com a máquina: uma porta já em uso (por um container de qualquer engine ou outro programa) vira a próxima livre, com o motivo embaixo do campo. No Podman sem root em Linux, portas abaixo de 1024 viram 8000 + a porta (80 → 8080).
  - Se mesmo assim o projeto não subir por porta ocupada, a saída diz quem usa a porta e qual variável trocar no .env.
  - A página da máquina mostra o IP dela na rede local.
- **Aplicações web**
  - Portas publicadas que respondem HTTP ou HTTPS (de containers e de VMs) viram links: a faixa Abrir no topo do projeto, as portas nas listas e a página do container. Abrir usa o túnel SSH quando o host é um servidor.
  - Copiar link dá o endereço pelo IP da máquina, para abrir em outro aparelho da rede. Um container com o label `gantry.path` (ex.: `/grafana`) abre direto nesse caminho.
- **Serviços compartilhados**
  - Um serviço compartilhado é só um container de PostgreSQL, MySQL/MariaDB, Redis, RabbitMQ ou MongoDB rodando na máquina com a porta publicada: o Gantry o encontra sozinho, seja de um projeto do Gantry ou não (o seu `homelab-postgres`, por exemplo).
  - Para criar um, **Novo serviço compartilhado…** (na aba Serviços da máquina ou do projeto) escolhe o tipo e abre o modelo dele já com o nome; depois é só gerar.
  - A aba **Serviços** da máquina lista todos, a porta e quais projetos usam cada um. A aba **Serviços** de cada projeto mostra os que ele usa (endereço, banco e usuário), os dele próprio e os outros da máquina, com **Abrir psql** (ou mysql, redis-cli, mongosh) já logado com o usuário do projeto e **Usar neste projeto…**, que cria banco e usuário próprios e grava a conexão no `.env` (PG_HOST, PG_PORTA, PG_USUARIO, PG_BANCO; senha e URL no chaveiro).
  - Um modelo declara o que pode usar de fora com `x-gantry: usa:` (o do Paperclip já vem assim). Ao gerar o projeto, o assistente oferece um serviço existente e, por padrão, cria um **banco e um usuário só para o projeto**, com senha nova (no RabbitMQ, um vhost). As variáveis do modelo são preenchidas sozinhas.
    ```yaml
    x-gantry:
      usa:
        postgres: {host: PG_HOST, porta: PG_PORTA, usuario: PG_USUARIO, senha: PG_SENHA, banco: PG_BANCO}
    ```
  - O projeto chega no serviço por `host.containers.internal` e a porta publicada; o compose do modelo mapeia esse nome com `extra_hosts: ["host.containers.internal:host-gateway"]`, que vale no Docker e no Podman.
- **Backup**
  - Aba **Backup** em cada projeto: copia os volumes (pela API do engine, então vale para servidores também), um dump de cada banco que o projeto roda (`pg_dumpall`, `mysqldump`/`mariadb-dump`, `mongodump`) e a pasta do projeto, num `.tar.gz` **na pasta que você escolher** deste computador (o app pergunta no primeiro backup e lembra por projeto; **Trocar…** muda). **Restaurar de um arquivo…** aceita um backup de qualquer pasta.
  - Opções: parar os containers durante a cópia dos volumes (cópia consistente) e incluir o `.env`.
  - **Restaurar** remove os containers, recria os volumes com o conteúdo do backup e sobe de novo (os arquivos do projeto voltam se você marcar). Os dumps ficam no arquivo para restaurar à mão em outro banco.
  - **Backup automático**: a cada 6 h, 12 h, dia ou semana, mantendo os últimos N. Roda enquanto o Gantry estiver aberto.
- **Segredos no chaveiro**
  - Senhas e tokens do `.env` (variáveis com SENHA, PASS, SECRET, TOKEN, KEY…) podem ficar no **Chaveiro do macOS** (ou no Secret Service no Linux; sem chaveiro, num arquivo só seu na pasta de configuração). No `.env` fica só um comentário.
  - Ao gerar um projeto, isso já vem marcado. Num projeto existente, a aba Arquivo tem **Mover para o chaveiro** e **Devolver ao .env**.
  - O app entrega os valores ao compose na hora: no próprio computador pelo ambiente do processo e nos servidores pela entrada padrão do shell, então não aparecem no `.env` nem na lista de processos do servidor. Enviar para outra máquina leva os segredos junto quando você marca o `.env`.
- **Versões das imagens**
  - Os modelos do Gantry usam versões fixas (nada de `latest`); os poucos que seguem uma tag móvel a tiram de uma variável.
  - Projetos com serviços que têm `build:` ganham o botão **Reconstruir**: constrói as imagens de novo e recria os containers (`compose up -d --build --force-recreate`). É o que usar depois de mudar o código, já que o compose em si não mudou.
  - A aba Containers do projeto mostra, para cada imagem com versão, a mais nova da mesma linha (ex.: 11.1.0 → 11.6.3) e a de outra versão principal (→ 12.0.2), consultando o registro (Docker Hub, ghcr.io, quay.io…). **Usar** grava a versão no compose ou no `.env` (quando uma variável define a tag); depois é só Atualizar imagens e Subir.
  - Para tags sem versão (`latest`, `main`…), compara a imagem da máquina com a do registro e avisa "nova imagem em :latest".
  - A lista de projetos marca os que têm atualização. Projetos de um repositório git mostram, na aba Arquivo, quantos commits novos há no remoto.
  - Nada é atualizado sozinho: o app só avisa.
- **Versões do Gantry**
  - O app consulta as páginas de versões (as releases deste repositório e a cópia delas no homebrew-tap, a cada 6 h) e mostra **↑ versão** no rodapé da barra lateral, com as notas; não baixa nem instala nada. Clicar na versão do rodapé verifica na hora.
- **Exportar, importar e enviar**
  - Exportar gera um `.tar.gz` com a pasta do projeto (o `.env` só se você marcar), no lugar que você escolher. Importar pacote, em Outras formas, abre esse pacote numa pasta nova e sobe. Se a pasta já existir, **Substituir** guarda a antiga ao lado (`<pasta>.antes-<data>`) e tira do Gantry o projeto que a usava, com containers, VMs e a configuração dos equipamentos, antes de abrir o pacote.
  - Enviar para… copia o projeto para outra máquina (deploy remoto) e sobe lá. No Criar projeto, Máquina de destino faz o mesmo para um projeto novo, com as portas conferidas na máquina escolhida.
  - Volumes e discos de VM não vão junto: o projeto recomeça do zero no destino.
- **Alertas**
  - Se houver um Prometheus com regras (o Lab de observabilidade já vem com algumas), os alertas disparados aparecem no topo da máquina e do projeto. Silenciar esconde por máquina; Configuração mostra de novo.
- **Arquivos do projeto**
  - A aba Arquivo edita, além do compose e do .env, os outros arquivos de texto da pasta (prometheus.yml, rotas do Traefik, dashboards…). Salvar e reiniciar reinicia só os serviços que montam aquele arquivo.
- **Métricas**
  - Aba Métricas em cada projeto, com gráficos de CPU e memória por container e por VM (15 min a 24 h) e um resumo em tabela com o disco das VMs. Clicar numa série da legenda destaca ela.
  - Página do container: CPU e memória de agora e o histórico.
  - Listas de containers e VMs com CPU, memória e (nas VMs) disco ao vivo.
  - VMs: o app lê o processo QEMU na máquina (CPU, memória que ele ocupa e espaço do disco), sem nada instalado dentro da VM. No Mac, a memória é a mesma do Monitor de Atividade (conta a parte comprimida). Para métricas de dentro do sistema convidado, use o node_exporter na VM (como no Lab de observabilidade).
  - Sem agente, o app coleta enquanto está aberto (até 6 h). Com o agente, o histórico dos containers e das VMs fica no servidor, mesmo com o app fechado.
- **Agente opcional** (veja abaixo)
- **Observabilidade** (Prometheus, Loki e Grafana; veja abaixo)
- **Imagens, volumes e redes**
  - Abas na página do host com cada item, o que usa cada um e opção de remover.
  - "Limpar sem uso" remove o que nenhum container usa.

- **Onde salvar** — tudo que grava ou abre arquivos neste computador pergunta onde, com o seletor do sistema (Finder no Mac, o do Windows, zenity ou kdialog no Linux): backups, exportar, importar e a pasta de projetos novos nesta máquina (**Escolher…** ao lado do campo). Num servidor via SSH (ou num Linux sem zenity), o Gantry abre o próprio navegador de pastas, que lista as pastas do servidor e cria pastas novas.
- **Boas-vindas** — na primeira vez (sem servidores nem clusters seus), uma tela mostra o que o Gantry achou neste computador, oferece os servidores do `~/.ssh/config` e os clusters do `~/.kube/config` com um clique cada, e um Postgres + Adminer de teste com a senha já criada. Volta por Configuração → Geral → Abrir as boas-vindas.
- **Ferramentas** — aba em cada máquina (esta e os servidores): o que o Gantry usa lá (engine, compose, QEMU, gerador de ISO, curl, aceleração KVM, a ponte da rede do projeto, helm, git, ssh, python3, dynamips), para que serve cada um e o comando de instalação do sistema daquela máquina (Homebrew, apt, dnf, pacman, apk, zypper). No Mac, o que vem pelo Homebrew instala com um botão. A página do projeto e o Criar projeto avisam quando falta algo que o projeto usa (QEMU para as VMs, compose), e um cluster que entra por um plugin que não está instalado (aws, gke-gcloud-auth-plugin, kubelogin…) mostra como instalar.
- **Assistentes de IA (MCP)** — engrenagem › Geral › **Assistentes (MCP)**. Um assistente de IA (um cliente MCP) consulta labs, containers, VMs e clusters por este Gantry, enquanto ele está aberto: hosts, projetos e o que está diferente do arquivo, logs de containers, recursos dos clusters, logs de pods, eventos, releases do Helm. Com permissão de escrita, também sobe e remove projetos, reinicia containers e escala ou reinicia workloads. Secrets e values do Helm nunca saem; clusters só leitura continuam só leitura. O cliente roda `gantry mcp` (o cartão mostra o comando e o arquivo exatos).
- **Abertura** — uma apresentação curta com o nome e o "by GonService", de pelo menos 1 s ou enquanto a lista de máquinas carrega.

## Rodando no macOS

Requer Go 1.22+, o [Wails](https://wails.io) v2 e as ferramentas de linha de comando do Xcode.

```bash
make deps     # instala o wails
make run      # compila e abre o app
```

Outros alvos:

| Alvo | O que faz |
|---|---|
| `make` / `make desktop` | Gera `build/bin/Gantry.app` (no Linux, `build/bin/gantry`) |
| `make dev` | Modo de desenvolvimento do Wails |
| `make web` / `make run-web` | Versão sem janela, que abre no navegador |
| `make linux` | O app desktop do Linux e o pacote |
| `make release` | Builds de distribuição |
| `make agents` | Só os binários do agente (os outros alvos já rodam este) |
| `make test` | Testes |
| `make clean` | Apaga os builds |

O app não é assinado nem notarizado (isso exige uma conta Apple Developer). Na primeira vez, abra com botão direito > Abrir, ou rode `xattr -dr com.apple.quarantine build/bin/Gantry.app`.

## Rodando no Linux

O app desktop do Linux usa o WebKitGTK 4.1 para a janela.

**Instalar com um comando:** `curl -fsSL https://gonservice.github.io/gantry/install.sh | sh` baixa a versão mais nova, confere com o `SHA256SUMS` e instala como abaixo (num Linux sem área de trabalho, ou com `sh -s -- --server`, a versão de servidor em `~/.local/bin`). O mesmo script funciona no macOS (pelo Homebrew, quando existe); no Windows, `irm https://gonservice.github.io/gantry/install.ps1 | iex`. `GANTRY_VERSION=X.Y.Z` escolhe uma versão.

**Instalar uma versão publicada à mão:** baixe `Gantry-VERSAO-linux-amd64.tar.gz` da release, descompacte e rode `./instalar.sh`. Ele instala para o seu usuário, sem sudo: o programa em `~/.local/bin/gantry`, o atalho no menu de aplicativos e o ícone. `./instalar.sh remover` desfaz; os hosts, labs e clusters em `~/.config/gantry` ficam. Se faltar o WebKitGTK, o script diz o comando:

- Fedora: `sudo dnf install webkit2gtk4.1`
- Debian/Ubuntu: `sudo apt install libwebkit2gtk-4.1-0`
- Arch: `sudo pacman -S webkit2gtk-4.1`

**Compilar:** precisa do Go, do Node e dos pacotes de desenvolvimento do WebKitGTK (Fedora: `sudo dnf install webkit2gtk4.1-devel gtk3-devel`; Debian/Ubuntu: `sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev`).

```bash
make linux    # build/bin/gantry e o pacote dist/Gantry-VERSAO-linux-ARQ.tar.gz
make run      # compila e abre
```

No Linux, "esta máquina" é o próprio computador: o Gantry acha o Podman e o Docker dele, e as VMs e os labs de rede rodam ali mesmo.

### Modo navegador

O mesmo binário roda sem janela com `-web` (ou compilado sem a tag `desktop`). Ele mostra um link que vale uma vez (por 10 minutos) e abre o app naquele navegador; usado o link, mostra outro, para outro navegador.

| Flag | Padrão | Descrição |
|---|---|---|
| `-web` | `false` | Abre no navegador em vez da janela. |
| `-addr` | `127.0.0.1:7777` | Endereço de escuta. Se a porta estiver ocupada, usa outra livre. |
| `-config` | ver abaixo | Arquivo de configuração. |
| `-no-browser` | `false` | Não abre o navegador. |
| `-expor-na-rede` | `false` | Permite um `-addr` fora do localhost (prefira um túnel SSH; se usar, ligue o `-tls`). Sem HTTPS, todas as páginas mostram um aviso. |
| `-tls` | `false` | HTTPS com um certificado que o Gantry gera e guarda na pasta da configuração (`tls/`), para os nomes e endereços deste computador. O navegador pede para confiar nele na primeira vez: confira a impressão digital SHA-256 mostrada ao iniciar. |
| `-cert`, `-key` | | HTTPS com o seu certificado e a sua chave (arquivos PEM). |
| `-version` | | Mostra a versão. |

## Hosts remotos

Na interface, use **Adicionar host** com algo como `ssh://homelab` ou `ssh://usuario@servidor:22`.

Como a conexão funciona:

1. O app abre uma conexão SSH mestre (ControlMaster). Senhas e frases da chave são perguntadas pela interface.
2. Um script curto procura o socket do engine no servidor.
3. O app encaminha esse socket para um socket local com `ssh -O forward`.
4. A mesma conexão serve para rodar o compose e ler e gravar os arquivos dos labs.

O usuário remoto precisa ter acesso ao socket. No Docker, isso significa estar no grupo `docker`. No Podman rootless, o `podman.socket` do usuário precisa estar ativo; o app oferece o botão para ativá-lo.

## containerd e k3s

Esses runtimes não têm a API do Docker, só a linha de comando. O Gantry roda um tradutor (shim) que atende a parte da API do Docker que o app usa e chama o `nerdctl` por baixo. Assim, as mesmas telas funcionam com eles: labs, containers, logs, shell, CPU e memória, imagens, volumes e redes.

| Endereço | Runtime |
|---|---|
| `nerdctl://default` | containerd pelo `nerdctl`, no namespace `default` |
| `nerdctl://ns?address=/run/x.sock&sudo=1` | outro namespace, outro containerd e, se preciso, `sudo -n` |
| `k3s://` | o containerd do k3s (namespace `k8s.io`); os pods aparecem como labs `k8s-<namespace>` |

No Linux, o app encontra o `nerdctl` e o k3s sozinho, usando `sudo -n` quando ele funciona sem pedir senha.

Nos servidores, quem faz a tradução é o agente: ao iniciar, ele publica `nerdctl.sock` e `k3s.sock` ao lado do próprio socket. Na página do host, a lista **Outros engines neste servidor** mostra esses engines (e o Podman, se houver um Docker também), com o botão **Adicionar como host**.

Limites:

- Labs com compose usam o `nerdctl compose`.
- O shell funciona por um terminal criado com o `script` do sistema, sem ajuste de tamanho.
- Parar containers do k3s não adianta, porque o kubelet os recria.

## Clusters Kubernetes

O Gantry fala direto com a API do Kubernetes (sem `kubectl`), com as credenciais do kubeconfig: certificados, token, usuário e senha, ou um plugin como o `gke-gcloud-auth-plugin` e o `aws` do EKS.

Todas as conexões, inclusive o shell dos pods, passam pelo proxy do kubeconfig (`proxy-url`) ou pelo `HTTPS_PROXY` do sistema. O plugin de login roda uma vez só mesmo com várias telas pedindo ao mesmo tempo, e a lista de tipos do cluster é lida de novo a cada 10 minutos (ou logo, quando um tipo novo, como um CRD recém-instalado, ainda não aparece).

Para adicionar um cluster: engrenagem → **Clusters** → **Adicionar cluster**. Dá para escolher um contexto do `~/.kube/config` (o app lê o arquivo de novo quando ele muda) ou colar um kubeconfig, que fica guardado numa cópia só do app. Um kubeconfig colado que roda um programa, lê arquivos deste computador, manda as conexões por um proxy (`proxy-url`) ou não confere o certificado do cluster (`insecure-skip-tls-verify`) só entra depois de você confirmar, com cada um desses pontos listado.

Cada cluster tem um modo, escolhido na Configuração:

| Modo | O que deixa fazer |
|---|---|
| **Normal** (padrão) | tudo o que a sua conta pode fazer no cluster, sem perguntar qual é o cluster; só ações como apagar ou reiniciar pedem confirmação da própria ação |
| **Só leitura** | ver tudo, menos o valor dos Secrets; sem shell, sem encaminhar portas, sem mudanças |

No modo só leitura também não dá para encaminhar portas, e mudar um cluster para só leitura fecha as que estavam abertas. Os values e o manifest das releases do Helm (que costumam ter senhas) não aparecem.

**Abrir** num Service cria um túnel local (127.0.0.1, porta aleatória) pelo proxy da API, sem `port-forward`. O túnel só aceita quem abriu pelo app e fecha quando o cluster é removido ou muda.

### Projetos que criam um cluster

Um projeto com VMs declara onde está a API:

```yaml
x-gantry:
  kubernetes: {vm: k3s-server, porta: 6443}

x-vms:
  k3s-server:
    ports: ["6443:6443"]
    ...
  k3s-agente-1:
    depois: k3s-server     # só sobe quando o servidor responde por SSH
    ...
```

Ao subir o projeto, as VMs sobem na ordem do `depois` (um ciclo dá erro). Quando a API responde, o Gantry lê o `k3s.yaml` (ou o `admin.conf`) da VM, aponta para a porta publicada e coloca o cluster na barra lateral, na pasta da máquina, em modo Normal. Na primeira vez o k3s leva alguns minutos para instalar. Remover as VMs do projeto tira o cluster também.

O modelo **Cluster k3s (3 VMs)** já vem assim.

## Labs mistos (`x-gantry`)

Um lab continua sendo um compose comum. A extensão `x-gantry`, ignorada pelas ferramentas de compose, diz em qual engine cada serviço roda:

```yaml
x-gantry:
  runtime: podman        # padrão do lab (sem isso, o engine do host)

services:
  prometheus:
    image: prom/prometheus
    ports: ["9090:9090"]
  grafana:
    image: grafana/grafana
    ports: ["3000:3000"]
    depends_on: [prometheus]
    x-gantry:
      runtime: docker    # só este serviço vai para o Docker
```

Ao subir o lab, o Gantry:

- separa os serviços por engine;
- sobe primeiro o engine de que os outros dependem;
- roda o compose uma vez para cada engine, com `DOCKER_HOST` apontando para o socket certo e `--no-deps`.

Parar, reiniciar e remover passam por todos os engines. O lab aparece inteiro, com o engine de cada container, na página de qualquer um dos dois hosts.

Requisitos e limites:

- Os dois engines precisam estar na mesma máquina. No Mac, são dois engines locais (OrbStack ou Docker Desktop, mais a `podman machine`). No servidor SSH, o app acha sozinho os sockets do Docker e do Podman.
- O ideal é ter o `docker compose` instalado, porque ele fala com os dois engines. Sem ele, o app usa o `podman compose` para os serviços do Podman.
- Engines diferentes não compartilham redes. Um serviço só alcança outro de outro engine pela porta publicada no host (`host.docker.internal` no Mac, ou `extra_hosts: ["host.docker.internal:host-gateway"]`). A aba Topologia avisa quando falta uma porta.
- A aba Métricas mostra só os containers do engine principal do host.

Um exemplo completo está em `examples/observabilidade`: Prometheus e Loki no Podman, Grafana no Docker, com os datasources já configurados.

## VMs no lab (`x-vms`)

O mesmo compose pode declarar máquinas virtuais. Elas rodam no QEMU da máquina do host: no Mac com HVF, num servidor Linux com KVM (ou em emulação, mais lenta, se o KVM não estiver disponível).

```yaml
x-vms:
  web:
    image: debian-12          # debian-13, ubuntu-24.04, ubuntu-22.04, uma URL ou um caminho para .qcow2
    cpus: 1
    memory: 1g
    disk: 8G
    ports: ["8081:80"]        # porta do host → porta da VM
    packages: [nginx]         # instalados pelo cloud-init no primeiro boot
    run:                      # comandos no primeiro boot
      - echo ok > /var/www/html/ok.txt
    cloud-init: |             # opcional: mais cloud-config, junto com o resto
      timezone: America/Sao_Paulo
```

Ao subir o lab, as VMs sobem antes dos containers. Para cada VM, o app:

- baixa a imagem uma vez para `~/.cache/gantry/images`;
- cria um disco próprio que só guarda as diferenças em relação à imagem;
- gera o cloud-init com o usuário `gantry` e uma chave SSH só das VMs;
- inicia o QEMU em segundo plano.

Tudo de uma VM fica em `~/.local/state/gantry/vms/<lab>/<vm>/`. Num servidor por SSH, o script que controla as VMs e os equipamentos vai uma vez e fica em `~/.cache/gantry/vmctl-<hash>.sh` (o hash é o do conteúdo: uma versão nova do app grava a dela; as que ficam um mês sem uso saem); cada comando depois só chama o arquivo. Na aba Containers, a seção Máquinas virtuais tem:

- Shell: um terminal na VM dentro do Gantry (SSH com a chave das VMs; num servidor, pelo túnel SSH);
- Logs: os logs do sistema da VM ao vivo (`journalctl -f`, ou o syslog onde não há journald);
- Console: a saída serial, que mostra o boot e o cloud-init antes do SSH estar pronto;
- clicar na porta SSH copia o comando `ssh` para usar num terminal seu;
- Parar, Iniciar, Reiniciar e Remover. Remover apaga o disco.

Os containers têm Shell e Logs (stdout e stderr ao vivo) nas listas e na página de cada um.

Com `depois: <outra VM>`, a VM só sobe quando a outra já responde por SSH (útil para os nós de um cluster esperarem o servidor).

Parar desliga a VM pelo ACPI; se ela não responder, o QEMU é encerrado. Reiniciar aplica mudanças de CPU, memória e portas. Pacotes, `run` e `cloud-init` só valem para uma VM nova, então remova a VM e suba o lab de novo.

A rede de cada VM é o NAT do QEMU: a VM alcança o host em `10.0.2.2`, e os containers alcançam a VM pelas portas publicadas. A porta de SSH é escolhida a partir do nome do lab e da VM, ou fixada com `ssh: 2230`.

Requisitos na máquina do host:

- No Mac: `brew install qemu`.
- No Fedora: `sudo dnf install qemu-kvm qemu-img genisoimage`.
- No Debian ou Ubuntu: `sudo apt install qemu-system qemu-utils genisoimage`.

Exemplo: `examples/vm-nginx` sobe uma VM Debian com Nginx em `http://localhost:8081`.

### Equipamentos de rede (IOL, Dynamips, VPCS)

Roteadores e switches Cisco (IOL e Dynamips) e computadores VPCS também vão em `x-vms`, ligados por cabos. Placas, NPE, memória, configuração inicial, imagens, o que instalar na máquina e a importação de labs do PNETLab e do EVE-NG estão em [docs/equipamentos-de-rede.md](equipamentos-de-rede.md).

### Rede do projeto (`lan: true`)

Com `lan: true` numa VM (ou `x-gantry: {lan: true}` para todas), ela ganha uma segunda placa numa rede da máquina, com IP fixo (de `.100` a `.250`). A primeira placa continua igual: internet, SSH e portas publicadas.

- As VMs do projeto se acham pelo nome (o Gantry escreve o `/etc/hosts` delas a cada boot).
- Os containers do projeto acham as VMs pelo nome (`http://web/`): ao subir, o Gantry gera `.gantry/rede.yaml` com `extra_hosts` e passa ao compose junto com o seu arquivo, que não é alterado.
- As VMs alcançam os containers pelas portas publicadas, no IP da máquina nessa rede (nomes dos serviços e `gantry-host` também estão no `/etc/hosts`).
- A coluna Rede, nas listas de VMs, mostra o IP de cada uma.

A rede é criada uma vez por máquina, como administrador. A aba VMs da máquina mostra se ela existe e o comando para criar:

- **Mac:** `brew install socket_vmnet && sudo brew services start socket_vmnet` (o mesmo usado pelo Lima). Rede `192.168.105.0/24`.
- **Linux e WSL2:** uma bridge `gantry0` (`10.213.0.0/24`) e o `qemu-bridge-helper` liberado para ela. O comando sugerido também cria um serviço do systemd para a bridge voltar depois de reiniciar.
- **Windows:** pelo WSL2 (veja abaixo).

**Modo bridge.** A aba VMs da máquina › **Modo da rede…** escolhe entre a rede compartilhada acima e a **bridge**: as VMs ficam na mesma rede da máquina (a da sua casa ou do escritório), e qualquer aparelho dela chega nelas pelo IP. Use quando a compartilhada não funcionar, por exemplo com uma VPN como o Cloudflare WARP. O Gantry dá a cada VM um endereço fixo de uma faixa que você escolhe (por padrão `.200` a `.230`), fora do DHCP do roteador, e pula os endereços que respondem na rede. As VMs que já existem só mudam de rede depois de removidas e criadas de novo.

- **Mac:** um segundo socket_vmnet, em bridge com a interface (`en0`), como serviço do launchd; a aba VMs mostra o comando (precisa de administrador uma vez). Funciona também pelo Wi-Fi.
- **Linux:** uma bridge do sistema com a placa de rede (por exemplo `br0`, criada no NetworkManager ou no netplan) e `allow br0` em `/etc/qemu/bridge.conf`; a aba VMs mostra o que falta.

Modelos que usam a rede: **Cluster k3s (3 VMs)** e **Rede: VMs e containers**. Uma VM que já existia só entra na rede depois de removida e recriada.

## Observabilidade

Se houver um Prometheus, um Loki ou um Grafana perto do host, o Gantry usa:

- **Prometheus:** a aba Métricas dos labs ganha a fonte Prometheus, com CPU e memória do cAdvisor por container e um campo para qualquer consulta PromQL.
- **Loki:** a janela de logs de cada container ganha um seletor (Ao vivo, ou Loki com 1 h, 6 h ou 24 h) para ver o histórico.
- **Grafana:** a página do host lista os dashboards e abre qualquer um no navegador.

O app encontra esses serviços entre os containers da máquina do host, pela imagem e pela porta publicada. Também dá para informar os endereços em **Observabilidade › Configurar**, na página do host, inclusive um token de service account do Grafana.

Os endereços são vistos da máquina do host. Num servidor SSH, `http://127.0.0.1:9090` é o Prometheus do próprio servidor: o app chega nele pela mesma conexão SSH e, para abrir o Grafana no navegador, encaminha uma porta local.

Para os logs, o Loki precisa dos labels `container` (ou `container_name`, ou `compose_project` com `compose_service`).

O exemplo `examples/monitoramento` sobe tudo para o Docker de uma máquina:

- Prometheus e cAdvisor;
- Loki e Alloy, que coleta os logs dos containers;
- Grafana, com os datasources e um dashboard por lab já configurados.

## Agente (opcional)

Hosts SSH funcionam sem nada instalado no servidor. O agente é um serviço pequeno (`gantry-agent`, cerca de 6 MB) que acrescenta:

- 24 h de histórico de CPU e memória dos containers **e das VMs**, coletado a cada 15 s mesmo com o app fechado e salvo em disco;
- aviso imediato (inotify) quando o compose de um lab muda no disco, em vez de ler o arquivo a cada 3 s;
- a **conexão direta**: TCP com TLS mútuo, sem passar pelo SSH (veja abaixo).

Para instalar, use **Instalar agente** na página do host. O app:

1. Descobre a arquitetura do servidor (Linux amd64 ou arm64) e envia o binário embutido nele para `~/.local/bin`.
2. Se a conexão direta estiver marcada (padrão), pareia o agente com este app (abaixo).
3. Cria o serviço `~/.config/systemd/user/gantry-agent.service`, ativa e roda `loginctl enable-linger`, para o agente seguir rodando sem sessão aberta. Sem systemd do usuário, inicia com `nohup`.
4. Encaminha o socket do agente (`$XDG_RUNTIME_DIR/gantry/agent.sock`) pela conexão SSH, que fica como reserva.

O agente tem uma versão de protocolo própria: o app só pede para atualizar quando precisa de algo que o agente instalado não tem, e não a cada versão nova do app. **Remover** apaga o serviço, o binário, o histórico e o pareamento; as VMs dos projetos (que ficam na mesma pasta de estado) não são tocadas.

### Conexão direta (TCP + TLS mútuo)

- Cada lado tem o próprio certificado (ECDSA P-256, gerado na máquina dele; as chaves privadas nunca saem de lá). O pareamento roda pelo SSH: o app envia o certificado dele e recebe o do agente. Daí em diante, cada lado só aceita o certificado do outro (fixado), com TLS 1.3.
- O agente escuta na porta 7781. O app usa a conexão direta para a API do agente e para a do engine (containers, logs, shell, estatísticas). Comandos (compose, arquivos, VMs) continuam pelo SSH.
- Se a porta não responder (firewall), o app volta sozinho para o SSH e tenta de novo depois de um minuto. A página do host mostra por onde está falando e tem **Ligar/Desligar conexão direta**.

## Windows (WSL2)

O app para Windows (`Gantry-X.Y.Z-windows.zip` nas releases) é experimental: ainda não foi testado num Windows de verdade.

- No Windows, cada distribuição do WSL2 aparece como uma máquina (`wsl://Ubuntu`), sem SSH: os comandos rodam pelo `wsl.exe` e o app mantém a distribuição ligada enquanto estiver aberto.
- No primeiro uso o app instala e pareia o agente dentro da distribuição, escutando só em `127.0.0.1:7781` (o WSL2 repassa para o Windows); é por ele que o app fala com o Docker ou Podman de dentro do WSL.
- VMs (`x-vms`) usam o QEMU da distribuição, com KVM quando o Windows 11 tem virtualização aninhada; a rede do projeto é a bridge `gantry0`, como num Linux.
- O Docker Desktop (distribuições `docker-desktop*`) é ignorado; instale Docker ou Podman na sua distribuição. Servidores via SSH a partir do Windows ainda não têm suporte (o OpenSSH do Windows não tem ControlMaster).

## Testes

```bash
make test        # testes de unidade e da API (engine e CLIs falsos), sem precisar de engine
make e2e         # ciclo de vida completo contra os engines reais desta máquina
```

O `make e2e` testa cada engine que o app encontra (Docker, Podman, containerd), passando por:

- subir um lab;
- logs, shell e estatísticas;
- topologia;
- parar e iniciar;
- remover.

Também testa um lab misto, quando há Docker e Podman. Variáveis opcionais:

- `GANTRY_E2E_SSH=ssh://servidor` inclui um servidor;
- `GANTRY_E2E_VM_IMAGE=debian-12` inclui uma VM;
- `GANTRY_E2E_IMAGE` troca a imagem dos testes;
- `GANTRY_E2E_ONLY` limita os hosts testados.

`make e2e-real` roda os testes com as coisas reais, no lugar dos programas falsos dos testes de unidade: uma VM no QEMU (`GANTRY_E2E_VM_IMAGE=debian-12`: o servidor web que ela instala responde na porta publicada, as amostras do processo do QEMU, o node_exporter de dentro dela, uma porta acrescentada e tirada com ela rodando) e os equipamentos de rede com as imagens de verdade (`GANTRY_E2E_C7200` e `GANTRY_E2E_IOL`, caminho das imagens: a configuração inicial entra, a linha errada de propósito aparece como recusada, um VPCS pinga o roteador e, no Dynamips, o idle-PC é calculado). No GitHub, o workflow **Testes reais** roda a VM com KVM toda segunda, antes de cada versão e quando você pede; os equipamentos de rede rodam só na sua máquina, porque as imagens da Cisco não podem ir para o GitHub.

A interface tem um teste rápido que abre todas as telas nos dois temas e falha se houver erro de JavaScript: `tests/ui_smoke.py`, que usa o Playwright. O roteiro de testes manuais no Mac está em [testes.md](testes.md).

## Releases e Homebrew

O repositório é `GonService/gantry`. Cada tag `vX.Y.Z` roda o GitHub Actions (`.github/workflows/release.yml`), que testa, compila e publica a release com:

- `Gantry-X.Y.Z-macos.zip`: o app do Mac (Apple Silicon e Intel no mesmo arquivo, sem assinatura);
- `Gantry-X.Y.Z-windows.zip`: o app do Windows (experimental, sem assinatura);
- `Gantry-X.Y.Z-linux-amd64.tar.gz`: o app desktop do Linux (x86_64), com o `instalar.sh`;
- `gantry-servidor-X.Y.Z-linux-<amd64|arm64>.tar.gz`: a versão de servidor (sem janela; a interface abre no navegador), para rodar num Linux sem tela;
- `SHA256SUMS`.

A release sai aqui, com a tag `vX.Y.Z`. O [GonService/homebrew-tap](https://github.com/GonService/homebrew-tap) busca a release sozinho (o workflow Gantry de lá, a cada hora ou rodado à mão, só com o token dele): copia para lá com a tag `gantry-vX.Y.Z` (os apps até a 0.47 procuram versões novas só lá; as versões até a 0.47.1 saíram só lá) e atualiza a receita (`Casks/gantry.rb`, gerada por `scripts/cask.sh`, que baixa deste repositório) e os links de download das páginas de lá (`scripts/downloads.py`). Nenhum segredo é necessário. A pipeline não usa artifacts do Actions (a cota de armazenamento da organização trava os builds quando enche): a release nasce como rascunho, cada build envia os seus arquivos direto para ela e o último job a publica. No Mac:

```bash
brew install --cask gonservice/tap/gantry
brew upgrade --cask gantry
```

A receita tira a marca de "baixado da internet" só do Gantry.app, então o macOS não bloqueia a primeira abertura. `brew uninstall --zap --cask gantry` também apaga a configuração (máquinas, clusters, preferências); os containers e VMs continuam nas máquinas.

Para lançar uma versão: mude o `VERSION` do Makefile, faça o commit e rode `git tag -a vX.Y.Z -m "Gantry X.Y.Z" && git push origin main vX.Y.Z`. O rodapé do app avisa quando há uma release mais nova: ele lê as releases deste repositório e as do homebrew-tap (as que começam com `gantry-v`) e fica com a mais nova. Instalado pelo Homebrew, o aviso mostra o `brew upgrade` para copiar.

Cada push na `main` roda os testes e o teste de interface, que abre todas as telas da versão de navegador com o Docker do GitHub e falha com erro de JavaScript (`.github/workflows/ci.yml`). Mudanças em `site/` ou `docs/images/` publicam o site (`.github/workflows/pages.yml`).

## Configuração

Fica em `~/Library/Application Support/gantry/config.json` no macOS e em `~/.config/gantry/config.json` no Linux. É criada quando você adiciona um host, mas também pode ser escrita à mão:

```json
{
  "autodetect": true,
  "hosts": [
    { "name": "homelab", "url": "ssh://homelab" },
    {
      "name": "laboratorio",
      "url": "ssh://ana@192.0.2.10",
      "socket": "/run/user/1000/podman/podman.sock",
      "compose": ["podman", "compose"]
    }
  ]
}
```

- `url`: `ssh://…`, `unix:///caminho/do.sock`, `tcp://host:2375`, `nerdctl://…` ou `k3s://`. Hosts `tcp://` só controlam containers, sem compose nem arquivos.
- `socket`: opcional; sem ele o app procura o socket sozinho.
- `compose`: opcional; o padrão é `podman compose` no Podman e `docker compose` nos demais.
- `autodetect: false`: desliga a detecção dos engines locais.
- `hidden`: hosts detectados que você ocultou.

Os labs que o app já viu ficam em `state.json`, na mesma pasta.

### Log, cópias e diagnóstico

- **Log:** `logs/gantry.log` na pasta da configuração. Quando passa de 5 MB, o arquivo vira `gantry.log.1` (até o `.3`). Fica registrado: abertura, operações (início, fim, duração e erro), falhas de SSH e erros internos. Senhas, tokens e chaves são mascarados.
- **Erros internos:** um erro de programação numa tarefa de fundo (watch do cluster, métricas, operações) fica registrado no log e o app continua aberto. Uma operação que quebra termina com "erro interno do Gantry…" na aba dela.
- **Formato e cópias:** `formato.json` diz em que versão do formato a pasta está. Ao abrir uma pasta de um formato antigo, o Gantry faz uma cópia em `copias/antes-do-formato-N-…` e só então migra. Uma pasta de um Gantry mais novo não é alterada. Além disso, o app guarda uma cópia por dia das configurações (`copias/diaria-AAAAMMDD`, as 7 últimas), com os `.json` e os kubeconfigs, sem o log. Para voltar uma configuração, feche o app e copie os arquivos da cópia para a pasta.
- **Diagnóstico:** Configuração → Geral → **Ver diagnóstico** junta a versão, o sistema, o estado de cada máquina e cluster, os engines deste computador, os erros internos com a pilha e o fim do log, sem endereços, usuários, senhas nem a sua pasta pessoal. **Copiar** ou **Salvar…** para mandar junto com a descrição do problema; **Abrir pasta do log** mostra o arquivo.

## VM de teste

`scripts/test-vm.sh` sobe uma VM Fedora com Podman no QEMU do Mac, para testar o Gantry contra um host remoto de verdade:

```bash
brew install qemu
scripts/test-vm.sh start    # baixa a imagem na primeira vez e sobe a VM
scripts/test-vm.sh ssh      # entra na VM
scripts/test-vm.sh status
scripts/test-vm.sh console  # acompanha o console da VM
scripts/test-vm.sh stop
scripts/test-vm.sh reset    # apaga o disco e começa do zero
```

A VM cria um usuário com o seu nome, aceita as suas chaves SSH e tem a senha `gantry`. Ela já vem com Podman, o `podman.socket` ligado e um compose de exemplo em `~/labs/web`. No fim do `start`, o script mostra o endereço para adicionar no Gantry (`ssh://<você>@127.0.0.1:2222`).

Para testar o botão que ativa o socket do Podman, suba com `GANTRY_VM_SOCKET=off`. As outras opções estão no topo do script.

## Segurança

- Por padrão, o servidor interno só escuta em `127.0.0.1`. Para escutar na rede é preciso `-expor-na-rede`; o recomendado é um túnel SSH. Com `-tls` (certificado gerado pelo Gantry) ou `-cert` e `-key` (o seu), a conexão usa HTTPS; sem eles, todas as páginas avisam que quem estiver na rede pode ler a chave. Os programas do próprio computador (as perguntas de senha do SSH, o `gantry mcp`) falam com o servidor sempre por `127.0.0.1`.
- Toda chamada à API leva um token gerado a cada execução, no cabeçalho `Authorization` (ou na URL, nos fluxos ao vivo). Não há cookie: um cookie de `127.0.0.1` iria também para todos os outros apps em `127.0.0.1`, inclusive as páginas dos containers e as portas encaminhadas.
- No modo navegador, o link do terminal (`?c=`) vale uma vez e por 10 minutos; ele entrega o token à página, que o guarda no armazenamento local daquela origem. Usado o link, o terminal mostra outro, para outro navegador. O token nunca vai na linha de comando de outro programa.
- Requisições de outras origens e de hosts que não sejam localhost são recusadas, o que protege contra CSRF e DNS rebinding.
- **Abrir** um app do cluster cria um túnel local com link de uso único e cookie próprio daquela porta; com o cluster em modo só leitura, só leituras passam pelo túnel.
- O kubeconfig lido de dentro da VM de um projeto (k3s) não pode rodar comandos nem ler arquivos deste computador: plugins de login, arquivos e proxy são descartados.
- A pasta dos túneis SSH em `/tmp` precisa ser do usuário e fechada aos outros; se não for, o app usa uma nova.
- O log mascara senhas, tokens, chaves e senhas em URLs antes de gravar.
- Pacotes e backups restaurados não escrevem fora da pasta do projeto, nem por links simbólicos que já estejam lá.
- Labs importados (`.unl`) e pacotes não conseguem injetar chaves no compose nem comandos no shell: nomes, caminhos e referências de imagem são conferidos antes de qualquer arquivo ser gravado.
- Senhas digitadas ficam só na memória, e só se você marcar "lembrar", até fechar o app.
- Senhas e tokens dos projetos podem ficar no chaveiro do sistema em vez do `.env` (veja Segredos).
- A conexão direta com os agentes usa TLS 1.3 com certificados fixados dos dois lados, pareados pelo SSH. Um app não pareado não conecta, e o app não aceita um agente diferente do que pareou.

## Estrutura

```
main.go, desktop.go      flags, servidor interno e janela (Wails, tag desktop)
cmd/gantry-agent         agente opcional que roda nos servidores
askpass.go               ajudante SSH_ASKPASS que repassa as perguntas do ssh à interface
envpath.go               PATH completo para apps abertos pelo Finder
importcmd.go, mcpcmd.go  gantry importar-unl e gantry mcp
internal/agent           servidor e cliente do agente (socket Unix, métricas, inotify)
internal/agentbin        binários do agente embutidos no app (gerados por make agents)
internal/composex        leitura do compose como escrito e das extensões x-gantry e x-vms
internal/config          configuração e detecção de engines locais
internal/engine          cliente da API do Docker/Podman (HTTP sobre o socket)
internal/hosts           conexões local, tcp e ssh; comandos e arquivos remotos
internal/labs            agrupamento por projeto do compose e projetos lembrados
internal/sysinfo         hardware e uso da máquina (script sh para Linux e macOS)
internal/templates       modelos de projeto: os embutidos (builtin/*.yaml) e os do usuário
internal/local           estado e instalação dos engines desta máquina
internal/metrics         coleta e histórico de CPU e memória por container
internal/vms             VMs QEMU e equipamentos de rede dos labs (script vmctl embutido, roda na máquina do host)
internal/unl             importação do PNETLab/EVE-NG
internal/mcp             servidor MCP para assistentes de IA
internal/obs             clientes do Prometheus, do Loki e do Grafana, e a detecção deles nos containers
internal/shim            API do Docker para runtimes de linha de comando (nerdctl, k3s)
internal/kube            cliente da API do Kubernetes (kubeconfig, listas, apply, logs, exec por WebSocket, Helm)
internal/server          API REST e streams SSE (eventos, logs, shell, senhas)
frontend/                interface em React + TypeScript (Vite); make ui compila para web/ui/
                         (src/shell: barra lateral, rotas e painel de baixo; src/views: as telas)
web/                     o que vai embutido no binário: index.html, CSS, xterm.js e web/ui/
site/                    o site (GitHub Pages)
scripts/test-vm.sh       VM Fedora de teste no QEMU
examples/                labs de exemplo (examples/README.md)
e2e/, tests/             testes de ponta a ponta e da interface
```

## Licença

Copyright (C) 2026 GonService.

O Gantry é software livre sob a [GNU Affero General Public License v3.0](../LICENSE) (AGPL-3.0): você pode usar, estudar, modificar e distribuir, desde que versões modificadas, inclusive as oferecidas a outras pessoas pela rede (o modo navegador do Gantry), também sejam distribuídas com o código-fonte, sob a mesma licença.

Componentes de terceiros embutidos mantêm as licenças deles: o VPCS (BSD, `internal/vpcsbin/bin/COPYING`) e o xterm.js (MIT, `web/vendor/xterm-LICENSE`). As imagens dos equipamentos Cisco (IOL, IOS do Dynamips) não fazem parte do projeto e não são distribuídas aqui.

## Segurança

Achou uma falha de segurança? Não abra uma issue pública: veja [SECURITY.md](../SECURITY.md).
