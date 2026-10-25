# Equipamentos de rede nos labs

[English](network-labs.md) · **Português** · [Referência](referencia.md)

Roteadores, switches e computadores de lab no Gantry: IOL, Dynamips e VPCS.
Eles ficam em `x-vms`, como as VMs, mas não são VMs: cada um é um programa
na máquina do host, ligado aos outros por cabos.

| Tipo | O que é | Imagem | Roda em |
|---|---|---|---|
| IOL | IOS on Linux: roteador (L3) ou switch (L2) Cisco | `.bin` (`i86bi_linux…`, `x86_64_crb_linux…`) | Linux x86_64 |
| Dynamips | Emulador dos roteadores Cisco clássicos: 7200, 3725, 2691 | `.image` ou `.bin` do IOS (`c7200-…`, `c3725-…`) | Linux, Mac |
| VPCS | Computador simples de lab (`ip`, `ping`, `trace`) | nenhuma: vem com o Gantry | Linux x86_64 |

Containers podem entrar nos mesmos cabos (`x-interfaces` num serviço): roteadores FRR, servidores, qualquer imagem Linux. O `examples/lab-mpls` é um lab que mistura os dois.

## Exemplo

```yaml
x-vms:
  R1:
    image: cisco-c7200            # Dynamips
    interfaces: [r1-sw1, r1-r2]
    config: equipamentos/R1.cfg
  R2:
    image: cisco-iol              # IOL L3
    interfaces: [r1-r2]
  SW1:
    image: cisco-iol-l2           # IOL L2
    interfaces: [r1-sw1, sw1-pc1]
  PC1:
    image: vpcs
    interfaces: [sw1-pc1]
    config: equipamentos/PC1.vpc
```

## Cabos (`interfaces:`)

- `interfaces:` lista as portas do equipamento, na ordem, e o cabo de cada
  uma. Dois equipamentos com o mesmo nome de cabo estão ligados.
- `none` é uma porta sem cabo, para pular uma porta e ligar a seguinte.
- Com mais de dois equipamentos no mesmo segmento, use uma rede do lab
  (`x-redes`) em vez de um cabo.
- A ordem das portas é a ordem das interfaces no IOS:
  - **IOL:** `Ethernet0/0`, `0/1`, `0/2`, `0/3`, depois `1/0`…`1/3`, e
    assim por diante, quatro por slot.
  - **Dynamips:** a ordem das placas, explicada abaixo.
  - **VPCS:** uma porta só.

## Configuração inicial (`config:`)

`config:` aponta para um arquivo do projeto com a configuração do equipamento
(`.cfg` para IOS, `.vpc` para o VPCS). Ela entra só no **primeiro boot**:

- **IOL:** o Gantry grava o arquivo na NVRAM antes de ligar o roteador.
- **Dynamips:** o próprio Dynamips grava o arquivo na NVRAM (`-C`).
- **VPCS:** o arquivo vira o `startup.vpc` dele.

Depois disso vale o que o equipamento salvou (`write memory`; no VPCS,
`save`). O `write memory` grava na NVRAM do nó, na pasta de estado do Gantry,
e não muda o arquivo do projeto; como a NVRAM já existe, o arquivo não é
aplicado de novo ao reiniciar. O VPCS é gravado sozinho (`save`) quando é
parado pelo Gantry, como no GNS3. Para voltar à configuração do arquivo,
remova o nó e suba de novo. Para guardar e voltar a
um ponto do exercício, use **Lab states**.

O texto do arquivo entra como está, sem correção. As linhas que o IOS
recusa ao ler a configuração (erro de digitação, comando de outra versão,
interface que o equipamento não tem) ficam guardadas: na lista de VMs do
projeto, o nó mostra **N linhas recusadas**, e clicar abre cada linha com a
mensagem do IOS. A contagem vale para o último início do nó; erros que você
digita depois, no console, não entram.

## Imagens

