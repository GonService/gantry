// The page of a machine: its hardware, its projects, the containers of each
// engine, its VMs, shared services, tools and configuration.
import { eventsDialog } from "@/views/container/tools";
import { useEffect, useRef } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, machHref, machineState, useApp, type Host, type Machine } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { fmtPct, fmtUptime } from "@/lib/app";
import { Loading, ErrorBox } from "@/components/ui";
import { LocalEngines } from "@/shell/localEngines";
import { useStore } from "@/lib/store";
import { t } from "@/lib/i18n";
import { AlertsBanner } from "@/views/shared/web";
import { ToolsTab, useMissingTools } from "@/views/tools/ToolsTab";
import { engKey, forgetMachine, loadCtrs, loadOverview, loadSys, useMachine } from "./store";
import { useTools } from "@/views/tools/store";
import { Empty, ConfigTab, ContainersTab, ProjectsTab, SharedTab, VMsTab } from "./tabs";

export type MachineProps = { mkey: string; mtab?: string; meng?: string; mseg?: string };

export function MachinePage({ mkey, mtab = "", meng = "", mseg = "" }: MachineProps) {
  const S = useApp();
  const m = (gantry.machines() as Machine[]).find((x) => x.key === mkey);
  if (!m) return null;
  return <Page m={m} S={S} tab={mtab || "projetos"} meng={meng} mseg={mseg} />;
}

function Meter({ label, used, total, text }: { label: string; used: number; total: number; text: string }) {
  const frac = total ? Math.min(1, used / total) : 0;
  const level = frac > 0.9 ? "crit" : frac > 0.75 ? "warn" : "";
  return (
    <div className="hw-meter">
      <div className="meter-top">
        <span className="meter-label">{label}</span>
        <span className="meter-val">{text}</span>
      </div>
      <div className="meter-bar">
        <span className={level} style={{ width: (frac * 100).toFixed(1) + "%" }} />
      </div>
    </div>
  );
}

