// The own-setup VM of "Criar projeto", written as x-vms.
import { t } from "@/lib/i18n";

// The Gantry NixOS image: the VM is configured by a NixOS module of the
// project (nix:), applied again in the running VM when the file changes.
export const NIXOS_IMAGE = "nixos-lab-26.05";

export const NIXOS_SAMPLE = `# ${t("Módulo NixOS da VM: qualquer opção do NixOS vale aqui.")}
# ${t("O Gantry entrega este arquivo em /etc/nixos/lab.nix e aplica com nixos-rebuild.")}
# ${t("Valores da instância vêm de config.gantry.params (nix: params: no compose).")}
{ config, pkgs, ... }:
{
  networking.hostName = config.gantry.params.hostname;
  time.timeZone = "America/Sao_Paulo";
  environment.systemPackages = with pkgs; [ vim htop ];
}
`;

export const VM_SYSTEMS: [string, string][] = [
  ["debian-12", "Debian 12"],
  ["debian-13", "Debian 13"],
  ["ubuntu-24.04", "Ubuntu 24.04"],
  ["ubuntu-22.04", "Ubuntu 22.04"],
  ["rocky-9", "Rocky Linux 9"],
  ["alma-9", "AlmaLinux 9"],
  [NIXOS_IMAGE, t("NixOS 26.05 (módulo Nix)")],
  ["cisco-iosvl2", t("Cisco IOSvL2 (switch)")],
  ["cisco-iosv", t("Cisco IOSv (roteador)")],
  ["cisco-csr1000v", t("Cisco CSR 1000v (roteador)")],
  ["cisco-iol", t("Cisco IOL (roteador, .bin, só Linux x86_64)")],
  ["cisco-iol-l2", t("Cisco IOL L2 (switch, .bin, só Linux x86_64)")],
  ["cisco-c7200", t("Cisco 7200 (roteador, Dynamips)")],
  ["cisco-c3725", t("Cisco 3725 (roteador, Dynamips)")],
  ["cisco-c2691", t("Cisco 2691 (roteador, Dynamips)")],
  ["image", t("Imagem própria (qcow2)")],
  ["iso", t("Instalar de uma ISO")],
];

// Snippets for the VM startup script (cloud-init runs them once, as root).
export const VM_SNIPPETS: [string, string][] = [
  [t("Atualizar o sistema"), "apt-get update && apt-get -y upgrade || dnf -y upgrade"],
  ["Docker", "curl -fsSL https://get.docker.com | sh\nusermod -aG docker gantry"],
  ["Podman", "apt-get install -y podman || dnf install -y podman"],
  [t("Nginx com uma página"), "apt-get install -y nginx || dnf install -y nginx\necho '<meta charset=\"utf-8\"><h1>Olá do Gantry</h1>' > /var/www/html/index.html\nsystemctl enable --now nginx"],
  [t("k3s (Kubernetes leve)"), "curl -sfL https://get.k3s.io | sh -"],
  [t("Fuso horário de São Paulo"), "timedatectl set-timezone America/Sao_Paulo"],
  [t("Usuário com senha"), "useradd -m -s /bin/bash -G sudo dev || useradd -m -s /bin/bash -G wheel dev\necho 'dev:troque-esta-senha' | chpasswd"],
];

export const CLOUD_INIT_SAMPLE = `# ${t("cloud-init extra (opcional), mesclado ao que o Gantry gera")}
write_files:
  - path: /etc/motd
    content: |
      ${t("VM criada pelo Gantry")}
timezone: America/Sao_Paulo
`;

export type VMSetup = {
  name: string;
  os: string;
  image: string;
  iso: string;
  cpus: string;
  memory: string;
  disk: string;
  ports: string;
  packages: string;
  script: string;
  cloudInit: string;
  lan?: boolean;
  metrics?: boolean; // node_exporter inside, graphs in Métricas
  tela?: boolean;
  nixModule?: string;
  ports2?: string; // ports (cables) of a network appliance
};

// isAppliance: the catalog images of network appliances (Cisco), with no
// cloud-init and no management NIC.
export const isAppliance = (os: string) => /^cisco-/.test(os);

// isProgram: IOL and Dynamips nodes run as a program, not a QEMU VM (only
// memory to set: no CPUs, no disk).
export const isProgram = (os: string) => /^cisco-(iol|c\d)/.test(os);

export const newVM = (): VMSetup => ({ name: "vm", os: "debian-12", image: "", iso: "", cpus: "2", memory: "2g", disk: "20G", ports: "", packages: "", script: "", cloudInit: "" });

// yq quotes a YAML scalar when needed.
export const yq = (s: string) =>
  /^[A-Za-z0-9_./@:+-]+$/.test(String(s)) && !/^(true|false|yes|no|null|~)$/i.test(String(s)) && !/^[0-9.]+$/.test(String(s)) ? String(s) : JSON.stringify(String(s));

