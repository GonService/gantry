// Web applications: published ports that answer HTTP, opened with a click.
// Also the alerts of Prometheus (only when a Prometheus with rules runs).
import { useEffect } from "react";
import { create } from "zustand";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { copyText } from "@/lib/format";
import { t, tn } from "@/lib/i18n";

export type WebApp = { port: number; scheme: string; path?: string; title?: string; lab?: string; owner: string };
type WebState = { at: number; apps: Record<string, WebApp>; ip: string; loading?: boolean };

export const useWeb = create<Record<string, WebState>>(() => ({}));

// ensureWeb loads (at most every 30 s) the apps of a host and the IP of
// the machine.
export function ensureWeb(host: string) {
  const w = useWeb.getState()[host] ?? { at: 0, apps: {}, ip: "" };
  if (w.loading || Date.now() - w.at < 30000) return;
  useWeb.setState({ [host]: { ...w, loading: true } });
  Promise.all([
    api<{ apps: Record<string, WebApp> }>("/hosts/" + enc(host) + "/web").catch(() => null),
    w.ip ? null : api<{ info: { ip?: string } }>("/hosts/" + enc(host) + "/system").catch(() => null),
  ]).then(([r, sys]) => {
    const cur = useWeb.getState()[host] ?? w;
    const apps = r ? r.apps || {} : cur.apps;
    const ip = sys?.info?.ip || cur.ip;
    const same = JSON.stringify(apps) + ip === JSON.stringify(cur.apps) + cur.ip;
    useWeb.setState({ [host]: same ? { ...cur, loading: false, at: Date.now() } : { apps, ip, at: Date.now() } });
  });
}

export const webApp = (host: string, port: number | string) => useWeb.getState()[host]?.apps?.[String(port)] ?? null;

// webURL: the address of the app for other devices (by the machine's IP).
export function webURL(host: string, app: WebApp) {
  const where = useWeb.getState()[host]?.ip || "localhost";
  return app.scheme + "://" + where + ":" + app.port + (app.path || "") + "/";
}

export async function openWeb(host: string, port: number | string, path?: string) {
  try {
    await api("/hosts/" + enc(host) + "/web/open", { method: "POST", body: { port: Number(port), path: path || "" } });
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

// PortLabel becomes an open button when the port is a web app.
export function PortLabel({ host, port, text }: { host: string; port: number | string; text: string }) {
  const app = useWeb((s) => s[host]?.apps?.[String(port)]);
  if (!app) return <span>{text}</span>;
  return (
    <button
      type="button"
      className="port-link"
      title={app.title ? t("Abrir {url} · {title}", { url: webURL(host, app), title: app.title }) : t("Abrir {url}", { url: webURL(host, app) })}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        openWeb(host, port, app.path);
      }}
    >
      {text}
      <span aria-hidden="true" className="port-go">
        {" "}
        ↗
      </span>
    </button>
  );
}

// AppsBar: the web apps of a project, at the top of its page.
export function AppsBar({ host, lab, loose, containers }: { host: string; lab: string; loose?: boolean; containers: { name: string; service?: string }[] }) {
  const w = useWeb((s) => s[host]);
  useEffect(() => ensureWeb(host));
  const apps = Object.values(w?.apps ?? {})
    .filter((a) => a.lab === lab || (loose && !a.lab && !a.owner.startsWith("vm:")))
    .sort((a, b) => a.port - b.port);
  if (!apps.length) return null;
  const nameOf = (a: WebApp) => {
    if (a.owner.startsWith("vm:")) return "VM " + a.owner.split("/").pop();
    const c = containers.find((x) => x.name === a.owner);
    return c?.service || a.owner;
  };
  return (
    <div className="apps-bar" role="list" aria-label={t("Aplicações web do projeto")}>
      <span className="apps-title">{t("Abrir")}</span>
      {apps.map((a) => {
        const url = webURL(host, a);
        return (
          <div className="app-chip" role="listitem" key={a.port}>
            <button type="button" className="btn small" title={a.title || undefined} onClick={() => openWeb(host, a.port, a.path)}>
              {nameOf(a)}
              <span className="muted mono"> :{a.port + (a.path || "")}</span>
            </button>
            <button type="button" className="btn small btn-ghost" title={t("Copiar {url} (para abrir em outro aparelho da rede)", { url })} onClick={async () => gantry.toast((await copyText(url)) ? t("Copiado: {url}", { url }) : url, "ok")}>
              {t("Copiar link")}
            </button>
          </div>
        );
      })}
    </div>
  );
}

