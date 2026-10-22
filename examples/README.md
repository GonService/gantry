# Examples / Exemplos

Each folder is a project: open it in Gantry (Projects › Other ways › Open folder with compose) or run it with `docker compose up -d`. The comments inside the files are in Portuguese.

Cada pasta é um projeto: abra no Gantry (Projetos › Outras formas › Abrir pasta com compose) ou rode com `docker compose up -d`.

| Folder / Pasta | What it shows | O que mostra |
|---|---|---|
| [vm-nginx](vm-nginx) | A Debian VM with Nginx on `http://localhost:8081`, no containers (`x-vms`) | Uma VM Debian com Nginx em `http://localhost:8081`, sem containers (`x-vms`) |
| [observabilidade](observabilidade) | A mixed lab: Prometheus and Loki on Podman, Grafana on Docker (`x-gantry`) | Um lab misto: Prometheus e Loki no Podman, Grafana no Docker (`x-gantry`) |
| [monitoramento](monitoramento) | Prometheus, cAdvisor, Loki, Alloy and Grafana for the containers of a machine; Gantry finds them and uses them for metrics and logs | Prometheus, cAdvisor, Loki, Alloy e Grafana para os containers de uma máquina; o Gantry os encontra e usa nas métricas e nos logs |
| [lab-backbone](lab-backbone) | A Cisco IOL backbone: MPLS core (OSPF + LDP), L3VPN with BGP and a site with switches on a trunk (Linux x86_64) | Um backbone Cisco IOL: núcleo MPLS (OSPF + LDP), L3VPN com BGP e um site com switches num trunk (Linux x86_64) |
| [lab-mpls](lab-mpls) | An MPLS provider with two customers, switches and PCs, monitored through the network itself by Prometheus (SNMP) and Grafana | Um provedor MPLS com dois clientes, switches e computadores, monitorado pela própria rede com Prometheus (SNMP) e Grafana |

The Cisco labs need IOL images, which are not distributed with Gantry: see [network-labs.md](../docs/network-labs.md) / [equipamentos-de-rede.md](../docs/equipamentos-de-rede.md).
