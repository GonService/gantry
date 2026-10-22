# Lab de backbone: MPLS, BGP e switches L3

Núcleo MPLS (OSPF + LDP), L3VPN com BGP (VPNv4 entre os PEs, eBGP com os
clientes) e um site com dois switches L3 (IOL-L2) ligados por trunk. Serve
para as aulas e para validar o Gantry com IOS de verdade: portas, cabos,
redes do lab com DHCP, captura e cabos trocados com tudo ligado.

```
               AS 65000 (provedor)
   CE1 ── PE1 ── P1 ── PE2 ── CE2 ── [lan-b] ── pc-b1
    │       └─── P2 ───┘
   SW1 ══trunk══ SW2
    │             │
  [lan-a]      [lan-a20]
  pc-a1 (VM, DHCP)  pc-a2
```

## Antes de subir

1. **Host Linux x86_64** (um servidor Linux, por exemplo): o IOL é um programa Linux x86 e não roda no
   Mac. As VMs (pc-a1) usam o KVM do host.
2. **Imagens**: copie os `.bin` do IOL (roteador e switch) e o `iourc`, se a
   imagem pedir licença, para `~/gantry/imagens` do host (ou outra pasta de
   imagens dele, na página da máquina › Configurações › Imagens). O Gantry
   reconhece cada arquivo pelo nome (`i86bi_…L3…bin` é o `cisco-iol`,
   `i86bi_…L2…bin` o `cisco-iol-l2`); no compose dá para usar o nome do
   catálogo, como aqui, ou o nome do arquivo em `image:`.
3. **Memória**: 8 nós IOL (~512–768 MB cada) e uma VM de 512 MB; uns 6 GB.
4. Crie o projeto a partir desta pasta com **Máquina de destino = o servidor Linux** (ou
   use Enviar para… o servidor) e suba.

## Endereços

| Ligação | Lado A | Lado B |
|---|---|---|
| CE1 e0/0 ↔ PE1 e0/0 (VRF CLIENTE) | CE1 172.16.1.2/30 | PE1 172.16.1.1/30 |
| PE1 e0/1 ↔ P1 e0/0 | PE1 10.0.12.1/30 | P1 10.0.12.2/30 |
| PE1 e0/2 ↔ P2 e0/2 | PE1 10.0.13.1/30 | P2 10.0.13.2/30 |
| P1 e0/1 ↔ P2 e0/0 | P1 10.0.23.1/30 | P2 10.0.23.2/30 |
| P1 e0/2 ↔ PE2 e0/0 | P1 10.0.24.1/30 | PE2 10.0.24.2/30 |
| P2 e0/1 ↔ PE2 e0/1 | P2 10.0.34.1/30 | PE2 10.0.34.2/30 |
| PE2 e0/2 ↔ CE2 e0/0 (VRF CLIENTE) | PE2 172.16.2.1/30 | CE2 172.16.2.2/30 |
| CE1 e0/1 ↔ SW1 e0/0 (porta roteada) | CE1 10.1.0.1/30 | SW1 10.1.0.2/30 |
| SW1 e0/1 ↔ SW2 e0/0 | trunk 802.1Q (VLAN 10 e 20) | |

Loopbacks: PE1 1.1.1.1, P1 2.2.2.2, P2 3.3.3.3, PE2 4.4.4.4, CE1 10.255.0.1,
CE2 10.255.0.2. Redes dos clientes: lan-a 10.1.10.0/24 (VLAN 10, DHCP do
Gantry, gateway SW1), lan-a20 10.1.20.0/24 (VLAN 20, gateway SW1), lan-b
10.2.10.0/24 (gateway CE2).

## Configurações

Cole no Console de cada nó (`enable`, `configure terminal`) e termine com
`end` e `write memory`; depois, **Salvar estado do lab** guarda tudo.

### P1

```
hostname P1
mpls label protocol ldp
interface Loopback0
 ip address 2.2.2.2 255.255.255.255
interface Ethernet0/0
 description PE1
 ip address 10.0.12.2 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/1
 description P2
 ip address 10.0.23.1 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/2
 description PE2
 ip address 10.0.24.1 255.255.255.252
 mpls ip
 no shutdown
router ospf 1
 router-id 2.2.2.2
 network 2.2.2.2 0.0.0.0 area 0
 network 10.0.0.0 0.0.255.255 area 0
mpls ldp router-id Loopback0 force
```