/* alerts */

type Alert = { name: string; severity?: string; summary?: string; since: string; state: string };
type AlertState = { at: number; list: Alert[]; lab?: string; loading?: boolean };
export const useAlerts = create<Record<string, AlertState>>(() => ({}));
const useMute = create<{ v: number }>(() => ({ v: 0 }));

export function ensureAlerts(host: string) {
  const a = useAlerts.getState()[host] ?? { at: 0, list: [] };
  if (a.loading || Date.now() - a.at < 30000) return;
  useAlerts.setState({ [host]: { ...a, loading: true } });
  api<{ alerts: Alert[]; lab?: string }>("/hosts/" + enc(host) + "/alerts")
    .then((r) => {
      const list = (r.alerts || []).filter((x) => x.state === "firing");
      const cur = useAlerts.getState()[host] ?? a;
      useAlerts.setState({ [host]: JSON.stringify(list) === JSON.stringify(cur.list) && r.lab === cur.lab ? { ...cur, loading: false, at: Date.now() } : { list, lab: r.lab || "", at: Date.now() } });
    })
    .catch(() => useAlerts.setState({ [host]: { ...a, loading: false, at: Date.now() } }));
}

export const alertsMuted = (host: string) => gantry.store.get("alerts.mute." + host, false);
export function setAlertsMuted(hosts: string[], on: boolean) {
  for (const h of hosts) gantry.store.set("alerts.mute." + h, on);
  useMute.setState((s) => ({ v: s.v + 1 }));
}

const SEV: Record<string, string> = { critico: "crit", critical: "crit", aviso: "warn", warning: "warn" };

// AlertsBanner: the firing alerts; hosts are the ones with the Prometheus,
// lab limits them to the Prometheus of that project.
export function AlertsBanner({ hosts, lab }: { hosts: string[]; lab?: string }) {
  const all = useAlerts();
  useMute((s) => s.v);
  const items: (Alert & { host: string })[] = [];
  useEffect(() => {
    for (const host of hosts) if (!alertsMuted(host)) ensureAlerts(host);
  });
  for (const host of hosts) {
    if (alertsMuted(host)) continue;
    const a = all[host];
    if (!a || (lab && a.lab && a.lab !== lab)) continue;
    for (const x of a.list) items.push({ host, ...x });
  }
  if (!items.length) return null;
  const since = (ts: string) => {
    const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
    return s < 3600 ? Math.round(s / 60) + " min" : Math.round(s / 3600) + " h";
  };
  return (
    <div className="banner alerts" role="alert">
      <div className="grow">
        <b>{tn(items.length, "{n} alerta disparado", "{n} alertas disparados")}</b>
        <ul className="alert-list">
          {items.slice(0, 8).map((x, i) => (
            <li key={i}>
              <span className={"badge " + (SEV[x.severity ?? ""] || "warn")}>{x.severity || t("alerta")}</span> <b>{x.name}</b>
              {x.summary ? " — " + x.summary : ""}
              <span className="muted small">{t(" · há {ago}", { ago: since(x.since) })}</span>
            </li>
          ))}
        </ul>
      </div>
      <button
        className="btn small"
        onClick={async () => {
          try {
            await api("/hosts/" + enc(items[0].host) + "/obs/open", { method: "POST", body: { kind: "prometheus", path: "/alerts" } });
          } catch (e) {
            gantry.toast((e as Error).message, "err");
          }
        }}
      >
        {t("Ver no Prometheus")}
      </button>
      <button className="btn small btn-ghost" title={t("Não mostrar alertas desta máquina (volta em Configuração)")} onClick={() => setAlertsMuted([...new Set(items.map((i) => i.host))], true)}>
        {t("Silenciar")}
      </button>
    </div>
  );
}
