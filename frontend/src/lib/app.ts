// Helpers shared by the views: links, formats and the state of machines.
import { useStore, type Host, type Machine } from "./store";
import { t, LOCALE } from "./i18n";

export type { Host, Machine };

// useApp re-renders the caller when the state of the app changes.
export const useApp = () => useStore();

export const enc = encodeURIComponent;
export const RT_NAME: Record<string, string> = { docker: "Docker", podman: "Podman", nerdctl: "containerd (nerdctl)", k3s: "k3s" };
export const hostHref = (host: string, tab?: string) => "#/h/" + enc(host) + (tab ? "/t/" + tab : "");
export const labHref = (host: string, lab: string, tab?: string) => hostHref(host) + "/l/" + enc(lab) + (tab && tab !== "containers" ? "/" + tab : "");
export const ctrHref = (host: string, rt: string | undefined, id: string, mode?: string) => "#/h/" + enc(host) + "/c/" + enc(rt || "-") + "/" + enc(id) + (mode ? "/" + mode : "");
export const newCtrHref = (host: string, rt?: string) => "#/h/" + enc(host) + "/criar" + (rt ? "/" + enc(rt) : "");
export const rtQuery = (rt?: string) => (rt ? "?rt=" + enc(rt) : "");
export function machHref(m: Machine, tab?: string, eng?: string, seg?: string) {
  const base = m.local ? "#/local" : hostHref(m.primary!.name);
  if (!tab) return base;
  return base + "/t/" + tab + (eng ? "/" + enc(eng) + (seg ? "/" + seg : "") : "");
}

export const fmtPct = (n: number | null | undefined) => (n == null ? "—" : (n < 10 ? n.toFixed(1) : n.toFixed(0)) + "%");
export function fmtAgo(sec: number | null | undefined): string {
  if (!sec) return "—";
  const d = Date.now() / 1000 - sec;
  if (d < 3600) return Math.max(1, Math.round(d / 60)) + " min";
  if (d < 86400) return Math.round(d / 3600) + " h";
  if (d < 86400 * 60) return t("{n} dias", { n: Math.round(d / 86400) });
  return new Date(sec * 1000).toLocaleDateString(LOCALE);
}
export function fmtUptime(s: number | null | undefined): string {
  if (!s) return "—";
  const d = Math.floor(s / 86400),
    hr = Math.floor((s % 86400) / 3600),
    mi = Math.floor((s % 3600) / 60);
  return d ? d + " d " + hr + " h" : hr ? hr + " h " + mi + " min" : mi + " min";
}

// machineState: [dot, text] of a machine in the sidebar and its page.
export function machineState(m: Machine, local: any): [string, string] {
  if (m.local) {
    const st = local;
    if (!st) return ["", t("verificando…")];
    const running = st.engines.filter((e: any) => e.state === "running");
    return running.length ? ["on", running.map((e: any) => e.name).join(", ")] : st.engines.length ? ["warn", t("engine parado")] : ["", t("sem engine")];
  }
  const hs = m.hosts;
  if (hs.some((x) => x.connecting)) return ["warn", t("conectando…")];
  if (hs.some((x) => x.ok)) return ["on", t("conectado")];
  if (hs.every((x) => x.idle)) return ["idle", t("não conectado")];
  return ["err", t("sem conexão")];
}
