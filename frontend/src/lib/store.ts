// The state of the app: the machines (hosts), their projects and stats,
// this computer's engines, the route, the folders of the sidebar and the
// clusters. Loaders keep it fresh; views read it with useStore.
import { create } from "zustand";
import { api, apiURL, enc } from "./api";
import { t } from "./i18n";
import type { Cluster, KindInfo } from "@/views/kube/types";

/* eslint-disable @typescript-eslint/no-explicit-any */
export type Host = {
  name: string;
  url: string;
  kind: "unix" | "ssh" | "tcp" | "wsl" | string;
  source: string;
  runtime?: string;
  version?: string;
  ok: boolean;
  idle?: boolean;
  connecting?: boolean;
  error?: string;
  fix?: { id: string; label: string };
  files?: boolean;
  compose: string[];
  composeConfig?: string[];
  socket?: string;
  machine?: string;
  agent?: { state: string; via?: string; version?: string; vms?: boolean; [k: string]: any } | null;
  info?: Record<string, any>;
};
export type Machine = { key: string; local: boolean; name: string; hosts: Host[]; primary: Host | null };
export type Folder = { id: string; name: string; parent?: string };
export type Groups = { folders: Folder[]; items: Record<string, string> };

export type Route = {
  q: Record<string, string>;
  config?: string;
  welcome?: boolean;
  kube?: string;
  kkind?: string;
  local?: boolean;
  newProj?: boolean;
  mtab?: string;
  meng?: string;
  mseg?: string;
  tpl?: boolean;
  tplEdit?: string;
  tplDeploy?: string;
  host?: string;
  lab?: string;
  tab?: string;
  build?: boolean;
  newCtr?: boolean;
  ctr?: string;
  ctrRt?: string;
  ctrMode?: string;
};