export function vmYAML(v: VMSetup): string {
  const name = (v.name || "vm").trim().replace(/[^A-Za-z0-9_.-]/g, "-");
  if (!/^[A-Za-z0-9]/.test(name)) throw new Error(t("Nome da VM: comece com letra ou número."));
  const out = ["x-vms:", "  " + name + ":"];
  if (v.os === "iso") {
    if (!v.iso.trim()) throw new Error(t("Informe o caminho ou a URL da ISO."));
    out.push("    iso: " + yq(v.iso.trim()));
  } else if (v.os === "image") {
    if (!v.image.trim()) throw new Error(t("Informe a imagem (URL ou caminho do qcow2)."));
    out.push("    image: " + yq(v.image.trim()));
  } else out.push("    image: " + v.os);
  const nixos = v.os === NIXOS_IMAGE;
  const appliance = isAppliance(v.os);
  // An IOL node is a program, not a QEMU VM: no CPUs or disk to set.
  if (isProgram(v.os)) out.push("    memory: " + yq(v.memory || "512m"));
  else out.push("    cpus: " + (parseInt(v.cpus, 10) || 1), "    memory: " + yq(v.memory || "1g"), "    disk: " + yq((v.disk || "10G").toUpperCase()));
  const ports = isAppliance(v.os) ? [] : (v.ports || "").split(/[,\s]+/).filter(Boolean);
  if (ports.length) out.push("    ports: [" + ports.map((p) => JSON.stringify(p)).join(", ") + "]");
  if (v.lan) out.push("    lan: true");
  if (v.metrics && !appliance && v.os !== "iso" && !v.tela) out.push("    metrics: true");
  if (v.tela && v.os === "image") out.push("    tela: true  # tela pelo VNC (sistemas sem cloud-init)");
  if (nixos) {
    out.push("    nix:", "      module: ./" + name + ".nix");
  } else if (appliance) {
    // A network appliance: its ports are cables (set in Cabos later).
    const n = Math.max(0, Math.min(28, parseInt(v.ports2 ?? "4", 10) || 0));
    if (n) out.push("    interfaces: [" + Array.from({ length: n }, () => "none").join(", ") + "]");
  } else if (v.os !== "iso") {
    const pk = (v.packages || "").split(/[,\s]+/).filter(Boolean);
    if (pk.length) out.push("    packages: [" + pk.map(yq).join(", ") + "]");
    const script = (v.script || "").replace(/\s+$/, "");
    if (script) out.push("    run:", "      - |", ...script.split("\n").map((l) => "        " + l));
    const ci = (v.cloudInit || "").replace(/\s+$/, "");
    if (ci) out.push("    cloud-init: |", ...ci.split("\n").map((l) => "      " + l));
  }
  return out.join("\n") + "\n";
}

// vmFiles: the files the own-setup VM brings to the project folder (the
// NixOS module of a NixOS VM).
export function vmFiles(v: VMSetup): Record<string, string> {
  if (v.os !== NIXOS_IMAGE) return {};
  const name = (v.name || "vm").trim().replace(/[^A-Za-z0-9_.-]/g, "-");
  const mod = (v.nixModule ?? "").trim() ? (v.nixModule as string).replace(/\s*$/, "\n") : NIXOS_SAMPLE;
  return { [name + ".nix"]: mod };
}

/* ports */

export type PortInfo = { host: string; ports: Record<string, string> | null; min: number };
export type PortNote = { text: string; warn?: boolean } | null;

// portVars: the variables used as a host port ("${PORTA:-80}:80").
export function portVars(yaml: string): Set<string> {
  const out = new Set<string>();
  for (const m of (yaml || "").matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?-\d+)?\}:\d+/g)) out.add(m[1]);
  return out;
}

export function portWhy(b: PortInfo | null, n: number): string {
  const busy = b?.ports ?? {};
  if (busy[n]) return busy[n].startsWith("container") ? t("em uso pelo {who}", { who: busy[n] }) : t("em uso por {who}", { who: busy[n] });
  if (b?.min && n < b.min) return t("abaixo de {min}, o Podman sem root não abre", { min: b.min });
  return "";
}

// fixPorts swaps, in the ports the user did not touch, the ones in use (or
// repeated) for the next free one, and notes why.
export function fixPorts(b: PortInfo | null, yaml: string, vars: { name: string; default?: string }[], vals: Record<string, string>, touched: Record<string, boolean>, notes: Record<string, PortNote>) {
  if (!b?.ports) return;
  const names = portVars(yaml);
  const taken = new Set<number>();
  for (const v of vars) {
    if (!names.has(v.name)) continue;
    const n = parseInt(vals[v.name], 10);
    if (!(n > 0 && n < 65536)) continue;
    if (touched[v.name]) {
      taken.add(n);
      continue;
    }
    const want = parseInt(v.default ?? "", 10) || n;
    const why = portWhy(b, want) || (taken.has(want) ? t("repetida em outra variável") : "");
    let p = want;
    if (why) {
      // 80 becomes 8080 when low ports do not open; otherwise the next one.
      p = b.min && want < b.min ? Math.max(want + 8000, b.min) : want + 1;
      while (p < 65535 && (portWhy(b, p) || taken.has(p))) p++;
    }
    vals[v.name] = String(p);
    notes[v.name] = why ? { text: t("A porta {port} está {why}; usando {next}.", { port: want, why, next: p }) } : null;
    taken.add(p);
  }
}

export function portWarning(b: PortInfo | null, yaml: string, name: string, value: string): PortNote {
  if (!portVars(yaml).has(name)) return null;
  const n = parseInt(value, 10);
  if (!(n > 0)) return null;
  const why = portWhy(b, n);
  return why ? { text: t("A porta {port} está {why}. O projeto não vai subir assim.", { port: n, why }), warn: true } : null;
}
