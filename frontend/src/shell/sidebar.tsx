// The sidebar: the machines and clusters in folders (with a filter per
// folder), the search, and the theme, version and settings at the bottom.
import { useEffect, useRef, useState } from "react";
import { CFG } from "@/lib/api";
import { machHref, machineState } from "@/lib/app";
import { connectMachine, loadClusters, loadHosts, loadLocal, machines, useStore, type Groups, type Machine } from "@/lib/store";
import { copyItem, openMenu } from "@/components/contextmenu";
import { disconnectCluster } from "@/views/kube/actions";
import { clusterState, loadView, useKube } from "@/views/kube/store";
import type { Cluster } from "@/views/kube/types";
import { localStore } from "./toasts";
import { gantry } from "@/lib/gantry";
import { appVersionDialog, checkAppVersion, useVersion } from "./version";
import { t } from "@/lib/i18n";
import { openPalette } from "./palette";

const kubeHref = (id: string) => "#/k/" + encodeURIComponent(id);
const folderById = (g: Groups, id: string) => g.folders.find((f) => f.id === id);
function folderPath(g: Groups, id: string) {
  const out: string[] = [];
  for (let f = folderById(g, id), n = 0; f && n < 50; f = folderById(g, f.parent ?? ""), n++) out.unshift(f.name);
  return out.join(" › ");
}
const itemFolder = (g: Groups, key: string) => {
  const id = g.items[key];
  return id && folderById(g, id) ? id : "";
};
function isUnder(g: Groups, id: string, ancestor: string) {
  for (let f = folderById(g, id), n = 0; f && n < 50; f = folderById(g, f.parent ?? ""), n++) if (f.parent === ancestor) return true;
  return false;
}
const matches = (m: Machine, q: string) => m.name.toLowerCase().includes(q) || m.hosts.some((x) => x.name.toLowerCase().includes(q) || (x.url || "").toLowerCase().includes(q));
const cmatch = (c: Cluster, q: string) => c.name.toLowerCase().includes(q) || (c.context || "").toLowerCase().includes(q);

// Folders bigger than this open showing the ones that need attention and
// the latest used, with "ver todas" for the rest.
const FOLDER_PEEK = 10;

// A ship's wheel: what a cluster looks like in the sidebar.
function KubeIcon() {
  const spokes = [0, 45, 90, 135, 180, 225, 270, 315].map((a) => {
    const r = (a * Math.PI) / 180,
      c = Math.cos(r),
      s = Math.sin(r);
    return <path key={a} d={"M" + (12 + 2.6 * c).toFixed(2) + " " + (12 + 2.6 * s).toFixed(2) + "L" + (12 + 9.6 * c).toFixed(2) + " " + (12 + 9.6 * s).toFixed(2)} />;
  });
  return (
    <span className="kube-ic">
      <svg width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <circle cx="12" cy="12" r="6.6" />
        <circle cx="12" cy="12" r="2.2" />
        {spokes}
      </svg>
    </span>
  );
}

function MachineRow({ m, cur, depth, sub, local }: { m: Machine; cur: Machine | null; depth: number; sub?: string | null; local: unknown }) {
  const [dot, state] = machineState(m, local);
  const engines = m.local ? "" : m.hosts.length > 1 ? m.hosts.length + " engines · " : "";
  // Remote machines with a connection open can be disconnected from here.
  const live = !m.local && m.hosts.some((x) => x.ok || x.connecting);
  return (
    <div className={"tree-row machine-row" + (cur === m ? " sel" : "")} style={{ ["--depth" as string]: depth }} onContextMenu={(e) => machineMenu(e, m, live)}>
      <a className="machine-a" href={machHref(m)} title={m.local ? "" : m.primary!.url}>
        <span className="local-ic" aria-hidden="true">
          {m.local ? "💻" : "🖧"}
        </span>
        <span className="tree-link">
          <span className={"dot " + dot} />
          <span className="tree-text">
            <span className="tree-name">{m.name}</span>
            <span className="tree-sub">{sub || engines + state}</span>
          </span>
        </span>
      </a>
      {live ? (
        <button
          className="icon-btn tree-act"
          type="button"
          title={t("Desconectar {name} (fecha a conexão e esquece a senha lembrada)", { name: m.name })}
          aria-label={t("Desconectar {name}", { name: m.name })}
          onClick={() => gantry.disconnectMachine(m)}
        >
          ⏏
        </button>
      ) : null}
    </div>
  );
}

