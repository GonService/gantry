# Network equipment in labs

**English** · [Português](equipamentos-de-rede.md) · [Reference](reference.md)

Routers, switches and lab computers in Gantry: IOL, Dynamips and VPCS. They go in `x-vms`, like VMs, but they are not VMs: each one is a regular program on the host machine, connected to the others by cables.

| Kind | What it is | Image | Runs on |
|---|---|---|---|
| IOL | IOS on Linux: a Cisco router (L3) or switch (L2) | `.bin` (`i86bi_linux…`, `x86_64_crb_linux…`) | Linux x86_64 |
| Dynamips | Emulator of the classic Cisco routers: 7200, 3725, 2691 | an IOS `.image` or `.bin` (`c7200-…`, `c3725-…`) | Linux, Mac |
| VPCS | A simple lab computer (`ip`, `ping`, `trace`) | none: it comes with Gantry | Linux x86_64 |

Containers can join the same cables (`x-interfaces` on a service): FRR routers, servers, any Linux image. See `examples/lab-mpls` for a lab that mixes both.

## Example

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

## Cables (`interfaces:`)

- `interfaces:` lists the ports of the device, in order, and the cable of each one. Two devices with the same cable name are connected.
- `none` is a port without a cable, to skip a port and wire the next one.
- With more than two devices on the same segment, use a lab network (`x-redes`) instead of a cable.
- The port order is the order of the interfaces in IOS:
  - **IOL:** `Ethernet0/0`, `0/1`, `0/2`, `0/3`, then `1/0`…`1/3`, and so on, four per slot.
  - **Dynamips:** the order of the cards, explained below.
  - **VPCS:** a single port.

## Startup configuration (`config:`)

`config:` points to a project file with the device configuration (`.cfg` for IOS, `.vpc` for VPCS). It goes in only on the **first boot**:

- **IOL:** Gantry writes the file to NVRAM before starting the router.
- **Dynamips:** Dynamips itself writes the file to NVRAM (`-C`).
- **VPCS:** the file becomes its `startup.vpc`.

After that, what the device saved (`write memory`; on VPCS, `save`) is what counts. `write memory` writes the node's NVRAM, in Gantry's state folder, and does not change the project file; since the NVRAM exists, the file is not applied again on restart. A VPCS is saved on its own (`save`) when Gantry stops it, as GNS3 does. To go back to the file configuration, remove the node and bring it up again. To keep a point of the exercise and go back to it, use **Lab states**.

The file text goes in as it is, without corrections. The lines IOS rejects while reading the configuration (a typo, a command from another version, an interface the device does not have) are kept: in the project's VM list, the node shows **N lines refused**, and clicking it opens each line with the IOS message. The count is for the last start of the node; errors you type later, on the console, are not included.

## Images

Images do not come with Gantry, because Cisco's license does not allow it. Put the files in one of the machine's image folders (machine page › Settings › Images). Gantry recognizes the kind by the file name.

In `image:` you can use:

- the catalog name: `cisco-iol`, `cisco-iol-l2`, `cisco-c7200`, `cisco-c3725`, `cisco-c2691`, `vpcs`;
- the file name (`c7200-adventerprisek9-mz.152-4.S6.image`);
- a name given in `x-imagens`, which keeps the IOS version in one place:

```yaml
x-imagens:
  iol-l3: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin
x-vms:
  R1:
    image: iol-l3
```

If the IOL image asks for a license, put the `iourc` file in the same folder.

## Dynamips: cards, NPE and memory

A Dynamips router is built from cards, like the real one. Each card has a number of ports, and the card gives the interface its name in IOS.

### Port adapters (`slots:`)

`slots:` says the card in each slot, starting at slot 1. Slot 0 comes with the router. `none` leaves a slot empty.

```yaml
R1:
  image: cisco-c7200
  slots: [PA-FE-TX, PA-GE, PA-4E]   # slot 1, 2, 3
  interfaces: [a, b, c, d]          # Fa0/0, Fa1/0, Gi2/0, Et3/0
```

| Model | Slot 0 (fixed) | Slots | Without `slots:` | Cards for the slots |
|---|---|---|---|---|
| 7200 | `C7200-IO-FE`: Fa0/0 | 1 to 6 | `PA-2FE-TX` in all | `PA-FE-TX` (1 FastEthernet), `PA-2FE-TX` (2 FastEthernet), `PA-4E` (4 Ethernet), `PA-8E` (8 Ethernet), `PA-GE` (1 GigabitEthernet) |
| 3725 | `GT96100-FE`: Fa0/0, Fa0/1 | 1 and 2 | `NM-16ESW` in both | `NM-1FE-TX` (1 FastEthernet), `NM-16ESW` (16 FastEthernet) |
| 2691 | `GT96100-FE`: Fa0/0, Fa0/1 | 1 | `NM-16ESW` | `NM-1FE-TX` (1 FastEthernet), `NM-16ESW` (16 FastEthernet) |