As imagens não vêm com o Gantry, porque a licença da Cisco não permite. Ponha
os arquivos numa das pastas de imagens da máquina (página da máquina ›
Configurações › Imagens). O Gantry reconhece o tipo pelo nome do arquivo.

Em `image:` vale:

- o nome do catálogo: `cisco-iol`, `cisco-iol-l2`, `cisco-c7200`,
  `cisco-c3725`, `cisco-c2691`, `vpcs`;
- o nome do arquivo (`c7200-adventerprisek9-mz.152-4.S6.image`);
- um nome dado em `x-imagens`, que deixa a versão do IOS num lugar só:

```yaml
x-imagens:
  iol-l3: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin
x-vms:
  R1:
    image: iol-l3
```

Se a imagem do IOL pedir licença, ponha o arquivo `iourc` na mesma pasta.

## Dynamips: placas, NPE e memória

Um roteador Dynamips é montado com placas, como o de verdade. Cada placa tem
um número de portas, e é ela que dá o nome da interface no IOS.

### Placas de porta (`slots:`)

`slots:` diz a placa de cada slot, a partir do slot 1. O slot 0 já vem com o
roteador. `none` deixa um slot vazio.

```yaml
R1:
  image: cisco-c7200
  slots: [PA-FE-TX, PA-GE, PA-4E]   # slot 1, 2, 3
  interfaces: [a, b, c, d]          # Fa0/0, Fa1/0, Gi2/0, Et3/0
```

| Modelo | Slot 0 (fixo) | Slots | Sem `slots:` | Placas para os slots |
|---|---|---|---|---|
| 7200 | `C7200-IO-FE`: Fa0/0 | 1 a 6 | `PA-2FE-TX` em todos | `PA-FE-TX` (1 FastEthernet), `PA-2FE-TX` (2 FastEthernet), `PA-4E` (4 Ethernet), `PA-8E` (8 Ethernet), `PA-GE` (1 GigabitEthernet) |
| 3725 | `GT96100-FE`: Fa0/0, Fa0/1 | 1 e 2 | `NM-16ESW` nos dois | `NM-1FE-TX` (1 FastEthernet), `NM-16ESW` (16 FastEthernet) |
| 2691 | `GT96100-FE`: Fa0/0, Fa0/1 | 1 | `NM-16ESW` | `NM-1FE-TX` (1 FastEthernet), `NM-16ESW` (16 FastEthernet) |

- Sem `slots:`, o Gantry só põe no roteador as placas que os cabos usam.
- Com `slots:`, todas as placas da lista entram, como no roteador original,
  mesmo sem cabo.
- Use `slots:` quando o exercício fala de interfaces com nome certo
  (`FastEthernet1/0`, `GigabitEthernet2/0`) ou quando o lab vem de outro
  lugar.
- Os nomes são os mesmos do Dynamips, do GNS3 e do PNETLab.

### Placa do processador (`npe:`), só no 7200

| Valor | Quando usar |
|---|---|
| `npe-400` | Padrão. Serve para qualquer IOS do 7200. |
| `npe-100`, `npe-150`, `npe-175`, `npe-200`, `npe-225`, `npe-300` | Para ficar igual a um lab que já usava essa placa. |

`npe-g1` e `npe-g2` pedem outras imagens e não são aceitos.

### Memória (`memory:`)

| Modelo | Padrão | Máximo no Gantry |
|---|---|---|
| 7200 | 256 MB | 256 MB |
| 3725, 2691 | 256 MB | sem limite no compose (o roteador real vai até 256 MB) |

No 7200, o Dynamips coloca a RAM a partir do endereço 0, e a ROM de boot fica
em 508 MB. Com mais de 256 MB, a RAM cobre a ROM ou outros dispositivos da
placa, e o roteador para logo ao iniciar com
`No memory map for code execution`. Por isso o compose recusa mais que isso.
Num lab do PNETLab com 512 MB e `npe-200`, o Dynamips de lá já reduzia para
256 MB.

### Idle-PC

O IOS do 7200 nunca para de rodar o loop de espera dele. Sem um idle-PC, o
Dynamips emula esse loop sem parar, e o roteador ocupa uma thread inteira do
processador, mesmo parado no prompt.