// The right-click menu of a machine in the sidebar.
function machineMenu(e: React.MouseEvent, m: Machine, live: boolean) {
  const p = m.primary;
  openMenu(
    e,
    [
      { label: t("Abrir"), onClick: () => void (location.hash = machHref(m)) },
      m.local
        ? { label: t("Atualizar"), onClick: () => void Promise.all([loadLocal(), loadHosts(true)]) }
        : { label: live ? t("Atualizar") : t("Conectar"), onClick: () => void (live ? Promise.all(m.hosts.map((x) => gantry.refreshHost(x.name))) : connectMachine(m)) },
      !m.local && live ? { label: t("Desconectar"), hint: t("Fecha a conexão e esquece a senha lembrada"), onClick: () => void gantry.disconnectMachine(m) } : null,
      "-",
      m.local ? { label: t("Engines"), onClick: () => void (location.hash = machHref(m, "engines")) } : null,
      { label: t("Ferramentas"), onClick: () => void (location.hash = machHref(m, "ferramentas")) },
      p && !m.local ? copyItem(t("Copiar endereço"), p.url, () => gantry.toast(t("Copiado"), "ok")) : null,
      "-",
      p && !m.local && p.source === "config" ? { label: t("Editar conexão…"), onClick: () => gantry.openHostDialog(p) } : null,
      p && !m.local && p.source === "config" ? { label: t("Remover máquina…"), danger: true, onClick: () => gantry.removeHost(p.name) } : null,
    ],
    m.name,
  );
}

function ClusterRow({ c, cur, depth, sub }: { c: Cluster; cur?: string; depth: number; sub?: string | null }) {
  const open = useKube((s) => !!(s.data[c.id]?.ov || Object.keys(s.data[c.id]?.lists ?? {}).length));
  const [dot, state] = clusterState(c.id);
  const menu = (e: React.MouseEvent) =>
    openMenu(
      e,
      [
        { label: t("Abrir"), onClick: () => void (location.hash = kubeHref(c.id)) },
        { label: open ? t("Atualizar") : t("Conectar"), onClick: () => void loadView(c.id, "") },
        open ? { label: t("Desconectar"), onClick: () => void disconnectCluster(c) } : null,
        "-",
        copyItem(t("Copiar contexto"), c.context, () => gantry.toast(t("Copiado"), "ok")),
        { label: t("Clusters em Configuração"), onClick: () => void (location.hash = "#/config/clusters") },
      ],
      c.name,
    );
  return (
    <div className={"tree-row machine-row" + (cur === c.id ? " sel" : "")} style={{ ["--depth" as string]: depth }} onContextMenu={menu}>
      <a className="machine-a" href={kubeHref(c.id)} title={c.context}>
        <span className="local-ic" aria-hidden="true">
          <KubeIcon />
        </span>
        <span className="tree-link">
          <span className={"dot " + dot} />
          <span className="tree-text">
            <span className="tree-name">{c.name}</span>
            <span className="tree-sub">{sub || state}</span>
          </span>
        </span>
      </a>
      {open ? (
        <button className="icon-btn tree-act" type="button" title={t("Desconectar {name}", { name: c.name })} aria-label={t("Desconectar {name}", { name: c.name })} onClick={() => void disconnectCluster(c)}>
          ⏏
        </button>
      ) : null}
    </div>
  );
}

