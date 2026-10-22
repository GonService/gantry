# Lab MPLS completo, com Prometheus e Grafana

Um provedor com núcleo MPLS que atende dois clientes por L3VPN, com a
gerência dentro da própria rede e tudo monitorado. Sobe do zero com um
compose só: 14 equipamentos Cisco IOL já configurados, os computadores dos
clientes, Prometheus, snmp_exporter, sondas de ping, Grafana e um terminal
pelo navegador.

```
  cli-a1/a1b─SW-A1─CE-A1─┐            ┌─PE2─CE-A2─SW-A2─cli-a2/a2b
                        PE1══P1══P2══┤
         cli-b1──CE-B1─┘   (MPLS)     └─PE3─CE-A3─SW-A3─cli-a3/a3b
                                       ├──CE-B2──cli-b2
                                       └══NOC──LAN 10.250.0.0/24
                                                ├─ Prometheus (.10) + snmp_exporter + ferramentas
                                                └─ Grafana (:3000) + terminal web
```

## Os arquivos

Tudo é arquivo comum, cada um no formato da sua ferramenta. Não há nada
gerado: para mudar algo, edite o arquivo.

```
lab-mpls/
├── compose.yaml              os equipamentos (x-vms), os cabos e os containers
├── equipamentos/
│   ├── P1.cfg … SW-A3.cfg    configuração IOS de cada equipamento
├── prometheus/
│   ├── prometheus.yml        o que coletar (alvos, sondas de ping)
│   └── snmp.yml              as OIDs do módulo "lab" do snmp_exporter
├── grafana/
│   ├── datasources.yaml      a fonte de dados (o Prometheus)
│   ├── dashboards.yaml       de onde vêm os painéis
│   └── paineis/              topologia.json, visao-geral.json, equipamento.json
├── web/
│   └── nginx.conf            Grafana e o terminal web no mesmo endereço
└── README.md
```

## Sumário