- Without `slots:`, Gantry only puts in the router the cards the cables use.
- With `slots:`, every card in the list goes in, as in the original router, even without a cable.
- Use `slots:` when the exercise talks about interfaces by name (`FastEthernet1/0`, `GigabitEthernet2/0`) or when the lab comes from somewhere else.
- The names are the same as in Dynamips, GNS3 and PNETLab.

### Processor board (`npe:`), 7200 only

| Value | When to use |
|---|---|
| `npe-400` | Default. Works for any 7200 IOS. |
| `npe-100`, `npe-150`, `npe-175`, `npe-200`, `npe-225`, `npe-300` | To match a lab that already used that board. |

`npe-g1` and `npe-g2` need other images and are not accepted.

### Memory (`memory:`)

| Model | Default | Maximum in Gantry |
|---|---|---|
| 7200 | 256 MB | 256 MB |
| 3725, 2691 | 256 MB | no limit in the compose (the real router goes up to 256 MB) |

On the 7200, Dynamips places the RAM from address 0, and the boot ROM is at 508 MB. With more than 256 MB, the RAM covers the ROM or other devices of the board, and the router stops right at start with `No memory map for code execution`. That is why the compose refuses more than that. In a PNETLab lab with 512 MB and `npe-200`, PNETLab's Dynamips already reduced it to 256 MB.

### Idle-PC

The 7200 IOS never stops running its idle loop. Without an idle-PC, Dynamips emulates that loop nonstop, and the router takes a whole processor thread, even idle at the prompt.

To calculate it: with the router on and at the prompt (`Router>`), open **Manage › Compute idle-PC**. The value is kept for the image and applies to every router using it. While the image has no idle-PC, the VM list shows **no idle-PC** next to the router; clicking it opens Manage.

### Dynamips messages that are not errors

`udp_connect: bind/connect: No such file or directory` appears once per port when the cable **connects successfully**. It is a wrong message from Dynamips itself; you can ignore it.

## What the machine needs

| For | Install |
|---|---|
| IOL, Dynamips and VPCS | `python3` |
| Dynamips | `dynamips` (Fedora: `sudo dnf install dynamips`; Debian/Ubuntu: `sudo apt install dynamips`; Mac: `brew install dynamips`) |
| Old 32-bit IOL (`i86bi_linux…`) | 32-bit libraries (Fedora: `sudo dnf install glibc.i686 libstdc++.i686 openssl-libs.i686`; Debian/Ubuntu: `sudo dpkg --add-architecture i386 && sudo apt update && sudo apt install libc6:i386 libstdc++6:i386 libssl3:i386`) |

The machine's **Tools** tab shows what is missing and the command for each. The **Images** block warns when there are images the machine cannot run yet.

Old IOL images ask for `libcrypto.so.4`, an OpenSSL name from 2005. Gantry links that name to the system's `libcrypto` in a folder of its own, without root.

## Processor and memory usage

Each device is a regular process on the machine and does not reserve a core. In the Metrics tab, "% of one core" is the usage of one processor thread.

- **IOL:** sleeps when it has no work; idle, it stays at a few percent.
- **Dynamips:** stays at ~100% of a thread until it gets an idle-PC.
- **VPCS:** almost nothing.

## PNETLab and EVE-NG labs (`.unl`)

Machine page › Projects › **Other ways › Import from PNETLab/EVE-NG**, or on the command line:

```sh
gantry importar-unl LAB.unl FOLDER
```

The import does not adapt anything: same devices, cards, NPE, cables and saved configurations, including mistakes a teacher may have put there on purpose. A device Gantry does not run yet stops the import. What changed or was left out goes to the folder's `IMPORTACAO.md` (written in Portuguese), and the original file stays in `original/`.

Changes Gantry has to make, always noted in `IMPORTACAO.md`:

- **More than 256 MB of memory on a 7200:** becomes 256 MB (see above).
- **The original idle-PC:** only noted, because the right value is calculated on the machine, in Manage.
- **`pnet` networks (PNETLab's cloud):** the ports connected to them stay without a cable.
- **Drawing positions:** PNETLab icons are small; when devices would touch in Gantry's boxes, the positions are spread in the same proportion, without changing the arrangement.

The import also points out, without changing them, what looks wrong in the original, in the "Pontos de atenção no original" (points of attention in the original) section of `IMPORTACAO.md` (and in a notice when it finishes). In a troubleshooting exercise it may be on purpose.

- **Switch with a router image:** a node with a switch name (`SW…`) running an IOL L3 image. Without configuration, its ports stay down and it does not switch frames.
- **Interface the device does not have:** a configuration with an Ethernet interface (`FastEthernet4/0`, for example) that the node's cards do not have.

Addressing and OSPF errors in the configuration are not pointed out. That is what the student has to find.
