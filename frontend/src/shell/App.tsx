// The app: the sidebar, the page of the route, the dock, the dialogs and
// the notices; and the boot (machines, clusters, folders, jobs).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, CFG } from "@/lib/api";
import {
  clusterById,
  followHash,
  hostByName,
  LOCAL_NAME,
  loadClusters,
  loadGroups,
  loadHosts,
  loadLabs,
  loadLocal,
  loadStats,
  machineOf,
  machines,
  onEngineEvent,
  refreshHost,
  useStore,
  type Host,
  type Route,
} from "@/lib/store";
import { DialogHost } from "@/components/dialog";
import { ContextMenuHost } from "@/components/contextmenu";
import { localStore } from "./toasts";
import { Toasts } from "./toasts";
import { Dock, reattachJobs } from "./dock";
import { Sidebar } from "./sidebar";
import { Prompts } from "./prompts";
import { checkAppVersion } from "./version";
import { engKey, loadCtrs, loadOverview, useMachine } from "@/views/machine/store";
import { MachinePage } from "@/views/machine/MachinePage";
import { LabPage } from "@/views/lab/LabPage";
import { ContainerPage } from "@/views/container/ContainerPage";
import { ContainerForm } from "@/views/container/form";
import { BuildPage } from "@/views/container/build";
import { KubePage } from "@/views/kube/KubePage";
import { SettingsPage } from "@/views/settings/SettingsPage";
import { Welcome } from "@/views/welcome/Welcome";
import { TemplatesPage } from "@/views/templates/TemplatesPage";
import { TemplateEdit } from "@/views/templates/TemplateEdit";
import { Deploy } from "@/views/templates/Deploy";
import { NewProject } from "@/views/newproject/NewProject";
import { NeedEngine } from "@/views/newproject/NeedEngine";
import { Loading } from "@/components/ui";
import { t } from "@/lib/i18n";
import { Palette } from "./palette";

// An engine event of a host: the page of its machine reads the containers
// and the overview again (once per burst).
const soon: Record<string, number> = {};
onEngineEvent((host) => {
  const m = machines().find((x) => x.hosts.some((y) => y.name === host));
  const r = useStore.getState().route;
  if (!m || r.lab || r.ctr) return;
  clearTimeout(soon[m.key]);
  soon[m.key] = window.setTimeout(() => {
    const d = useMachine.getState().ov[m.key]?.data;
    if (d) for (const e of d.engines) if (e.host === host && useMachine.getState().ctrs[engKey(e)]) loadCtrs(e);
    loadOverview(m);
  }, 500);
});

function crumb(r: Route): string {
  let c = "Gantry";
  if (r.config) c = t("Configuração");
  if (r.kube) {
    const kc = clusterById(r.kube);
    c = "Cluster  /  " + (kc ? kc.name : r.kube);
  }
  if (r.local) c = LOCAL_NAME + (r.newProj ? "  /  " + t("criar projeto") : "");
  if (r.tpl) c = t("Modelos") + (r.tplDeploy ? "  /  " + t("gerar projeto") : r.tplEdit === "novo" ? "  /  " + t("novo") : r.tplEdit ? "  /  " + t("editar") : "");
  if (r.host) {
    c = r.host;
    if (r.newCtr) c += "  /  " + t("novo container");
    if (r.newProj) c += "  /  " + t("criar projeto");
    if (r.build) c += "  /  " + t("construir imagem");
    if (r.ctr) c += "  /  " + (r.ctrMode === "editar" ? t("editar container") : r.ctrMode === "duplicar" ? t("duplicar container") : "container");
    if (r.lab) {
      const l = (useStore.getState().labs[r.host] || []).find((x) => x.id === r.lab);
      c += "  /  " + (l ? l.name : r.lab);
    }
  }
  return c;
}

// A page of a host (project, container, forms) loads its projects and
// stats while open.
function useHostPage(hst: Host | undefined) {
  useEffect(() => {
    if (!hst) return;
    if (!hst.ok && !hst.connecting) refreshHost(hst.name); // opening it connects
    if (hst.ok && !useStore.getState().labs[hst.name]) loadLabs(hst.name);
    if (!hst.ok) return;
    loadStats(hst.name);
    const iv = setInterval(() => {
      if (!document.hidden) loadStats(hst.name);
    }, 5000);
    return () => clearInterval(iv);
  }, [hst?.name, hst?.ok]); // eslint-disable-line react-hooks/exhaustive-deps
}

function NotFound({ name, back }: { name?: string; back: string }) {
  return (
    <div className="empty">
      <h1>{t("Host não encontrado")}</h1>
      <p>{name || ""}</p>
      <a className="btn" href={back}>
        {t("Voltar")}
      </a>
    </div>
  );
}

