# Imagem base NixOS das VMs do Gantry

A imagem `nixos-lab-26.05` do catálogo de VMs. Ela é sempre a mesma; o que
muda por VM é o módulo NixOS do lab (`nix: module:` no `x-vms`) e os
parâmetros da instância (`nix: params:`), que o Gantry entrega em
`/etc/nixos/lab.nix` e `/etc/nixos/gantry-params.json`. Dentro da VM, o
serviço `gantry-apply` roda `nixos-rebuild switch` quando esses dois arquivos
mudam e grava o resultado em `/var/lib/gantry/status` (`ok <hash>` ou
`failed <hash>`), com o log em `/var/lib/gantry/last-apply.log`.

## Gerar

```bash
# Mac com Apple Silicon (VMs aarch64)
nix build ./images/nixos#packages.aarch64-linux.base-qcow2
# Linux x86_64
nix build ./images/nixos#packages.x86_64-linux.base-qcow2
```

O arquivo sai em `result/nixos-lab-base.qcow2`.

**É preciso um builder Linux com KVM.** A imagem é montada dentro de uma VM
(`make-disk-image`), então o Nix precisa de um builder `*-linux` com a
feature `kvm`:

- **Linux:** o próprio Nix da máquina, com `/dev/kvm` acessível ao usuário
  que builda (ou ao daemon). Confira com `nix show-config | grep features`;
  `kvm` precisa aparecer em `system-features`.
- **macOS:** um builder Linux remoto ou local. Com nix-darwin, o caminho mais
  simples é `nix.linux-builder.enable = true;` (uma VM aarch64-linux com KVM
  dentro do próprio Mac). Sem nix-darwin, use qualquer máquina Linux com KVM
  como builder remoto (`--builders 'ssh://servidor aarch64-linux'`).

O build de uma arquitetura diferente da do builder não funciona sem
emulação; gere a imagem `aarch64-linux` num builder aarch64 e a `x86_64-linux`
num builder x86_64.

## Usar no Gantry

O Gantry não baixa esta imagem. Depois de gerar, informe onde está o
arquivo em **Configurações › Geral › Imagens de VM**, para cada máquina que
vai rodar as VMs (o caminho é o da máquina, não o deste computador). Copie
o `.qcow2` para fora de `result/` antes: o `result` é um link para o Nix
store e muda a cada build. A imagem fica onde está; cada VM usa um disco
próprio por cima dela (overlay qcow2), então não apague nem regere o arquivo
no mesmo lugar enquanto houver VMs criadas a partir dele.

Em x86_64 a imagem só inicia com UEFI: a máquina precisa do firmware OVMF
(`ovmf` no Debian/Ubuntu, `edk2-ovmf` no Fedora; no macOS ele vem com o
`brew install qemu`).

## O que difere do anexo da spec

- `gantry-apply` tem o `nix` no `runtimeInputs` e o `NIX_PATH` no ambiente do
  serviço: o `nix.nixPath` só chega às sessões de login, e sem ele o
  `nixos-rebuild` dentro do serviço não acha `<nixpkgs/nixos>`.
- Quando o lab já está aplicado, o script reescreve `ok <hash>` no status.
  Assim, voltar os arquivos para a última versão boa depois de uma falha não
  deixa o status preso em `failed`.
