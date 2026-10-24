// What the machine page reads: the overview (engines, VMs, projects), the
// hardware, the containers of each engine, the shared services and the
// network between VMs. Kept per machine while the app is open.
import { create } from "zustand";
import { api, enc } from "@/lib/api";
import { rtQuery, type Machine } from "@/lib/app";

/* eslint-disable @typescript-eslint/no-explicit-any */
export type Engine = { label: string; runtime: string; host: string; rt?: string; ok: boolean; error?: string; version?: string; containers: number; running: number; images: number };
export type VM = { name: string; project: string; host: string; state: string; accel?: string; lan?: string; ports?: string[]; sshPort?: number; vnc?: boolean };
export type Project = { id: string; name: string; host: string; dir?: string; runtimes?: string[]; running: number; total: number; vms: number; vmsUp: number; used?: number };
export type Overview = { engines: Engine[]; vms: VM[]; projects: Project[] };
export type Ctr = { id: string; name: string; image: string; state: string; status: string; project?: string; service?: string; ports: { PublicPort?: number; PrivatePort: number; Type: string; IP?: string }[] };
export type SharedSvc = { id: string; kind: string; name: string; title: string; image: string; port?: number; running: boolean; lab?: string; host: string; runtime?: string; consumers?: Record<string, { db?: string; user?: string }> };
export type VMNet = { mode: string; prefix?: string; gateway?: string; problems?: string[]; hint?: string; net?: string; interface?: string; first?: number; last?: number; lanIface?: string; lanGateway?: string };

type Loadable<T> = { data?: T; err?: string; at: number; loading?: boolean };

type MS = {
  ov: Record<string, Loadable<Overview>>;
  sys: Record<string, { info: any; error?: string } | null>;
  ctrs: Record<string, { items?: Ctr[]; err?: string }>;
  shared: Record<string, Loadable<SharedSvc[]>>;
  net: Record<string, VMNet | "loading">;
};

export const useMachine = create<MS>(() => ({ ov: {}, sys: {}, ctrs: {}, shared: {}, net: {} }));
const patch = <K extends keyof MS>(k: K, key: string, v: MS[K][string]) => useMachine.setState((s) => ({ [k]: { ...s[k], [key]: v } }) as Partial<MS>);

export const engKey = (e: { host: string; rt?: string }) => e.host + (e.rt ? "@" + e.rt : "");

export async function loadOverview(m: Machine) {
  if (!m.primary) return;
  const cur = useMachine.getState().ov[m.key];
  if (cur?.loading) return;
  patch("ov", m.key, { ...(cur ?? { at: 0 }), loading: true });
  try {
    const d = await api<Overview>("/hosts/" + enc(m.primary.name) + "/overview");
    patch("ov", m.key, { data: d, at: Date.now() });
  } catch (e) {
    patch("ov", m.key, { err: (e as Error).message, data: cur?.data, at: Date.now() });
  }
}

export function forgetMachine(key: string) {
  useMachine.setState((s) => ({ ov: { ...s.ov, [key]: undefined as never }, sys: { ...s.sys, [key]: null } }));
}

export async function loadSys(m: Machine) {
  try {
    const url = m.local ? "/local/system" : "/hosts/" + enc(m.primary!.name) + "/system";
    patch("sys", m.key, await api(url));
  } catch (e) {
    patch("sys", m.key, { error: (e as Error).message, info: {} });
  }
}

export async function loadCtrs(e: Engine) {
  const k = engKey(e);
  try {
    patch("ctrs", k, { items: await api<Ctr[]>("/hosts/" + enc(e.host) + "/containers" + rtQuery(e.rt)) });
  } catch (err) {
    patch("ctrs", k, { err: (err as Error).message });
  }
}

export async function loadShared(m: Machine) {
  const cur = useMachine.getState().shared[m.key];
  if (cur?.loading) return;
  patch("shared", m.key, { ...(cur ?? { at: 0 }), loading: true });
  const items: SharedSvc[] = [];
  let err: string | undefined;
  for (const x of m.hosts.filter((y) => y.ok && y.files)) {
    try {
      for (const it of await api<SharedSvc[]>("/hosts/" + enc(x.name) + "/shared")) items.push({ ...it, host: x.name, runtime: x.runtime });
    } catch (e) {
      err = (e as Error).message;
    }
  }
  patch("shared", m.key, { data: items, err, at: Date.now() });
}

export async function loadNet(m: Machine, host: string) {
  patch("net", m.key, "loading");
  try {
    patch("net", m.key, await api<VMNet>("/hosts/" + enc(host) + "/vmnet"));
  } catch (e) {
    patch("net", m.key, { mode: "error", problems: [(e as Error).message] });
  }
}
export const forgetNet = (key: string) => useMachine.setState((s) => {
  const net = { ...s.net };
  delete net[key];
  return { net };
});