### P2

```
hostname P2
mpls label protocol ldp
interface Loopback0
 ip address 3.3.3.3 255.255.255.255
interface Ethernet0/0
 description P1
 ip address 10.0.23.2 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/1
 description PE2
 ip address 10.0.34.1 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/2
 description PE1
 ip address 10.0.13.2 255.255.255.252
 mpls ip
 no shutdown
router ospf 1
 router-id 3.3.3.3
 network 3.3.3.3 0.0.0.0 area 0
 network 10.0.0.0 0.0.255.255 area 0
mpls ldp router-id Loopback0 force
```

### PE1

```
hostname PE1
vrf definition CLIENTE
 rd 65000:1
 address-family ipv4
  route-target export 65000:1
  route-target import 65000:1
 exit-address-family
mpls label protocol ldp
interface Loopback0
 ip address 1.1.1.1 255.255.255.255
interface Ethernet0/0
 description CE1
 vrf forwarding CLIENTE
 ip address 172.16.1.1 255.255.255.252
 no shutdown
interface Ethernet0/1
 description P1
 ip address 10.0.12.1 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/2
 description P2
 ip address 10.0.13.1 255.255.255.252
 mpls ip
 no shutdown
router ospf 1
 router-id 1.1.1.1
 network 1.1.1.1 0.0.0.0 area 0
 network 10.0.0.0 0.0.255.255 area 0
mpls ldp router-id Loopback0 force
router bgp 65000
 bgp router-id 1.1.1.1
 no bgp default ipv4-unicast
 neighbor 4.4.4.4 remote-as 65000
 neighbor 4.4.4.4 update-source Loopback0
 address-family vpnv4
  neighbor 4.4.4.4 activate
  neighbor 4.4.4.4 send-community extended
 exit-address-family
 address-family ipv4 vrf CLIENTE
  neighbor 172.16.1.2 remote-as 65001
  neighbor 172.16.1.2 activate
 exit-address-family
```

### PE2

```
hostname PE2
vrf definition CLIENTE
 rd 65000:1
 address-family ipv4
  route-target export 65000:1
  route-target import 65000:1
 exit-address-family
mpls label protocol ldp
interface Loopback0
 ip address 4.4.4.4 255.255.255.255
interface Ethernet0/0
 description P1
 ip address 10.0.24.2 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/1
 description P2
 ip address 10.0.34.2 255.255.255.252
 mpls ip
 no shutdown
interface Ethernet0/2
 description CE2
 vrf forwarding CLIENTE
 ip address 172.16.2.1 255.255.255.252
 no shutdown
router ospf 1
 router-id 4.4.4.4
 network 4.4.4.4 0.0.0.0 area 0
 network 10.0.0.0 0.0.255.255 area 0
mpls ldp router-id Loopback0 force
router bgp 65000
 bgp router-id 4.4.4.4
 no bgp default ipv4-unicast
 neighbor 1.1.1.1 remote-as 65000
 neighbor 1.1.1.1 update-source Loopback0
 address-family vpnv4
  neighbor 1.1.1.1 activate
  neighbor 1.1.1.1 send-community extended
 exit-address-family
 address-family ipv4 vrf CLIENTE
  neighbor 172.16.2.2 remote-as 65002
  neighbor 172.16.2.2 activate
 exit-address-family
```

### CE1

```
hostname CE1
interface Loopback0
 ip address 10.255.0.1 255.255.255.255
interface Ethernet0/0
 description PE1
 ip address 172.16.1.2 255.255.255.252
 no shutdown
interface Ethernet0/1
 description SW1
 ip address 10.1.0.1 255.255.255.252
 no shutdown
ip route 10.1.10.0 255.255.255.0 10.1.0.2
ip route 10.1.20.0 255.255.255.0 10.1.0.2
router bgp 65001
 bgp router-id 10.255.0.1
 neighbor 172.16.1.1 remote-as 65000
 network 10.255.0.1 mask 255.255.255.255
 network 10.1.0.0 mask 255.255.255.252
 network 10.1.10.0 mask 255.255.255.0
 network 10.1.20.0 mask 255.255.255.0
```

### CE2