Para calcular: com o roteador ligado e no prompt (`Router>`), abra
**Gerenciar › Calcular idle-PC**. O valor fica guardado para a imagem e vale
para todos os roteadores com ela. Enquanto a imagem não tem idle-PC, a lista
de VMs mostra **sem idle-PC** ao lado do roteador; clicar abre o Gerenciar.

### Mensagens do Dynamips que não são erro

`udp_connect: bind/connect: No such file or directory` aparece uma vez por
porta quando o cabo **liga com sucesso**. É uma mensagem errada do próprio
Dynamips; pode ignorar.

## O que a máquina precisa

| Para | Instale |
|---|---|
| IOL, Dynamips e VPCS | `python3` |
| Dynamips | `dynamips` (Fedora: `sudo dnf install dynamips`; Debian/Ubuntu: `sudo apt install dynamips`; Mac: `brew install dynamips`) |
| IOL antigo de 32 bits (`i86bi_linux…`) | bibliotecas de 32 bits (Fedora: `sudo dnf install glibc.i686 libstdc++.i686 openssl-libs.i686`; Debian/Ubuntu: `sudo dpkg --add-architecture i386 && sudo apt update && sudo apt install libc6:i386 libstdc++6:i386 libssl3:i386`) |

A aba **Ferramentas** da máquina mostra o que falta e o comando de cada um.
O bloco **Imagens** avisa quando há imagens que a máquina ainda não consegue
rodar.

As imagens IOL antigas pedem `libcrypto.so.4`, um nome do OpenSSL de 2005. O
Gantry liga esse nome à `libcrypto` do sistema numa pasta dele, sem root.

## Uso de processador e memória

Cada equipamento é um processo comum da máquina e não reserva núcleo. Na aba
Métricas, "% de um núcleo" é o uso de uma thread do processador.

- **IOL:** dorme quando não tem trabalho; parado, fica em poucos por cento.
- **Dynamips:** fica em ~100% de uma thread até ganhar um idle-PC.
- **VPCS:** quase nada.

## Labs do PNETLab e do EVE-NG (`.unl`)

Página da máquina › Projetos › **Outras formas › Importar do PNETLab/EVE-NG**,
ou na linha de comando:

```sh
gantry importar-unl LAB.unl PASTA
```

A importação não adapta nada: mesmos equipamentos, placas, NPE, cabos e
configurações salvas, inclusive erros que o professor tenha posto de
propósito. Equipamento que o Gantry ainda não roda impede a importação. O
que mudou ou ficou de fora vai para o `IMPORTACAO.md` da pasta, e o arquivo
original fica em `original/`.

Mudanças que o Gantry é obrigado a fazer, sempre anotadas no
`IMPORTACAO.md`:

- **Memória acima de 256 MB num 7200:** vira 256 MB (veja acima).
- **idle-PC do original:** fica só anotado, porque o valor certo é calculado
  na máquina, em Gerenciar.
- **Redes `pnet` (a nuvem do PNETLab):** as portas ligadas nelas ficam sem
  cabo.
- **Posições do desenho:** os ícones do PNETLab são pequenos; quando os
  equipamentos ficariam encostados nas caixas do Gantry, as posições são
  afastadas na mesma proporção, sem mudar a disposição.

A importação também aponta, sem mudar, o que parece errado no original, na
seção "Pontos de atenção no original" do `IMPORTACAO.md` (e num aviso ao
terminar). Num exercício de troubleshooting pode ser de propósito.

- **Switch com imagem de roteador:** nó com nome de switch (`SW…`) rodando
  uma imagem IOL L3. Sem configuração, as portas dele ficam desligadas e ele
  não comuta quadros.
- **Interface que o equipamento não tem:** configuração com uma interface
  Ethernet (`FastEthernet4/0`, por exemplo) que as placas do nó não têm.

Erros de endereçamento e de OSPF na configuração não são apontados. É isso
que o aluno tem que achar.