function Tree({ filter }: { filter: string }) {
  const s = useStore();
  useKube((k) => k.data); // the folders count clusters with problems
  const g = s.groups;
  const [closed, setClosed] = useState<Set<string>>(() => new Set(localStore.get<string[]>("pastas-fechadas", [])));
  const [ffilter, setFFilter] = useState<Record<string, string | undefined>>({});
  const [all, setAll] = useState<Record<string, boolean>>({});
  const focusRef = useRef<string>("");
  useEffect(() => {
    if (!focusRef.current) return;
    const el = document.getElementById("ff-" + focusRef.current) as HTMLInputElement | null;
    focusRef.current = "";
    el?.focus();
  });
  if (!s.hostsLoaded || !g)
    return (
      <div className="loading">
        <span className="spin" />
        {t("Procurando hosts…")}
      </div>
    );
  const r = s.route;
  const cur = r.tpl || r.config || r.kube ? null : (machines().find((m) => (r.local ? m.local : m.hosts.some((x) => x.name === r.host))) ?? null);
  const ms = machines(s.hosts);
  const cls = s.clusters || [];
  const toggle = (id: string) =>
    setClosed((c) => {
      const n = new Set(c);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      localStore.set("pastas-fechadas", [...n]);
      return n;
    });
  const stats = (id: string) => {
    let n = 0,
      err = 0,
      warn = 0;
    const visit = (fid: string) => {
      for (const m of ms) {
        if (itemFolder(g, m.key) !== fid) continue;
        n++;
        const [dot] = machineState(m, s.local);
        if (dot === "err") err++;
        else if (dot === "warn") warn++;
      }
      for (const c of cls) {
        if (itemFolder(g, "cluster:" + c.id) !== fid) continue;
        n++;
        const [dot] = clusterState(c.id);
        if (dot === "err") err++;
        else if (dot === "warn") warn++;
      }
      for (const f of g.folders) if (f.parent === fid) visit(f.id);
    };
    visit(id);
    return { n, err, warn };
  };
  const q = filter.toLowerCase();
  if (q) {
    // Global search: every match, with the folder it is in.
    const found = ms.filter((m) => matches(m, q));
    const kfound = cls.filter((c) => cmatch(c, q));
    return (
      <>
        {!found.length && !kfound.length ? <div className="tree-empty">{t("Nada com \"{filter}\".", { filter })}</div> : null}
        {found.map((m) => {
          const fid = itemFolder(g, m.key);
          return <MachineRow key={m.key} m={m} cur={cur} depth={0} sub={fid ? folderPath(g, fid) : null} local={s.local} />;
        })}
        {kfound.map((c) => {
          const fid = itemFolder(g, "cluster:" + c.id);
          return <ClusterRow key={c.id} c={c} cur={r.kube} depth={0} sub={fid ? folderPath(g, fid) : null} />;
        })}
      </>
    );
  }
  const out: React.ReactNode[] = [];
  const renderFolder = (f: { id: string; name: string }, depth: number) => {
    const open = !closed.has(f.id);
    const st = stats(f.id);
    const filt = ffilter[f.id];
    const searching = filt !== undefined;
    out.push(
      <div className="tree-row folder-row" style={{ ["--depth" as string]: depth }} key={"f" + f.id}>
        <button className="folder-toggle" type="button" aria-expanded={open} onClick={() => toggle(f.id)}>
          <span className="caret" aria-hidden="true">
            {open ? "▾" : "▸"}
          </span>
          <span className="folder-name">{f.name}</span>
          {st.err ? (
            <span className="folder-alert err" title={t("{n} com problema", { n: st.err })}>
              {st.err}
            </span>
          ) : st.warn ? (
            <span className="folder-alert warn" title={t("{n} com aviso", { n: st.warn })}>
              {st.warn}
            </span>
          ) : null}
          <span className="folder-count">{st.n}</span>
        </button>
        <button
          className="icon-btn folder-search"
          type="button"
          title={t("Buscar em {name}", { name: f.name })}
          aria-label={t("Buscar em {name}", { name: f.name })}
          onClick={(e) => {
            e.stopPropagation();
            if (searching)
              setFFilter((x) => {
                const n = { ...x };
                delete n[f.id];
                return n;
              });
            else {
              setFFilter((x) => ({ ...x, [f.id]: "" }));
              if (!open) toggle(f.id);
              focusRef.current = f.id;
            }
          }}
        >
          🔍
        </button>
      </div>,
    );
    if (!open) return;
    if (searching) {
      out.push(
        <input
          key={"ff" + f.id}
          id={"ff-" + f.id}
          className="folder-filter"
          type="search"
          placeholder={t("Filtrar em {name}…", { name: f.name })}
          value={filt}
          spellCheck={false}
          style={{ ["--depth" as string]: depth + 1 }}
          onChange={(e) => setFFilter((x) => ({ ...x, [f.id]: e.target.value }))}
          onKeyDown={(e) => {
            if (e.key === "Escape")
              setFFilter((x) => {
                const n = { ...x };
                delete n[f.id];
                return n;
              });
            if (e.key === "Enter") {
              const a = (e.currentTarget.parentNode as HTMLElement).querySelector(".machine-row a, a.machine-row");
              if (a) location.hash = a.getAttribute("href")!;
            }
          }}
        />,
      );
      const fq = filt.trim().toLowerCase();
      const inside = (fid: string) => !!fid && (fid === f.id || isUnder(g, fid, f.id));
      const found = ms.filter((m) => inside(itemFolder(g, m.key)) && (!fq || matches(m, fq)));
      const kfound = cls.filter((c) => inside(itemFolder(g, "cluster:" + c.id)) && (!fq || cmatch(c, fq)));
      if (!found.length && !kfound.length)
        out.push(
          <div key={"fe" + f.id} className="tree-empty" style={{ ["--depth" as string]: depth + 1 }}>
            {t("Nada encontrado.")}
          </div>,
        );
      for (const m of found) {
        const fid = itemFolder(g, m.key);
        out.push(<MachineRow key={"fm" + f.id + m.key} m={m} cur={cur} depth={depth + 1} sub={fid !== f.id ? folderPath(g, fid).split(" › ").slice(-1)[0] + " · " + machineState(m, s.local)[1] : null} local={s.local} />);
      }
      for (const c of kfound) out.push(<ClusterRow key={"fc" + f.id + c.id} c={c} cur={r.kube} depth={depth + 1} />);
      return;
    }
    for (const sub of g.folders.filter((x) => x.parent === f.id)) renderFolder(sub, depth + 1);
    const items = ms.filter((m) => itemFolder(g, m.key) === f.id);
    let shown = items;
    if (items.length > FOLDER_PEEK + 2 && !all[f.id]) {
      const rank = (m: Machine) => {
        const [d] = machineState(m, s.local);
        return m === cur ? 0 : d === "err" ? 1 : d === "warn" ? 2 : 3;
      };
      shown = [...items].sort((a, b) => rank(a) - rank(b)).slice(0, FOLDER_PEEK);
    }
    for (const m of shown) out.push(<MachineRow key={"m" + m.key} m={m} cur={cur} depth={depth + 1} local={s.local} />);
    for (const c of cls.filter((x) => itemFolder(g, "cluster:" + x.id) === f.id)) out.push(<ClusterRow key={"c" + c.id} c={c} cur={r.kube} depth={depth + 1} />);
    if (shown.length < items.length)
      out.push(
        <button key={"more" + f.id} className="tree-more" type="button" style={{ ["--depth" as string]: depth + 1 }} onClick={() => setAll((x) => ({ ...x, [f.id]: true }))}>
          {t("mais {n} · ver todas", { n: items.length - shown.length })}
        </button>,
      );
    else if (all[f.id] && items.length > FOLDER_PEEK + 2)
      out.push(
        <button key={"less" + f.id} className="tree-more" type="button" style={{ ["--depth" as string]: depth + 1 }} onClick={() => setAll((x) => ({ ...x, [f.id]: false }))}>
          {t("mostrar menos")}
        </button>,
      );
  };
  for (const f of g.folders.filter((x) => !x.parent)) renderFolder(f, 0);
  for (const m of ms.filter((x) => !itemFolder(g, x.key))) out.push(<MachineRow key={"m" + m.key} m={m} cur={cur} depth={0} local={s.local} />);
  for (const c of cls.filter((x) => !itemFolder(g, "cluster:" + x.id))) out.push(<ClusterRow key={"c" + c.id} c={c} cur={r.kube} depth={0} />);
  if (ms.length <= 1 && !s.hosts.some((x) => x.source === "config"))
    out.push(
      <a key="hint" className="tree-hint" href="#/config/maquinas">
        {t("＋ Adicionar um servidor (ou use a engrenagem)")}
      </a>,
    );
  return <>{out}</>;
}

