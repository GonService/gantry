// The Container VM: types and the actions shared by the sidebar, the
// engines tab and the machine's page.
import { api, enc } from "@/lib/api";
import { loadHosts, loadLocal, useStore } from "@/lib/store";
import { openJob } from "@/shell/dock";
import { toast } from "@/shell/toasts";
import { t } from "@/lib/i18n";

export type Spec = {
  name: string;
  cpus: number;
  memoryMiB: number;
  systemGiB: number;
  dataGiB: number;
  docker: boolean;
  podman: boolean;
  containerd?: boolean;
  rosetta: boolean;
  qemu: boolean;
  sshAgent: boolean;
  extra?: string;
  image?: string;
  autoStart: boolean;
  idleStopMin?: number;
  shares?: Share[];
  snapDaily?: boolean;
  snapKeep?: number;
};

export type Share = { path: string; readOnly?: boolean };

export type Integration = {
  dockerContext: string;
  podmanContext: string;
  podmanConn: boolean;
  podmanCli: boolean;
  dockerCli: string;
  path: boolean;
  rc: string;
  link: boolean;
  linkOther?: string;
  shims: string[];
  currentContext: string;
  dockerSock?: string;
  podmanSock?: string;
  binDir: string;
  commands: string[];
};

export type Snap = { id: string; at: number; reason: string; auto: boolean; extra: number };
export type Platform = { name: string; how: string; active: boolean };
export type Creds = { agent: boolean; agentKeys: number; agentSock: string; registries: string[]; store: string; folders: string[] };

export const recreateHref = (name: string, engine?: string) => "#/cvm/criar?de=" + enc(name) + (engine ? "&engine=" + enc(engine) : "");

export type DiskUse = { name: string; size: number; occupied: number };

export type CvmView = {
  name: string;
  state: string;
  spec: Spec;
  disks: DiskUse[];
  docker?: string;
  podman?: string;
  dir: string;
  scriptSum?: string;
  pending?: boolean;
};

export type CvmList = { supported: boolean; vfkit: string; home: string; arch: string; default: Spec; machines: CvmView[]; image: string };

export type Proc = {
  pid: number;
  user: string;
  state: string;
  cpu: number;
  rss: number;
  elapsed: number;
  threads: number;
  cmd: string;
  kind: string;
  origin: string;
  rosetta?: boolean;
};

export type Snapshot = {
  at: number;
  procs: Proc[];
  cores: { user: number; system: number }[];
  cpu: number;
  memTotal: number;
  memUsed: number;
  memBuffers: number;
  memCached: number;
  swapTotal: number;
  swapUsed: number;
  load: string;
  diskRead: number;
  diskWrite: number;
  netIn: number;
  netOut: number;
  tasks: number;
  threads: number;
  running: number;
  first?: boolean;
};

export const cvmHref = (name: string, tab?: string) => "#/cvm/" + enc(name) + (tab && tab !== "processos" ? "/" + tab : "");

export const STATE: Record<string, [string, string]> = {
  running: ["on", t("rodando")],
  starting: ["warn", t("ligando")],
  stopping: ["warn", t("desligando")],
  paused: ["warn", t("pausada")],
  stopped: ["", t("desligada")],
  missing: ["err", t("não existe")],
};

// The state of a machine from the engines of this computer.
export function cvmState(local: any, name: string): string {
  const e = (local?.engines || []).find((x: { id: string }) => x.id === "cvm:" + name);
  return e ? (e.state === "running" ? "running" : e.state === "starting" ? "starting" : "stopped") : "stopped";
}

export function refreshAfter() {
  useStore.setState({ localBusy: false });
  void loadLocal();
  void loadHosts(true);
}

// engineNames lists the engines of a spec.
export const engineNames = (s: Spec) => [s.docker ? "Docker" : "", s.podman ? "Podman" : "", s.containerd ? "containerd" : ""].filter(Boolean).join(" · ");

// power runs ligar, desligar, reiniciar or apagar in a job of the dock.
export async function power(name: string, act: "ligar" | "desligar" | "reiniciar" | "apagar", after?: () => void) {
  const label = { ligar: t("Ligar {name}", { name }), desligar: t("Desligar {name}", { name }), reiniciar: t("Reiniciar {name}", { name }), apagar: t("Apagar {name}", { name }) }[act];
  try {
    const r = await api<{ id: string }>("/cvm/" + enc(name) + "/" + act, { method: "POST" });
    openJob(r.id, label, () => {
      refreshAfter();
      after?.();
    });
  } catch (e) {
    toast((e as Error).message, "err");
  }
}

export function fmtRate(n: number): string {
  if (!n || n < 1) return "0 B/s";
  const u = ["B/s", "KB/s", "MB/s", "GB/s"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) {
    n /= 1000;
    i++;
  }
  return (n < 10 ? n.toFixed(1) : n.toFixed(0)).replace(".", ",") + " " + u[i];
}