function HostRoute({ r }: { r: Route }) {
  const hst = useStore((s) => s.hosts.find((x) => x.name === r.host));
  const visit = useStore((s) => s.visit);
  useHostPage(hst);
  if (!hst) return <NotFound name={r.host} back="#/" />;
  if (r.build) return <BuildPage key={"b" + visit} host={hst.name} />;
  if (r.newCtr) return <ContainerForm key={"n" + visit} host={hst.name} mode="novo" rt={r.ctrRt || ""} id="" />;
  if (r.ctrMode) return <ContainerForm key={"e" + visit} host={hst.name} mode={r.ctrMode as "editar" | "duplicar"} rt={r.ctrRt || ""} id={r.ctr!} />;
  if (r.ctr) return <ContainerPage host={hst.name} rt={r.ctrRt || ""} id={r.ctr} />;
  return <LabPage host={hst.name} lab={r.lab!} tab={r.tab || "containers"} />;
}

function Page() {
  const s = useStore();
  const r = s.route;
  if (!s.hostsLoaded) return <Loading>{t("Conectando aos hosts…")}</Loading>;
  if (r.kube) return <KubePage cluster={clusterById(r.kube) || null} loaded={!!s.clusters} kind={r.kkind || ""} kinds={s.kinds || []} />;
  if (r.welcome) {
    const lm = machines(s.hosts).find((x) => x.local);
    return <Welcome engines={s.local ? s.local.engines.map((e: { name: string; state: string }) => ({ name: e.name, state: e.state })) : null} localHost={lm && lm.primary ? lm.primary.name : ""} />;
  }
  if (r.config) {
    const ms = machines(s.hosts).map((m) => ({
      key: m.key,
      local: !!m.local,
      name: m.name,
      href: m.local ? "#/local" : "#/h/" + encodeURIComponent(m.primary!.name),
      url: m.primary ? m.primary.url : "",
      kind: m.primary ? m.primary.kind : "",
      source: m.primary ? m.primary.source : "",
      engines: m.hosts.length,
      primary: m.primary,
    }));
    return <SettingsPage tab={r.config} groups={s.groups} clusters={s.clusters || []} machines={ms} firstHost={s.hosts.find((x) => x.ok) || null} />;
  }
  if (r.tpl) {
    if (r.tplDeploy)
      return (
        <Deploy
          key={"d" + s.visit}
          id={r.tplDeploy}
          host={r.q.host || ""}
          nome={r.q.nome || ""}
          senhas={r.q.senhas || ""}
          targets={s.hosts.filter((x) => x.files).map((x) => ({ name: x.name, ok: !!x.ok, idle: !!x.idle, runtime: x.runtime || "" }))}
        />
      );
    if (r.tplEdit) return <TemplateEdit key={"e" + s.visit} id={r.tplEdit} host={r.q.host || ""} />;
    return <TemplatesPage host={r.q.host || ""} />;
  }
  if (r.newProj) {
    const m = machineOf(r);
    // No engine running on this computer: install or start one first.
    if (m && m.local && !m.hosts.some((x) => x.ok)) return <NeedEngine href="#/local" name={m.name} />;
    if (m)
      return (
        <NewProject
          key={"np" + m.key + s.visit + (r.q.tipo || "") + (r.q.origem || "")}
          mkey={m.key}
          name={m.name}
          local={m.local}
          href={m.local ? "#/local" : "#/h/" + encodeURIComponent(m.primary!.name)}
          toolsHref={(m.local ? "#/local" : "#/h/" + encodeURIComponent(m.primary!.name)) + "/t/ferramentas"}
          primary={m.primary ? m.primary.name : ""}
          tcp={!m.primary || m.primary.kind === "tcp"}
          hosts={m.hosts.map((x) => ({ name: x.name, ok: !!x.ok, files: !!x.files }))}
          others={s.hosts.filter((x) => x.files && !m.hosts.some((y) => y.name === x.name)).map((x) => ({ name: x.name, ok: !!x.ok }))}
          start={{ kind: r.q.tipo, source: r.q.origem, engine: r.q.engine }}
        />
      );
  }
  if (r.local || (r.host && !r.lab && !r.ctr && !r.newCtr && !r.ctrMode && !r.build)) {
    const m = machineOf(r);
    if (!m) return <NotFound name={r.host} back="#/local" />;
    return <MachinePage mkey={m.key} mtab={r.mtab || ""} meng={r.meng || ""} mseg={r.mseg || ""} />;
  }
  if (!r.host) return <Redirect to="#/local" />;
  return <HostRoute r={r} />;
}

function Redirect({ to }: { to: string }) {
  useEffect(() => {
    location.replace(to);
  }, [to]);
  return null;
}