- [Os arquivos](#os-arquivos)
- [O que tem no lab](#o-que-tem-no-lab)
- [Endereços](#endereços)
- [Cabos e portas](#cabos-e-portas)
- [Roteamento](#roteamento)
- [Por que existe o NOC](#por-que-existe-o-noc)
- [Monitoramento](#monitoramento)
- [Grafana](#grafana)
- [Subir o lab](#subir-o-lab)
- [Acessos](#acessos)
- [Mudar o lab](#mudar-o-lab)
- [Validar](#validar)
- [Problemas conhecidos e dicas](#problemas-conhecidos-e-dicas)

## O que tem no lab

| Parte | Equipamentos | O que roda |
|---|---|---|
| Núcleo (AS 65000) | P1, P2 | OSPF área 0, LDP e refletores de rota VPNv4 para os PEs |
| Borda (AS 65000) | PE1, PE2, PE3 | VRFs dos clientes, MP-BGP (VPNv4) com os P, eBGP com os CEs |
| CLIENTE-A | CE-A1, CE-A2, CE-A3 (AS 65101–65103) | eBGP com o PE; LAN 172.16.N.0/24 atrás de um switch L2 |
| Switches do CLIENTE-A | SW-A1, SW-A2, SW-A3 (IOL L2) | VLAN 10 (CLIENTES) nas portas; IP só para gerência (SVI) |
| CLIENTE-B | CE-B1, CE-B2 (AS 65201, 65202) | eBGP com o PE; LAN 172.17.N.0/24 |
| Gerência | NOC (AS 65999) | Site do provedor atrás do PE3, com a LAN do monitoramento |
| Computadores | cli-a1, cli-a1b, cli-a2, cli-a2b, cli-a3, cli-a3b, cli-b1, cli-b2 | Containers busybox: ping, telnet, traceroute, wget |
| Monitoramento | prometheus, snmp-exporter, rotas, ferramentas, sonda-a, sonda-b | Coleta SNMP de tudo e ping entre os sites |
| Visualização | grafana, web | Painéis, a topologia e o terminal pelo navegador |

São 14 nós IOL (uns 8 GB de memória no host) e 25 containers.

## Endereços

| O quê | Endereço |
|---|---|
| Loopbacks do provedor | P1 10.255.0.1, P2 10.255.0.2, PE1 10.255.0.11, PE2 10.255.0.12, PE3 10.255.0.13, NOC 10.255.0.100 |
| Loopbacks dos CEs | CE-A1..3 10.255.1.1..3, CE-B1..2 10.255.2.1..2 |
| Núcleo (/30) | P1–P2 10.0.100.0, PEn–P1 10.0.n1.0, PEn–P2 10.0.n2.0 (o PE é o .1) |
| PE–CE (/30) | CLIENTE-A 10.1.n.0, CLIENTE-B 10.2.n.0 (o PE é o .1, o CE o .2) |
| NOC–PE3 | tabela global 10.0.90.0/30, VRF GERENCIA 10.9.0.0/30 (PE3 .1, NOC .2) |
| LANs do CLIENTE-A | 172.16.N.0/24: CE .1, switch .2 (gerência), computadores .10 e .11 |
| LANs do CLIENTE-B | 172.17.N.0/24: CE .1, computador .10 |
| LAN do NOC | 10.250.0.0/24: NOC .1, Prometheus .10 |

## Cabos e portas

No IOL as portas são numeradas na ordem de `interfaces:` do compose, quatro
por slot (Et0/0..Et0/3, depois Et1/0..).

| Nó | Portas |
|---|---|
| P1 | Et0/0 P2 · Et0/1 PE1 · Et0/2 PE2 · Et0/3 PE3 |
| P2 | Et0/0 P1 · Et0/1 PE1 · Et0/2 PE2 · Et0/3 PE3 |
| PE1 | Et0/0 P1 · Et0/1 P2 · Et0/2 CE-A1 · Et0/3 CE-B1 |
| PE2 | Et0/0 P1 · Et0/1 P2 · Et0/2 CE-A2 |
| PE3 | Et0/0 P1 · Et0/1 P2 · Et0/2 CE-A3 · Et0/3 CE-B2 · Et1/0 NOC (global) · Et1/1 NOC (VRF GERENCIA) |
| NOC | Et0/0 PE3 (global) · Et0/1 PE3 (VRF GERENCIA) · Et0/2 LAN do NOC |
| CE-A1..3 | Et0/0 PE · Et0/1 switch do site |
| CE-B1..2 | Et0/0 PE · Et0/1 LAN do site (computador) |
| SW-A1..3 | Et0/0 CE · Et0/1 e Et0/2 computadores (VLAN 10) |

## Roteamento

- **Núcleo:** OSPF área 0 entre P e PE (e o NOC pelo enlace global), LDP em
  todos os enlaces do núcleo. Os loopbacks viram os rótulos MPLS.
- **VPN:** os PEs fecham MP-BGP VPNv4 com P1 e P2, que são refletores de rota
  (cada vizinho ativado um a um no address-family vpnv4).
- **VRFs nos PEs:**

  | VRF | RD | Exporta | Importa |
  |---|---|---|---|
  | CLIENTE-A | 65000:100 | 65000:100 | 65000:100, 65000:999 |
  | CLIENTE-B | 65000:200 | 65000:200 | 65000:200, 65000:999 |
  | GERENCIA (só no PE3) | 65000:999 | 65000:999 | 65000:999, 65000:100, 65000:200 |

  Cada cliente importa a si mesmo e a rede da gerência. A GERENCIA importa os
  dois clientes. Resultado: o NOC alcança todo mundo, e o CLIENTE-A continua
  sem ver o CLIENTE-B (extranet de gerência).
- **CEs:** eBGP com o PE, anunciando a LAN e o loopback.
- **Switches:** só L2. O IP de gerência fica na VLAN 10, com gateway no CE.

## Por que existe o NOC

O Prometheus precisa alcançar dois mundos separados:

1. os P e PE, que estão na tabela global do provedor;
2. os CEs e switches, que estão dentro das VRFs dos clientes.

Um container não fala OSPF nem BGP. O NOC é o roteador da sala de
monitoramento que junta os dois: um cabo na tabela global (OSPF) e outro na
VRF GERENCIA (eBGP), ambos com o PE3. Para o Prometheus, tudo é "mande para
10.250.0.1". É o desenho que os provedores usam: a gerência é mais um site da
rede, e não uma rede à parte ligada em todos os equipamentos.

O container `rotas` (na rede do Prometheus) mantém as rotas 10.0.0.0/8 e
172.16.0.0/15 via 10.250.0.1.

## Monitoramento

| Serviço | Papel |
|---|---|
| prometheus | Na LAN do NOC (10.250.0.10). Retenção de 15 dias no volume `prometheus` |
| snmp-exporter | Na rede do Prometheus. Módulo `lab` (SNMP v2c, comunidade `public`) |
| sonda-a, sonda-b | blackbox_exporter dentro dos sites cli-a1 e cli-b1, para o ping entre sites |
| ferramentas | Alpine com snmpwalk, curl, telnet e o terminal web (ttyd) |

Jobs do Prometheus:

- **rede** (a cada 30 s): os 14 equipamentos pelo loopback (os switches pelo
  IP de gerência), com os rótulos `device` e `papel` (P, PE, CE, L2, NOC).
- **ping**: as sondas pingam os outros sites. Rótulos `origem`, `destino`,
  `vrf` e `esperado` (`ok` ou `bloqueado`). Os pings entre clientes diferentes
  devem falhar.
- **snmp-exporter**: a saúde do próprio exporter.

O que o módulo `lab` coleta:

| Assunto | MIB |
|---|---|
| Interfaces, tráfego, erros e descartes | IF-MIB (ifXTable, contadores de 64 bits) |
| CPU | CISCO-PROCESS-MIB; nos switches IOL L2, OLD-CISCO-CPU-MIB (busyPer, avgBusy1, avgBusy5) |
| Memória | CISCO-MEMORY-POOL-MIB; nos switches L2, freeMem |
| BGP | BGP4-MIB (as sessões em VRF dos PEs não aparecem nela) |
| OSPF | OSPF-MIB (vizinhos) |
| Tabela de rotas | IP-FORWARD-MIB (ipCidrRouteTable) |
| LDP | MPLS-LDP-STD-MIB |
| Rotas por VRF | MPLS-VPN-MIB (1.3.6.1.3.118; o IOS não tem a MPLS-L3VPN-STD) |

## Grafana

Endereço `http://<host>:3000` (admin/admin; anônimo vê sem login). Os
painéis ficam na pasta **Lab MPLS** e vêm de `grafana/paineis/`.

- **Topologia** (a tela inicial): o mesmo desenho da aba Topologia do Gantry.
  - Caixa verde: o equipamento responde ao SNMP. Vermelha: não responde.
  - Borda: azul escuro P, roxo PE, laranja CE, azul L2, verde NOC.
  - Número embaixo: CPU dos últimos 5 minutos.
  - Cabo verde: interface up. Vermelho: down. Mais grosso: mais tráfego.
  - Passe o mouse (ou clique) num equipamento: **Detalhes** abre o painel
    Equipamento já nele; **Terminal** abre um telnet para ele no navegador.
  - A roda do mouse aproxima, arrastar move o desenho.
- **Rede MPLS — visão geral:** equipamentos respondendo, sessões BGP
  (estabelecidas e com problema), vizinhos OSPF, sessões LDP, CPU, memória,
  rotas, tráfego por equipamento e do núcleo, rotas por VRF e a tabela de
  alcance entre os sites, com a latência.
- **Equipamento** (escolha no topo): CPU (5 s, 1 min, 5 min), memória,
  tráfego, pacotes, erros e descartes por interface, a tabela de interfaces,
  a tabela de rotas, vizinhos BGP e OSPF, sessões LDP e rotas por VRF.

O terminal é o ttyd do container `ferramentas`, em
`http://<host>:3000/terminal/?arg=<ip>` (abre `telnet <ip>`). O nginx do
serviço `web` roda na rede do Grafana e entrega os dois no mesmo endereço,
por isso os links funcionam de qualquer máquina que abra o painel.

## Subir o lab

1. Um host Linux x86_64 com Podman ou Docker e o agente do Gantry, com as
   imagens do IOL numa pasta de imagens da máquina (página da máquina ›
   Configurações › Imagens). Quais arquivos o lab usa está em `x-imagens` no
   `compose.yaml`:

   ```yaml
   x-imagens:
     iol-l3: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin
     iol-l2: i86bi_Linux-L2-Adventerprisek9-ms.SSA.high_iron_20190423.bin
   ```

   O valor pode ser só o nome do arquivo ou o caminho completo.
2. Leve a pasta inteira para o host (o lab é a pasta, não só o compose):
   - pelo Gantry: abra a pasta como projeto em **This Mac** (Projetos ›
     Outras formas › abrir uma pasta com compose) e use **Enviar para…** o
     host, marcando para subir; ou
   - à mão: `git clone` do repositório (ou `scp -r lab-mpls host:`) e, no
     Gantry, abra a pasta como projeto naquele host.
3. **Bring up**. No primeiro boot cada equipamento recebe o seu
   `equipamentos/<nome>.cfg` (digitado no console e salvo com
   `write memory`). Em um ou dois minutos o OSPF, o LDP e o BGP fecham e o
   Grafana fica todo verde.

Para voltar um equipamento ao arquivo, remova o nó e suba de novo: a
configuração só entra quando ele não tem NVRAM.

## Acessos

| O quê | Como |
|---|---|
| Equipamentos | Console no Gantry, terminal da Topologia no Grafana, ou `telnet <loopback>` a partir do `ferramentas` ou dos computadores. Usuário `admin`, senha `admin` (já entra em privilégio 15) |
| SNMP | v2c, comunidade `public`, só leitura |
| Grafana | `http://<host>:3000`, admin/admin |
| Prometheus | `http://<host>:9090` |
| Ferramentas no NOC | Shell do container `ferramentas` no Gantry. Exemplo: `snmpwalk -v2c -c public 10.255.0.11 1.3.6.1.2.1.15.3.1.2` |

## Mudar o lab

Edite os arquivos (no seu editor ou na aba **File** do projeto no Gantry, que
lista todos eles) e aplique:

| Para mudar | Arquivo | Como aplica |
|---|---|---|
| Configuração de um equipamento | `equipamentos/<nome>.cfg` | Vale no próximo primeiro boot: remova o nó e suba de novo. Num equipamento rodando, use o console ou o telnet e `write memory` |
| Equipamentos e cabos | `compose.yaml` (`x-vms`: `interfaces:` são os cabos, na ordem das portas) | **Save and apply** |
| Versão do IOS | `compose.yaml` (`x-imagens`) | Remova os nós e suba de novo |
| Computadores e containers | `compose.yaml` (`services`) | **Save and apply** (recria só o que mudou) |
| O que o Prometheus coleta | `prometheus/prometheus.yml` | Reinicie o `prometheus` (o Gantry oferece ao salvar) |
| As OIDs do SNMP | `prometheus/snmp.yml` | Reinicie o `snmp-exporter` |
| Painéis | Na tela do Grafana; para guardar, Share › Export › Save to file por cima de `grafana/paineis/<painel>.json` | O Grafana relê a pasta a cada 30 s |
| Grafana e terminal no mesmo endereço | `web/nginx.conf` | Reinicie o `web` |

Exemplo, um CE-A4 no PE2:

1. `equipamentos/CE-A4.cfg`: copie o `CE-A3.cfg` e troque hostname,
   loopback, AS, IPs e a LAN.
2. `equipamentos/PE2.cfg`: uma interface nova na VRF CLIENTE-A e o vizinho
   no `address-family ipv4 vrf CLIENTE-A` do BGP. No PE2 rodando, os mesmos
   comandos pelo console.
3. `compose.yaml`: o nó `CE-A4` em `x-vms` com `config: equipamentos/CE-A4.cfg`,
   e o cabo novo (`pe2-cea4`) nas `interfaces:` do PE2 e do CE-A4.
4. `prometheus/prometheus.yml`: o loopback do CE-A4 no job `rede`, com
   `device: CE-A4` e `papel: CE`.
5. Aplique. Para ele aparecer no desenho do Grafana, edite o painel
   Topologia (Edit) e adicione o nó e o cabo.

## Validar

Validação feita em 21/10/2026 num servidor Fedora 43 com Podman 5.8:

| Verificação | Esperado | Resultado |
|---|---|---|
| Equipamentos respondendo (`up{job="rede"}`) | 14 | 14 |
| Sessões BGP estabelecidas (`count(bgpPeerState == 6)`) | 24, nenhuma com problema | 24 / 0 |
| Vizinhos OSPF full (`count(ospfNbrState == 8)`) | 16 | 16 |
| Sessões LDP operacionais (`count(mplsLdpSessionState == 5)`) | 14 | 14 |
| Rotas por VRF | PE1: CLIENTE-A 9, CLIENTE-B 7; PE3: GERENCIA 13 | igual |
| Ping entre sites (`probe_success{job="ping"}`) | `ok` = 1 e `bloqueado` = 0 | todos certos |
| Telnet a partir do NOC | PE1, CE-A3 e SW-A2 aceitam admin/admin | ok |
| Traceroute CE-A3 → cli-a1 | passa pelo núcleo com rótulo MPLS | 5 saltos, com rótulo |
| VLAN no switch | `show vlan brief` no SW-A2: VLAN 10 nas três portas | ok |
| Terminal pela Topologia | abre telnet no PE3 | ok |

Comandos úteis nos equipamentos:

```
show ip bgp vpnv4 all summary
show ip ospf neighbor
show mpls ldp neighbor
show ip route vrf CLIENTE-A
traceroute vrf CLIENTE-A 172.16.3.10
```

## Problemas conhecidos e dicas

- **VPNv4 com peer-group:** no IOS 15.7, `neighbor <peer-group> activate` em
  `address-family vpnv4` não fica salvo. Os P ativam cada PE um a um.
- **Telnet no IOS 15:** a vty precisa de `transport input telnet` além do
  `login local`.
- **Switches IOL L2:** não têm a CISCO-PROCESS-MIB nem a MEMORY-POOL; CPU e
  memória vêm da OLD-CISCO-CPU-MIB.
- **Séries antigas no Prometheus:** o volume guarda 15 dias. Se a rede mudar
  de endereços, as séries antigas aparecem como DOWN até expirarem. Os
  indicadores usam o valor de agora; para zerar, remova o volume `prometheus`.
- **Containers na rede de outro** (`network_mode: service:x`, como `web`,
  `snmp-exporter`, `rotas` e `ferramentas`): o Podman não recria um serviço
  enquanto outros usam a rede dele. O Aplicar do Gantry remove antes os que
  dependem e o compose os cria de novo.
- **Configuração só no primeiro boot:** o `equipamentos/<nome>.cfg` entra
  quando o equipamento não tem NVRAM. Mudou o arquivo? Remova o nó e suba de
  novo, ou aplique os comandos no equipamento rodando.
- **SELinux (Fedora, RHEL):** os arquivos montados nos containers usam `:z`
  para o Podman ajustar o rótulo; sem isso o container não consegue ler.
- **Permissão dos arquivos montados:** o Prometheus, o snmp_exporter e o
  Grafana rodam como root no container (`user: "0:0"`) para ler os arquivos
  da pasta mesmo que eles cheguem ao host só com permissão para o dono
  (comum ao copiar de outro sistema). No Podman sem root, esse root é o seu
  usuário.