export function parseRoute(hash: string): Route {
  const [path, query] = hash.replace(/^#\/?/, "").split("?");
  const p = path.split("/").map((x) => {
    try {
      return decodeURIComponent(x);
    } catch {
      return x;
    }
  });
  const r: Route = { q: Object.fromEntries(new URLSearchParams(query || "")) };
  const machineTab = (i: number) => {
    if (p[i] === "t" && p[i + 1]) {
      r.mtab = p[i + 1];
      r.meng = p[i + 2] || "";
      r.mseg = p[i + 3] || "";
    }
  };
  if (p[0] === "config") r.config = p[1] || "maquinas";
  if (p[0] === "bem-vindo") r.welcome = true;
  if (p[0] === "k" && p[1]) {
    r.kube = p[1];
    r.kkind = p[2] || "";
  }
  if (p[0] === "local") {
    r.local = true;
    if (p[1] === "novo") r.newProj = true;
    else machineTab(1);
  }
  if (p[0] === "modelos") {
    r.tpl = true;
    if (p[1] === "novo") r.tplEdit = "novo";
    else if (p[1] && p[2] === "gerar") r.tplDeploy = p[1];
    else if (p[1]) r.tplEdit = p[1];
  }
  if (p[0] === "h" && p[1]) {
    r.host = p[1];
    if (p[2] === "l" && p[3]) {
      r.lab = p[3];
      r.tab = p[4] || "containers";
    } else if (p[2] === "t") machineTab(2);
    else if (p[2] === "construir") r.build = true;
    else if (p[2] === "novo") r.newProj = true;
    else if (p[2] === "criar") {
      r.newCtr = true;
      r.ctrRt = p[3] || "";
    } else if (p[2] === "c" && p[3] && p[4]) {
      r.ctr = p[4];
      r.ctrRt = p[3] === "-" ? "" : p[3];
      r.ctrMode = p[5] === "editar" || p[5] === "duplicar" ? p[5] : "";
    }
  }
  return r;
}

export const LOCAL_NAME = /Mac/i.test(navigator.platform || navigator.userAgent) ? t("Este Mac") : t("Esta máquina");

export type State = {
  hosts: Host[];
  hostsLoaded: boolean;
  labs: Record<string, any[]>;
  labsErr: Record<string, string>;
  stats: Record<string, Record<string, { cpu: number; mem: number; disk?: number }>>;
  busy: Record<string, string>;
  local: any;
  hidden: string[] | null;
  localBusy: boolean;
  route: Route;
  routeKey: string;
  visit: number;
  filter: string;
  groups: Groups | null;
  clusters: Cluster[] | null;
  kinds: KindInfo[] | null;
};

export const useStore = create<State>(() => {
  const route = parseRoute(location.hash);
  return {
    hosts: [],
    hostsLoaded: false,
    labs: {},
    labsErr: {},
    stats: {},
    busy: {},
    local: null,
    hidden: null,
    localBusy: false,
    route,
    routeKey: JSON.stringify(route),
    visit: 1,
    filter: "",
    groups: null,
    clusters: null,
    kinds: null,
  };
});

const get = useStore.getState;
const set = useStore.setState;

// The route follows the hash; forms opened again start empty (visit).
export function followHash() {
  const route = parseRoute(location.hash);
  const key = JSON.stringify(route);
  if (key === get().routeKey) return;
  set((s) => ({ route, routeKey: key, visit: s.visit + 1 }));
  syncEvents();
}

export const hostByName = (n: string) => get().hosts.find((x) => x.name === n);

let machinesCache: { hosts: Host[]; list: Machine[] } | null = null;
// machines groups the hosts by the computer behind them.
export function machines(hosts = get().hosts): Machine[] {
  if (machinesCache && machinesCache.hosts === hosts) return machinesCache.list;
  const out: Machine[] = [];
  const by: Record<string, Machine> = {};
  for (const hst of hosts) {
    const key = hst.machine || "host:" + hst.name;
    let m = by[key];
    if (!m) {
      m = { key, local: key === "local", hosts: [], name: "", primary: null };
      by[key] = m;
      out.push(m);
    }
    m.hosts.push(hst);
  }
  if (!by.local) out.unshift({ key: "local", local: true, hosts: [], name: "", primary: null });
  for (const m of out) {
    m.primary = m.hosts[0] || null;
    m.name = m.local ? LOCAL_NAME : m.primary!.name;
  }
  out.sort((a, b) => (a.local === b.local ? 0 : a.local ? -1 : 1));
  machinesCache = { hosts, list: out };
  return out;
}
export function machineOf(route: Route): Machine | undefined {
  const ms = machines();
  if (route.local) return ms.find((m) => m.local);
  return ms.find((m) => m.hosts.some((x) => x.name === route.host));
}

const setHost = (v: Host) => set((s) => ({ hosts: s.hosts.map((x) => (x.name === v.name ? v : x)) }));

/* loading */

let toastFn: (msg: string, kind?: "ok" | "err") => void = () => {};
export const onToast = (f: typeof toastFn) => {
  toastFn = f;
};

export async function loadHosts(refresh = false) {
  try {
    const hosts = await api<Host[]>("/hosts" + (refresh ? "?refresh=1" : ""));
    set({ hosts });
  } catch (e) {
    toastFn(t("Não consegui listar os hosts: {error}", { error: (e as Error).message }), "err");
  }
  set({ hostsLoaded: true });
  scheduleConnectingPoll();
  const r = get().route;
  for (const hst of get().hosts) if (hst.ok && r.host === hst.name && (r.lab || r.ctr)) loadLabs(hst.name);
  syncEvents();
}

const labsInflight: Record<string, Promise<void> | undefined> = {};
export function loadLabs(host: string): Promise<void> {
  if (labsInflight[host]) return labsInflight[host]!;
  labsInflight[host] = (async () => {
    try {
      const l = await api<any[]>("/hosts/" + enc(host) + "/labs");
      set((s) => {
        const labsErr = { ...s.labsErr };
        delete labsErr[host];
        return { labs: { ...s.labs, [host]: l }, labsErr };
      });
    } catch (e) {
      set((s) => ({ labsErr: { ...s.labsErr, [host]: (e as Error).message } }));
    } finally {
      delete labsInflight[host];
    }
    syncEvents();
  })();
  return labsInflight[host]!;
}
const labsSoon: Record<string, number> = {};
export function scheduleLabs(host: string) {
  clearTimeout(labsSoon[host]);
  labsSoon[host] = window.setTimeout(() => loadLabs(host), 350);
}

export async function loadStats(host: string) {
  try {
    const st = await api<Record<string, { cpu: number; mem: number }>>("/hosts/" + enc(host) + "/stats");
    set((s) => ({ stats: { ...s.stats, [host]: st } }));
  } catch {
    /* the next round tries again */
  }
}

// While a host is still connecting (maybe waiting for a password), check
// again soon instead of waiting for the slow refresh.
let connectingTimer = 0;
export function scheduleConnectingPoll() {
  clearTimeout(connectingTimer);
  if (get().hosts.some((x) => x.connecting)) connectingTimer = window.setTimeout(() => loadHosts(), 3000);
}

export async function refreshHost(name: string) {
  try {
    const v = await api<Host>("/hosts/" + enc(name) + "/retry", { method: "POST" });
    setHost(v);
    scheduleConnectingPoll();
    if (v.ok) await loadLabs(name);
  } catch (e) {
    toastFn((e as Error).message, "err");
  }
}

export async function connectMachine(m: Machine) {
  await Promise.all(
    m.hosts
      .filter((x) => !x.ok)
      .map(async (x) => {
        try {
          setHost(await api<Host>("/hosts/" + enc(x.name)));
        } catch {
          /* shows as an error on the host */
        }
      }),
  );
  scheduleConnectingPoll();
  syncEvents();
}

export function markBusy(key: string, op: string | null) {
  set((s) => {
    const busy = { ...s.busy };
    if (op) busy[key] = op;
    else delete busy[key];
    return { busy };
  });
}

export async function loadLocal() {
  loadHidden();
  try {
    set({ local: await api("/local") });
  } catch (e) {
    set({ local: { os: "", engines: [], install: [], error: (e as Error).message } });
  }
}
export async function loadHidden() {
  try {
    set({ hidden: await api<string[]>("/hidden") });
  } catch {
    set({ hidden: [] });
  }
}

export async function loadGroups() {
  try {
    set({ groups: await api<Groups>("/grupos") });
  } catch {
    if (!get().groups) set({ groups: { folders: [], items: {} } });
  }
}

export async function loadClusters() {
  try {
    set({ clusters: await api<Cluster[]>("/clusters") });
  } catch {
    if (!get().clusters) set({ clusters: [] });
  }
  if (!get().kinds) {
    try {
      set({ kinds: await api<KindInfo[]>("/kube/tipos") });
    } catch {
      set({ kinds: [] });
    }
  }
}
export const clusterById = (id: string) => (get().clusters || []).find((c) => c.id === id);

/* engine events: one stream per host of the machine on screen */

const streams = new Map<string, EventSource>();
let changedFn: (host: string) => void = () => {};
export const onEngineEvent = (f: (host: string) => void) => {
  changedFn = f;
};

export function syncEvents() {
  const want = new Set<string>();
  const r = get().route;
  const m = (r.local || r.host) && !r.tpl ? machineOf(r) : undefined;
  if (m) for (const hst of m.hosts) if (hst.ok) want.add(hst.name);
  for (const [name, es] of streams) {
    if (!want.has(name)) {
      es.close();
      streams.delete(name);
    }
  }
  for (const name of want) {
    if (streams.has(name)) continue;
    const es = new EventSource(apiURL("/hosts/" + enc(name) + "/events", true));
    es.onmessage = () => {
      if (get().labs[name]) scheduleLabs(name);
      changedFn(name);
    };
    streams.set(name, es);
  }
}