function ThemeToggle() {
  const effective = () => document.documentElement.getAttribute("data-theme") || (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  const [theme, setTheme] = useState(effective);
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const f = () => setTheme(effective());
    mq.addEventListener("change", f);
    return () => mq.removeEventListener("change", f);
  }, []);
  return (
    <button
      className="theme-toggle"
      id="themeToggle"
      type="button"
      title={t("Alternar tema claro/escuro")}
      aria-label={t("Alternar tema")}
      onClick={() => {
        const next = effective() === "dark" ? "light" : "dark";
        document.documentElement.setAttribute("data-theme", next);
        localStore.set("theme", next);
        setTheme(next);
      }}
    >
      {theme === "dark" ? "☀️" : "🌙"}
    </button>
  );
}

export function Sidebar() {
  const [filter, setFilter] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const route = useStore((s) => s.route);
  const v = useVersion((s) => s.v);
  return (
    <aside className="sidebar" aria-label={t("Máquinas")}>
      <a className="sidebar-brand" href="#/">
        <img className="logo" src="logo.svg" alt="" />
        <span>Gantry</span>
      </a>
      <label className="side-search">
        <span className="sr">{t("Buscar máquina")}</span>
        <span className="search-icon" aria-hidden="true">
          🔍
        </span>
        <input
          id="search"
          ref={input}
          type="search"
          placeholder={t("Buscar…")}
          autoComplete="off"
          spellCheck={false}
          value={filter}
          onChange={(e) => setFilter(e.target.value.trim() ? e.target.value : "")}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              const first = document.querySelector("#tree a");
              if (first) {
                location.hash = first.getAttribute("href")!;
                setFilter("");
                e.currentTarget.blur();
              }
            } else if (e.key === "Escape") {
              setFilter("");
              e.currentTarget.blur();
            }
          }}
        />
        <kbd
          id="search-kbd"
          title={t("Paleta de comandos: ir para qualquer lugar e fazer as ações comuns")}
          onClick={(e) => {
            e.preventDefault();
            openPalette();
          }}
        >
          {/Mac|iPhone|iPad/.test(navigator.platform) ? "⌘K" : "Ctrl K"}
        </kbd>
      </label>
      <nav className="nav">
        <div className="nav-group-label nav-group-row">
          <span>{t("Máquinas")}</span>
          <button
            className="icon-btn"
            id="refresh-hosts"
            type="button"
            title={t("Atualizar a lista")}
            aria-label={t("Atualizar a lista")}
            onClick={() => {
              useStore.setState({ labs: {} });
              loadHosts(true);
              loadLocal();
              loadClusters();
            }}
          >
            ↻
          </button>
        </div>
        <div id="tree">
          <Tree filter={filter.trim()} />
        </div>
      </nav>
      <div className="sidebar-foot">
        <ThemeToggle />
        <span className="foot-hint" id="version" title={t("Verificar se há versão nova")} style={{ cursor: "pointer" }} onClick={() => checkAppVersion(true)}>
          {CFG.version ? "v" + CFG.version : ""}
        </span>
        <a className={"icon-btn foot-gear" + (route.config ? " sel" : "")} id="settings-link" href="#/config" title={t("Configuração: máquinas, pastas, credenciais")} aria-label={t("Configuração")}>
          ⚙
        </a>
        {v ? (
          <button id="update-pill" className="update-pill" type="button" title={t("Gantry {version} disponível", { version: v.latest })} onClick={() => appVersionDialog(v)}>
            ↑ {v.latest}
          </button>
        ) : null}
      </div>
    </aside>
  );
}