function SysBlock({ m }: { m: Machine }) {
  const s = useMachine((x) => x.sys[m.key]);
  if (!s)
    return (
      <div className="panel panel-pad sys">
        <Loading>{t("Lendo o hardware…")}</Loading>
      </div>
    );
  const i = s.info || {};
  if (s.error && !i.cpus)
    return (
      <div className="panel panel-pad sys">
        <p className="muted" style={{ margin: 0 }}>
          {t("Não consegui ler o hardware: {error}", { error: s.error })}
        </p>
      </div>
    );
  const meters: React.ReactNode[] = [];
  if (i.cpus) meters.push(<Meter key="cpu" label="CPU" used={i.hasCpu ? i.cpuPct : 0} total={100} text={i.hasCpu ? t("{pct} de {n} núcleos", { pct: fmtPct(i.cpuPct), n: i.cpus }) : t("{n} núcleos", { n: i.cpus })} />);
  if (i.memTotal) meters.push(<Meter key="mem" label={t("Memória")} used={i.memUsed || 0} total={i.memTotal} text={i.memUsed ? t("{used} de {total}", { used: fmtBytes(i.memUsed), total: fmtBytes(i.memTotal) }) : fmtBytes(i.memTotal)} />);
  // The main disk next to CPU and memory; the others (another partition, a
  // second SSD, a USB drive) in a compact list below, one line each.
  const [disk0, ...otherDisks] = (i.disks || []) as { path: string; used: number; total: number }[];
  if (disk0) meters.push(<Meter key="d0" label={t("Disco {path}", { path: disk0.path })} used={disk0.used} total={disk0.total} text={t("{used} de {total}", { used: fmtBytes(disk0.used), total: fmtBytes(disk0.total) })} />);
  if (i.swapTotal) meters.push(<Meter key="swap" label="Swap" used={i.swapUsed} total={i.swapTotal} text={t("{used} de {total}", { used: fmtBytes(i.swapUsed), total: fmtBytes(i.swapTotal) })} />);
  return (
    <div className="panel panel-pad sys" id="sys-box">
      <div className="sys-facts">
        {[i.hostname, i.os, i.kernel ? "kernel " + i.kernel : null, i.arch, i.cpuModel].filter(Boolean).map((f: string) => (
          <span key={f}>{f}</span>
        ))}
        {i.ip ? (
          <span className="mono" title={t("Endereço na rede local: outros aparelhos acessam as portas publicadas por aqui")}>
            IP {i.ip}
          </span>
        ) : null}
        {i.uptime ? <span>{t("ligado há {uptime}", { uptime: fmtUptime(i.uptime) })}</span> : null}
        {i.load ? <span title={t("carga média em 1, 5 e 15 min")}>{t("carga {load}", { load: i.load })}</span> : null}
      </div>
      <div className="meters">{meters}</div>
      {otherDisks.length ? (
        <div className="disk-list" aria-label={t("Outros discos")}>
          <span className="disk-list-title">{t("Outros discos")}</span>
          {otherDisks.map((d) => {
            const frac = d.total ? Math.min(1, d.used / d.total) : 0;
            const level = frac > 0.9 ? "crit" : frac > 0.75 ? "warn" : "";
            return (
              <div className="disk-row" key={d.path}>
                <span className="mono disk-path" title={d.path}>
                  {d.path}
                </span>
                <div className="meter-bar thin">
                  <span className={level} style={{ width: (frac * 100).toFixed(1) + "%" }} />
                </div>
                <span className="meter-val">{t("{used} de {total}", { used: fmtBytes(d.used), total: fmtBytes(d.total) })}</span>
              </div>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}

// usePolling loads what the page needs and keeps it fresh.
function usePolling(m: Machine, tab: string, meng: string) {
  const ref = useRef({ tab, meng, m });
  ref.current = { tab, meng, m };
  useEffect(() => {
    const st = useMachine.getState();
    const fresh = (x: { at?: number } | undefined, ms: number) => !!x?.at && Date.now() - x.at < ms;
    if (!m.local && m.hosts.some((x) => !x.ok && !x.connecting)) {
      gantry.connectMachine(m).then(() => {
        const cur = (gantry.machines() as Machine[]).find((x) => x.key === m.key) ?? m;
        forgetMachine(m.key);
        loadOverview(cur);
        loadSys(cur);
      });
    } else {
      if (!fresh(st.ov[m.key], 3000)) loadOverview(m);
      loadSys(m);
    }
    if (m.local && !gantry.state().local) gantry.loadLocal();
    gantry.syncEvents();
    const d0 = st.ov[m.key]?.data;
    if (d0) for (const e of d0.engines) if (e.ok && !e.rt) gantry.loadStats(e.host);
    let n = 0;
    const timer = setInterval(() => {
      if (document.hidden) return;
      n++;
      const { m: cur, tab: tb, meng: me } = ref.current;
      loadSys(cur);
      const d = useMachine.getState().ov[cur.key]?.data;
      if (tb === "containers" && d) {
        const e = d.engines.find((x) => engKey(x) === me) || d.engines.find((x) => x.ok);
        if (e && e.ok) {
          loadCtrs(e);
          if (!e.rt) gantry.loadStats(e.host);
        }
      }
      if (tb === "vms" && d) for (const host of new Set(d.vms.filter((v) => v.state === "running").map((v) => v.host))) gantry.loadStats(host);
      if (n % 4 === 0) loadOverview(cur);
    }, 5000);
    return () => clearInterval(timer);
  }, [m.key]); // eslint-disable-line react-hooks/exhaustive-deps
}

function Page({ m, S, tab, meng, mseg }: { m: Machine; S: ReturnType<typeof useApp>; tab: string; meng: string; mseg: string }) {
  usePolling(m, tab, meng);
  const ov = useMachine((x) => x.ov[m.key]);
  const shared = useMachine((x) => x.shared[m.key]);
  const missing = useMissingTools(m.key);
  const canTools = m.local || (!!m.primary && m.primary.kind !== "tcp");
  useEffect(() => {
    if (canTools && missing == null) useTools.getState().load(m.key, m.local ? "/local/ferramentas" : "/hosts/" + enc(m.primary!.name) + "/ferramentas");
  }, [m.key, canTools]); // eslint-disable-line react-hooks/exhaustive-deps
  const [dot, stateText] = machineState(m, S.local);
  const p = m.primary;
  const d = ov?.data;
  const remote = !m.local;
  const kindText = !p ? "" : p.kind === "ssh" ? "via SSH" : p.kind === "tcp" ? "via TCP" : p.kind === "wsl" ? t("WSL2, pelo agente (TLS local)") : "";

  const refresh = async (btn: HTMLButtonElement) => {
    btn.disabled = true;
    forgetMachine(m.key);
    if (m.local) gantry.loadLocal();
    for (const x of m.hosts) await gantry.refreshHost(x.name);
    const cur = (gantry.machines() as Machine[]).find((x) => x.key === m.key) ?? m;
    loadOverview(cur);
    loadSys(cur);
    btn.disabled = false;
  };

  const head = (
    <div className="page-head">
      <div className="page-title">
        <nav className="crumbs" aria-label={t("Caminho")}>
          <span>{remote ? "Hosts" : "Local"}</span>
        </nav>
        <h1>
          {m.name}
          {remote ? (
            <span className={"badge " + (dot === "on" ? "ok" : dot === "warn" ? "warn" : dot === "err" ? "err" : "")}>
              {dot === "warn" ? <span className="spin" /> : <span className={"dot " + dot} />}
              {stateText}
            </span>
          ) : null}
        </h1>
        <div className="sub">{remote ? [p?.url, kindText].filter(Boolean).join(" · ") : t("Containers e VMs deste computador")}</div>
      </div>
      <div className="actions">
        <button className="btn" onClick={(e) => refresh(e.currentTarget)}>
          {t("Atualizar")}
        </button>
        {p && p.ok ? (
          <button className="btn" title={t("O que aconteceu com containers, imagens, volumes e redes")} onClick={() => eventsDialog(p.name)}>
            {t("Eventos")}
          </button>
        ) : null}
        {remote && p ? (
          <button
            className="btn"
            title={
              p.kind === "wsl"
                ? t("Solta a distribuição do WSL; ela só é usada de novo quando for aberta")
                : t("Fecha a conexão SSH e esquece a senha lembrada; o host só conecta de novo quando for aberto")
            }
            onClick={() => gantry.disconnectMachine(m)}
          >
            {t("Desconectar")}
          </button>
        ) : null}
        {remote && p?.source === "config" ? (
          <button className="btn" onClick={() => gantry.openHostDialog(p)}>
            {t("Editar")}
          </button>
        ) : null}
        {remote && p?.source === "config" ? (
          <button className="btn danger" onClick={() => gantry.removeHost(p.name)}>
            {t("Remover host")}
          </button>
        ) : null}
      </div>
    </div>
  );

  const banners: React.ReactNode[] = [<AlertsBanner key="al" hosts={m.hosts.filter((x) => x.ok).map((x) => x.name)} />];
  if (remote && p?.kind === "ssh") {
    const agent = m.hosts.map((x) => x.agent).find(Boolean);
    if (agent && agent.state === "running") {
      banners.push(
        <div key="ag" className="banner info">
          <span className="grow">
            {t("{conn}: métricas com histórico{vms} e arquivos observados na hora.", {
              conn: agent.via === "tls" ? t("Conectado direto por TLS ao agente do Gantry {version} (o SSH fica para comandos)", { version: agent.version || "" }) : t("Conectado via SSH, com o agente do Gantry {version}", { version: agent.version || "" }),
              vms: agent.vms ? t(" (containers e VMs)") : "",
            })}
          </span>
        </div>,
      );
    } else if (p.ok) {
      banners.push(
        <div key="ag" className="banner info">
          <span className="grow">{t("Host rodando sobre SSH. Dá para instalar o agente do Gantry para ter histórico de métricas e ver mudanças nos arquivos na hora.")}</span>
          <a className="btn small" href={machHref(m, "config")}>
            {t("Ver o agente")}
          </a>
        </div>,
      );
    }
  } else if (remote && p?.kind === "wsl") {
    banners.push(
      <div key="wsl" className="banner info">
        <span className="grow">{t("Distribuição do WSL2 nesta máquina Windows: os comandos rodam pelo wsl.exe e o engine é acessado pelo agente do Gantry (instalado sozinho no primeiro uso), por TLS em 127.0.0.1.")}</span>
      </div>,
    );
  } else if (remote && p?.kind === "tcp") {
    banners.push(
      <div key="tcp" className="banner info">
        <span className="grow">{t("Conectado direto à API do engine (tcp://). Sem SSH, o Gantry não lê o hardware, não executa compose nem cria VMs aqui.")}</span>
      </div>,
    );
  }

  // Not connected: say why and stop there.
  if (remote && !m.hosts.some((x) => x.ok)) {
    const x = m.hosts.find((y) => y.connecting) || p!;
    return (
      <div id="machine-view">
        {head}
        {banners}
        {x.connecting || x.idle ? (
          <Loading>{x.connecting ? t("Conectando… se o servidor ou a chave pedirem senha, responda na janela.") : t("Conectando…")}</Loading>
        ) : (
          <>
            <ErrorBox>{x.error || t("sem conexão")}</ErrorBox>
            {x.fix ? (
              <div className="actions" style={{ marginTop: 12 }}>
                <button className="btn primary" onClick={(e) => runFix(x, e.currentTarget)}>
                  {x.fix.label}
                </button>
              </div>
            ) : null}
            <p className="muted small">
              {x.kind === "ssh" ? t("O app usa o ssh do sistema e o seu ~/.ssh/config. Use Atualizar para tentar de novo.") : t("Confira se o engine está rodando e se seu usuário tem acesso ao socket.")}
            </p>
          </>
        )}
      </div>
    );
  }

  const engines = d?.engines ?? [];
  const nCtr = engines.reduce((a, e) => a + (e.containers || 0), 0);
  const vms = d?.vms ?? [];
  const tabs: [string, string, number | null][] = [
    ["projetos", t("Projetos"), d ? d.projects.length : null],
    ["containers", "Containers", d ? nCtr : null],
    ["vms", "VMs", d ? vms.length : null],
  ];
  if (p?.files) tabs.push(["servicos", t("Serviços"), shared?.data ? shared.data.length : null]);
  if (m.local) tabs.push(["engines", "Engines", null]);
  if (canTools) tabs.push(["ferramentas", t("Ferramentas"), missing || null]);
  if (p) tabs.push(["config", t("Configuração"), null]);

  let body: React.ReactNode;
  if (tab === "engines") body = <LocalEngines />;
  else if (tab === "ferramentas" && canTools) body = <ToolsTab mkey={m.key} local={m.local} host={p ? p.name : ""} />;
  else if (tab === "config") body = <ConfigTab m={m} />;
  else if (tab === "servicos" && p) body = <SharedTab m={m} />;
  else if (m.local && !m.hosts.some((x) => x.ok) && (tab === "projetos" || !tab))
    body = (
      <div className="block">
        <div className="block-head">
          <h2 className="block-title">{t("Projetos")}</h2>
          <span className="grow" />
          <a className="btn primary" href="#/local/novo">
            {t("＋ Criar projeto")}
          </a>
        </div>
        <Empty>{t("Nenhum projeto ainda. Para rodar projetos aqui, o Gantry precisa de um engine de containers: Criar projeto ajuda a instalar ou iniciar o Podman, ou veja a aba Engines.")}</Empty>
      </div>
    );
  else if (!p)
    body = (
      <div className="panel panel-pad">
        <p className="muted" style={{ margin: 0 }}>
          {t("Nenhum engine de containers nesta máquina. Veja a aba Engines para instalar ou iniciar um.")}
        </p>
      </div>
    );
  else if (!d) body = ov?.err ? <ErrorBox>{ov.err}</ErrorBox> : <Loading />;
  else if (tab === "vms") body = <VMsTab m={m} d={d} />;
  else if (tab === "containers") body = <ContainersTab m={m} engines={engines} meng={meng} mseg={mseg} />;
  else body = <ProjectsTab m={m} d={d} />;

  return (
    <div id="machine-view">
      {head}
      {banners}
      <SysBlock m={m} />
      <div className="tabs" role="tablist" aria-label={t("Recursos")}>
        {tabs.map(([k, label, n]) => (
          <a key={k} className="tab" role="tab" href={machHref(m, k)} aria-selected={tab === k}>
            {label}
            {n != null ? <span className="tab-count">{String(n)}</span> : null}
          </a>
        ))}
      </div>
      {body}
    </div>
  );
}

async function runFix(hst: Host, btn: HTMLButtonElement) {
  btn.disabled = true;
  btn.textContent = t("Aplicando…");
  try {
    const v = await api<Host>("/hosts/" + enc(hst.name) + "/fix/" + enc(hst.fix!.id), { method: "POST" });
    useStore.setState((s) => ({ hosts: s.hosts.map((x) => (x.name === v.name ? v : x)) }));
    gantry.toast(v.ok ? t("Pronto, conectado") : t("Aplicado; tentando conectar de novo"), "ok");
    gantry.scheduleConnectingPoll();
    if (v.ok) await gantry.loadLabs(hst.name);
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}