function LoginNeeded() {
  const [need, setNeed] = useState(false);
  useEffect(() => {
    const f = () => {
      try {
        localStorage.removeItem("gantry.token");
      } catch {
        /* nothing */
      }
      setNeed(true);
    };
    window.addEventListener("gantry:login-needed", f);
    return () => window.removeEventListener("gantry:login-needed", f);
  }, []);
  if (!need) return null;
  return (
    <div id="login-needed" className="login-needed">
      <div className="panel panel-pad">
        <h1>{t("Abra pelo link do terminal")}</h1>
        <p className="muted">{t("Esta janela do navegador não tem a chave do Gantry (ou o Gantry foi aberto de novo). Use o link \"abra:\" (ou \"link para outro navegador:\") que aparece no terminal onde o Gantry está rodando.")}</p>
      </div>
    </div>
  );
}

// maybeWelcome opens the welcome page once, when nothing was set up yet.
function maybeWelcome() {
  const s = useStore.getState();
  if (!s.hostsLoaded || !s.clusters) return;
  if (localStore.get("boas-vindas", false)) return;
  const h0 = location.hash.replace(/^#\/?/, "");
  if (h0 && h0 !== "local") return;
  const mine = s.hosts.some((x) => x.source === "config") || s.clusters.length > 0;
  if (mine) {
    localStore.set("boas-vindas", true);
    return;
  }
  location.hash = "#/bem-vindo";
}

function hideSplash(t0: number) {
  const el = document.getElementById("splash");
  if (!el || el.classList.contains("done")) return;
  setTimeout(
    () => {
      el.classList.add("done");
      setTimeout(() => el.remove(), 400);
    },
    Math.max(0, 1000 - (Date.now() - t0)),
  );
}

function boot() {
  // Splash: at least 1 s, until the machines are listed (at most 8 s).
  const t0 = Date.now();
  setTimeout(() => hideSplash(t0), 8000);
  loadGroups();
  const firstHosts = loadHosts();
  Promise.all([loadClusters(), firstHosts]).then(maybeWelcome);
  firstHosts
    .then(() =>
      reattachJobs(async () => {
        for (const x of useStore.getState().hosts) if (x.files) await loadLabs(x.name).catch(() => {});
      }),
    )
    .finally(() => hideSplash(t0));
  loadLocal();
  checkAppVersion(false);
  setInterval(() => checkAppVersion(false), 6 * 3600e3);
  setInterval(async () => {
    if (document.hidden) return;
    try {
      const list = await api<Host[]>("/hosts");
      const s = useStore.getState();
      const sig = (l: Host[]) => JSON.stringify(l.map((x) => [x.name, x.ok]));
      if (sig(list) !== sig(s.hosts)) useStore.setState({ hosts: list });
    } catch {
      /* tries again later */
    }
  }, 30000);
  window.addEventListener("hashchange", followHash);
  document.addEventListener("visibilitychange", () => {
    const r = useStore.getState().route;
    if (!document.hidden && r.host && hostByName(r.host)) loadLabs(r.host);
  });
}

// NetworkWarning: the browser version listening on the network without
// HTTPS says so on every page (anyone on the network can read the key).
function NetworkWarning() {
  const [a, setA] = useState<{ exposed: boolean; tls: boolean; link: string } | null>(null);
  useEffect(() => {
    if (CFG.mode === "desktop") return;
    api<{ exposed: boolean; tls: boolean; link: string }>("/acesso")
      .then(setA)
      .catch(() => {});
  }, []);
  if (!a || !a.exposed || a.tls) return null;
  return (
    <div className="net-warn" role="alert">
      <b>{t("Aberto na rede sem criptografia")}</b>{" "}
      {t("O Gantry está em {link} sem HTTPS: quem estiver na mesma rede pode ler a chave de acesso e controlar as suas máquinas. Reinicie com -tls (ou -cert e -key), ou use um túnel SSH.", { link: a.link })}
    </div>
  );
}

export function App() {
  const route = useStore((s) => s.route);
  const routeKey = useStore((s) => s.routeKey);
  const labs = useStore((s) => s.labs);
  const clusters = useStore((s) => s.clusters);
  const view = useRef<HTMLElement>(null);
  const booted = useRef(false);
  useEffect(() => {
    if (booted.current) return;
    booted.current = true;
    boot();
  }, []);
  useLayoutEffect(() => {
    view.current?.scrollTo(0, 0);
  }, [routeKey]);
  const title = crumb(route);
  useEffect(() => {
    document.title = route.host || route.local || route.tpl ? title.replace(/\s+\/\s+/g, " · ") + " · Gantry" : "Gantry";
  }, [title, labs, clusters]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <>
      <div className="layout">
        <Sidebar />
        <div className="content">
          <NetworkWarning />
          <header className="topbar">
            <span className="crumb" id="crumb">
              {title}
            </span>
            <span className="spacer" />
            <span className="env-tag" id="mode-tag">
              {CFG.mode === "desktop" ? "" : ""}
            </span>
          </header>
          <main className="container" id="view" tabIndex={-1} ref={view}>
            <Page />
          </main>
          <Dock />
        </div>
      </div>
      <Toasts />
      <Palette />
      <DialogHost />
      <ContextMenuHost />
      <Prompts />
      <LoginNeeded />
    </>
  );
}
