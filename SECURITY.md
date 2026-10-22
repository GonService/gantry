# Security policy

**English** · [Português](#português)

Gantry runs commands on your machines (containers, VMs, labs and clusters), so security issues matter a lot.

## How to report

Please do not open a public issue. Use **Report a vulnerability** in this repository's **Security** tab (GitHub private reporting). Include:

- the Gantry version and the system (macOS, Linux, Windows);
- what an attacker can do and from where (another site in the browser, another person on the network, a lab file, a kubeconfig…);
- the steps to reproduce.

The first answer comes within 7 days. The fix ships in a new version, and the report is published after it is available, crediting the reporter if they wish.

## Supported versions

Only the newest version.

## What Gantry assumes

- The interface talks to Gantry through a local API on `127.0.0.1`, protected by a key. The `-expor-na-rede` mode exists, but without encryption: prefer an SSH tunnel.
- A lab is code: bringing a lab up runs its containers, VMs and commands, like `docker compose up`. Only bring up labs from people you trust.
- Importing or opening a lab, a `.tar.gz` package or a kubeconfig must not run anything or write outside the chosen folder. If that happens, it is a vulnerability.

## Português

[English](#security-policy)

O Gantry executa comandos nas suas máquinas (containers, VMs, labs e clusters), então falhas de segurança importam muito.

### Como reportar

Não abra uma issue pública. Use o **Report a vulnerability** da aba **Security** deste repositório (relato privado do GitHub). Diga:

- a versão do Gantry e o sistema (macOS, Linux, Windows);
- o que um atacante consegue fazer e a partir de onde (outro site no navegador, outra pessoa na rede, um arquivo de lab, um kubeconfig…);
- os passos para reproduzir.

A resposta inicial vem em até 7 dias. A correção sai numa versão nova, e o relato é publicado depois que ela estiver disponível, com o crédito para quem reportou, se quiser.

### Versões que recebem correção

Só a versão mais nova.

### O que o Gantry assume

- A interface fala com o Gantry por uma API local, em `127.0.0.1`, protegida por uma chave. O modo `-expor-na-rede` existe, mas sem criptografia: prefira um túnel SSH.
- Um lab é código: subir um lab roda os containers, as VMs e os comandos dele, como um `docker compose up`. Só suba labs de quem você confia.
- Importar ou abrir um lab, um pacote `.tar.gz` ou um kubeconfig não deve executar nada nem escrever fora da pasta escolhida. Se isso acontecer, é uma falha.