```
hostname CE2
interface Loopback0
 ip address 10.255.0.2 255.255.255.255
interface Ethernet0/0
 description PE2
 ip address 172.16.2.2 255.255.255.252
 no shutdown
interface Ethernet0/1
 description lan-b
 ip address 10.2.10.1 255.255.255.0
 no shutdown
router bgp 65002
 bgp router-id 10.255.0.2
 neighbor 172.16.2.1 remote-as 65000
 network 10.255.0.2 mask 255.255.255.255
 network 10.2.10.0 mask 255.255.255.0
```

### SW1 (L3)

```
hostname SW1
ip routing
vlan 10
 name lan-a
vlan 20
 name lan-a20
interface Ethernet0/0
 description CE1
 no switchport
 ip address 10.1.0.2 255.255.255.252
 no shutdown
interface Ethernet0/1
 description SW2
 switchport trunk encapsulation dot1q
 switchport mode trunk
 switchport trunk allowed vlan 10,20
 no shutdown
interface Ethernet0/2
 description lan-a
 switchport mode access
 switchport access vlan 10
 no shutdown
interface Vlan10
 ip address 10.1.10.1 255.255.255.0
 no shutdown
interface Vlan20
 ip address 10.1.20.1 255.255.255.0
 no shutdown
ip route 0.0.0.0 0.0.0.0 10.1.0.1
spanning-tree vlan 10,20 priority 4096
```

### SW2

```
hostname SW2
vlan 10
 name lan-a
vlan 20
 name lan-a20
interface Ethernet0/0
 description SW1
 switchport trunk encapsulation dot1q
 switchport mode trunk
 switchport trunk allowed vlan 10,20
 no shutdown
interface Ethernet0/1
 description lan-a20
 switchport mode access
 switchport access vlan 20
 no shutdown
interface Vlan20
 ip address 10.1.20.2 255.255.255.0
 no shutdown
ip default-gateway 10.1.20.1
```

## Roteiro de validação

Marque cada item; se algum falhar, anote o comando e a saída.

**Gantry**

- [ ] Os 8 nós IOL sobem; o Console de cada um abre e aceita `enable`
- [ ] `show ip interface brief` mostra Ethernet0/0, 0/1… na ordem do desenho
      (a porta e0/x do desenho é a Ethernet0/x do roteador)
- [ ] Rede do lab: as três nuvens aparecem, com as portas ligadas nelas

**Núcleo**

- [ ] P1: `show ip ospf neighbor` com PE1, P2 e PE2 em FULL
- [ ] P1: `show mpls ldp neighbor` com os três; `show mpls forwarding-table`
      com rótulos para 1.1.1.1, 3.3.3.3 e 4.4.4.4
- [ ] PE1: `traceroute 4.4.4.4 source Loopback0` mostra os rótulos MPLS

**BGP e L3VPN**

- [ ] PE1: `show bgp vpnv4 unicast all summary` com 4.4.4.4 recebendo prefixos
- [ ] PE1: `show bgp vpnv4 unicast vrf CLIENTE` com as redes dos dois sites
- [ ] CE1: `show ip bgp` com 10.2.10.0/24 e 10.255.0.2/32
- [ ] CE1: `ping 10.255.0.2 source Loopback0` responde

**Switches L3**

- [ ] SW1: `show interfaces trunk` com VLAN 10 e 20 ativas em Et0/1
- [ ] SW1: `show spanning-tree vlan 10` com SW1 como root
- [ ] SW2: `ping 10.1.20.1` responde (pelo trunk)

**Computadores**

- [ ] pc-a1 pegou 10.1.10.100 pelo DHCP (painel da rede lan-a, coluna Endereços)
- [ ] pc-a1 (Shell): `ping 10.2.10.10` responde (atravessa SW1, CE1, MPLS, CE2)
- [ ] pc-a2 (Terminal): `ping 10.1.10.100` responde (VLAN 20 → SVI → VLAN 10)

**Ferramentas do Gantry**

- [ ] Captura no cabo p1-p2 (Ver ao vivo; Salvar .pcap… abre no Wireshark): pacotes MPLS (0x8847) com os
      rótulos e LDP/OSPF
- [ ] Captura da rede lan-a: o DHCP do pc-a1 e o ARP
- [ ] Desligar o cabo p1-p2 com tudo rodando: o OSPF converge pelo outro
      caminho e o ping de pc-a1 para pc-b1 volta
- [ ] Ligar um segundo cabo entre SW1 e SW2 (e0/3 de cada um, arrastando de
      uma porta à outra): `show spanning-tree` bloqueia uma das duas
- [ ] Salvar estado do lab, parar, subir de novo: as configurações voltam
