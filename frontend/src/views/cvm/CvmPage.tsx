// The page of a Container VM: only what runs inside it (processes, logs)
// and how it is set up. Projects and containers stay under This Mac.
// Everything here is read only while the page is open and visible.
import { useEffect, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { fmtBytes } from "@/lib/format";
import { fmtUptime } from "@/lib/app";
import { useStore } from "@/lib/store";
import { confirm } from "@/components/dialog";
import { CopyCommand, ErrorBox, Loading } from "@/components/ui";
import { toast } from "@/shell/toasts";
import { t } from "@/lib/i18n";
import { cvmHref, engineNames, fmtRate, power, recreateHref, STATE, type CvmList, type CvmView, type MemoryState, type Proc, type Snapshot } from "./cvm";
import { EnginesTab, PlatformsTab, StorageTab } from "./tabs";
import { openTerminal } from "@/shell/dock";

type Sys = { cpus?: number; cpuPct?: number; hasCpu?: boolean; memTotal?: number; memUsed?: number; disks?: { path: string; used: number; total: number }[]; uptime?: number; load?: string; kernel?: string; os?: string; arch?: string; ip?: string };

// usePoll calls fn every ms while the page is visible and on is true.
function usePoll(fn: () => void | Promise<void>, ms: number, on: boolean, deps: unknown[]) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (!on) return;
    let stop = false;
    let timer = 0;
    const tick = async () => {
      if (stop) return;
      if (!document.hidden) {
        try {
          await ref.current();
        } catch {
          /* shown by the caller */
        }
      }
      if (!stop) timer = window.setTimeout(tick, ms);
    };
    void tick();
    const vis = () => {
      if (!document.hidden && !stop) {
        clearTimeout(timer);
        void tick();
      }
    };
    document.addEventListener("visibilitychange", vis);
    return () => {
      stop = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", vis);
    };
  }, [ms, on, ...deps]); // eslint-disable-line react-hooks/exhaustive-deps
}

export function CvmPage({ name, tab }: { name: string; tab: string }) {
  const [v, setV] = useState<CvmView | null>(null);
  const [err, setErr] = useState("");
  const local = useStore((s) => s.local);
  const load = async () => {
    try {
      const l = await api<CvmList>("/cvm");
      const m = l.machines.find((x) => x.name === name);
      if (!m) setErr(t("A máquina {name} não existe.", { name }));
      else setV(m);
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  useEffect(() => {
    void load();
  }, [name, local]); // eslint-disable-line react-hooks/exhaustive-deps
  if (err)
    return (
      <div className="empty">
        <ErrorBox>{err}</ErrorBox>
        <a className="btn" href="#/local/t/engines">
          {t("Voltar")}
        </a>
      </div>
    );
  if (!v) return <Loading />;
  const running = v.state === "running";
  const [dot, stText] = STATE[v.state] || ["", v.state];
  const tabs: [string, string][] = [
    ["processos", t("Processos")],
    ["logs", t("Logs e terminal")],
    ["engines", t("Engines e sockets")],
    ["recursos", t("Recursos")],
    ["armazenamento", t("Armazenamento")],
    ["plataformas", t("Multi-arch e credenciais")],
    ["script", t("Script")],
  ];
  const cur = tabs.some(([k]) => k === tab) ? tab : "processos";
  return (
    <div id="cvm-view">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href="#/local">{t("Este Mac")}</a>
            <span>Container VM</span>
          </nav>
          <h1>
            {v.name}
            <span className={"badge " + (dot === "on" ? "ok" : dot)}>
              {dot === "warn" ? <span className="spin" /> : <span className={"dot " + dot} />}
              {stText}
            </span>
          </h1>
          <div className="sub">
            Apple Virtualization · {engineNames(v.spec)} · {v.dir}
          </div>
        </div>
        <div className="actions">
          {running ? (
            <>
              <button className="btn" type="button" onClick={() => void power(v.name, "reiniciar", load)}>
                {t("Reiniciar")}
              </button>
              <button className="btn danger" type="button" onClick={() => void power(v.name, "desligar", load)}>
                {t("Desligar")}
              </button>
            </>
          ) : v.state === "stopped" ? (
            <button className="btn primary" type="button" onClick={() => void power(v.name, "ligar", load)}>
              {t("Ligar")}
            </button>
          ) : null}
        </div>
      </div>
      {v.pending ? (
        <div className="banner warn">
          <span className="grow">{t("CPU ou memória mudaram: valem quando a máquina reiniciar.")}</span>
          <button className="btn" type="button" onClick={() => void power(v.name, "reiniciar", load)}>
            {t("Reiniciar agora")}
          </button>
        </div>
      ) : null}
      <SysBlock v={v} />
      <div className="tabs" role="tablist" aria-label={t("Seções da máquina")}>
        {tabs.map(([k, label]) => (
          <a key={k} className="tab" role="tab" href={cvmHref(v.name, k)} aria-selected={cur === k}>
            {label}
          </a>
        ))}
      </div>
      {cur === "processos" ? running ? <Procs name={v.name} /> : <Off v={v} load={load} /> : null}
      {cur === "logs" ? <Logs v={v} /> : null}
      {cur === "engines" ? <EnginesTab v={v} /> : null}
      {cur === "recursos" ? <Resources v={v} reload={load} /> : null}
      {cur === "armazenamento" ? <StorageTab v={v} reload={load} /> : null}
      {cur === "plataformas" ? <PlatformsTab v={v} reload={load} /> : null}
      {cur === "script" ? <SavedScript name={v.name} /> : null}
    </div>
  );
}

function Off({ v, load }: { v: CvmView; load: () => void }) {
  return (
    <div className="panel panel-pad cvm-off">
      <span>{v.state === "stopped" ? t("Desligada. Os containers desta máquina voltam do jeito que estavam quando ela ligar.") : t("A máquina está {state}.", { state: STATE[v.state]?.[1] || v.state })}</span>
      {v.state === "stopped" ? (
        <button className="btn primary" type="button" onClick={() => void power(v.name, "ligar", load)}>
          {t("Ligar agora")}
        </button>
      ) : null}
    </div>
  );
}

function Meter({ label, used, total, text, sub }: { label: string; used: number; total: number; text: string; sub?: string }) {
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
      {sub ? <span className="muted small">{sub}</span> : null}
    </div>
  );
}

function SysBlock({ v }: { v: CvmView }) {
  const [s, setS] = useState<Sys | null>(null);
  const running = v.state === "running";
  const [m, setM] = useState<MemoryState | null>(null);
  usePoll(async () => setS(await api<Sys>("/cvm/" + enc(v.name) + "/sistema")), 5000, running, [v.name]);
  usePoll(async () => setM(await api<MemoryState | null>("/cvm/" + enc(v.name) + "/memoria")), 5000, running, [v.name]);
  const back = running && m?.balloon && m.returned > 0 ? m.returned : 0;
  const data = v.disks.find((d) => d.name === "dados.raw");
  const sysd = v.disks.find((d) => d.name === "sistema.raw");
  const dataFs = s?.disks?.find((d) => d.path === "/var/lib/gantry");
  const root = s?.disks?.find((d) => d.path === "/");
  return (
    <div className="panel panel-pad sys">
      <div className="sys-facts">
        <span>{v.name}</span>
        {s?.os ? <span>{s.os}</span> : <span>Debian 13</span>}
        {s?.kernel ? <span>kernel {s.kernel}</span> : null}
        <span>{s?.arch || "arm64"}{v.spec.rosetta ? " + amd64 (Rosetta)" : ""}</span>
        {running && s?.uptime ? <span>{t("ligada há {uptime}", { uptime: fmtUptime(s.uptime) })}</span> : null}
        {running && s?.load ? <span>{t("carga {load}", { load: s.load })}</span> : null}
        {back ? <span className="cvm-back">{t("devolvendo {n} ao Mac", { n: fmtBytes(back) })}</span> : null}
      </div>
      {running ? (
        <div className="meters">
          <Meter label="CPU" used={s?.cpuPct || 0} total={100} text={s?.hasCpu ? t("{pct}% de {n} núcleos", { pct: (s.cpuPct || 0).toFixed(0), n: v.spec.cpus }) : t("{n} núcleos", { n: v.spec.cpus })} />
          <Meter label={t("Memória")} used={s?.memUsed || 0} total={s?.memTotal || v.spec.memoryMiB * 1048576} text={s?.memUsed ? t("{used} de {total}", { used: fmtBytes(s.memUsed), total: fmtBytes(s.memTotal) }) : fmtBytes(v.spec.memoryMiB * 1048576)}
            sub={m?.balloon ? t("com a máquina agora: {n} de {max}", { n: fmtBytes(m.target), max: fmtBytes(m.max) }) : undefined}
          />
          <Meter
            label={t("Disco de dados")}
            used={dataFs?.used || 0}
            total={dataFs?.total || (data?.size ?? 0)}
            text={dataFs ? t("{used} de {total}", { used: fmtBytes(dataFs.used), total: fmtBytes(dataFs.total) }) : fmtBytes(data?.size)}
            sub={data ? t("ocupa {n} no Mac", { n: fmtBytes(data.occupied) }) : undefined}
          />
          <Meter label={t("Disco do sistema")} used={root?.used || 0} total={root?.total || (sysd?.size ?? 0)} text={root ? t("{used} de {total}", { used: fmtBytes(root.used), total: fmtBytes(root.total) }) : fmtBytes(sysd?.size)} />
        </div>
      ) : (
        <p className="muted" style={{ margin: 0 }}>
          {t("Desligada: {cpus} CPU, {mem} de memória e {disk} de disco de dados quando ligar.", { cpus: v.spec.cpus, mem: fmtBytes(v.spec.memoryMiB * 1048576), disk: fmtBytes((data?.size ?? 0) || v.spec.dataGiB * 1073741824) })}
        </p>
      )}
    </div>
  );
}

const KINDS: [string, string][] = [
  ["", t("Todos")],
  ["container", t("Containers")],
  ["docker", "Docker"],
  ["podman", "Podman"],
  ["sistema", t("Sistema")],
];
const KIND_COLOR: Record<string, string> = { container: "var(--cvm-c1)", docker: "var(--cvm-c2)", podman: "var(--cvm-c3)", sistema: "var(--cvm-c4)" };
const heat = (v: number) => (v > 60 ? "hot" : v > 25 ? "warm" : "");

function Procs({ name }: { name: string }) {
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [err, setErr] = useState("");
  const [rate, setRate] = useState(2000);
  const [paused, setPaused] = useState(false);
  const [only, setOnly] = useState("");
  const [by, setBy] = useState<"cpu" | "rss">("cpu");
  const [q, setQ] = useState("");
  const [hist, setHist] = useState<number[]>([]);
  usePoll(
    async () => {
      try {
        const s = await api<Snapshot>("/cvm/" + enc(name) + "/processos");
        setErr("");
        setSnap(s);
        if (!s.first) setHist((h) => [...h.slice(-59), s.cpu]);
      } catch (e) {
        setErr((e as Error).message);
      }
    },
    rate,
    !paused,
    [name],
  );
  if (err && !snap) return <ErrorBox>{err}</ErrorBox>;
  if (!snap) return <Loading>{t("Lendo os processos…")}</Loading>;
  const counts: Record<string, number> = { "": snap.procs.length };
  for (const p of snap.procs) counts[p.kind] = (counts[p.kind] || 0) + 1;
  const ql = q.trim().toLowerCase();
  const list = snap.procs
    .filter((p) => (!only || p.kind === only) && (!ql || p.cmd.toLowerCase().includes(ql) || String(p.pid) === ql || p.origin.toLowerCase().includes(ql)))
    .sort((a, b) => (by === "cpu" ? b.cpu - a.cpu || b.rss - a.rss : b.rss - a.rss));
  const shown = list.slice(0, 80);
  const maxRss = Math.max(1, ...shown.map((p) => p.rss));
  const signal = async (p: Proc, sig: string) => {
    if (!(await confirm(t("Enviar {sig} para {pid}?", { sig, pid: p.pid }), p.cmd, sig === "KILL" ? t("Encerrar à força") : t("Encerrar"), true))) return;
    try {
      await api("/cvm/" + enc(name) + "/sinal", { method: "POST", body: { pid: p.pid, signal: sig } });
      toast(t("Sinal enviado."), "ok");
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  const mem = (n: number) => ((n / (snap.memTotal || 1)) * 100).toFixed(1) + "%";
  return (
    <div className="cvm-procs">
      <section className="panel">
        <div className="cvm-ph">
          <span className="cvm-actions">
            <h2>{t("Agora")}</h2>
            <span className={"cvm-live" + (paused ? " off" : "")}>
              <span aria-hidden="true" />
              {paused ? t("pausado") : t("ao vivo")}
            </span>
            <span className="muted small">{t("{n} processos · {th} threads · {r} rodando", { n: snap.tasks, th: snap.threads, r: snap.running })}</span>
          </span>
          <span className="cvm-actions">
            <label className="muted small">
              {t("Atualizar a cada")}{" "}
              <select value={rate} onChange={(e) => setRate(+e.target.value)}>
                <option value={1000}>1 s</option>
                <option value={2000}>2 s</option>
                <option value={5000}>5 s</option>
              </select>
            </label>
            <button className="btn small" type="button" onClick={() => setPaused(!paused)}>
              {paused ? t("Retomar") : t("Pausar")}
            </button>
          </span>
        </div>
        {err ? <div className="cvm-pb"><ErrorBox>{err}</ErrorBox></div> : null}
        <div className="cvm-now">
          <div className="cvm-cores">
            <span className="cvm-legend">
              <b>{t("CPU por núcleo")}</b>
              <span className="muted small">
                <i className="sw u" />
                {t("usuário")} <i className="sw s" />
                {t("sistema")}
              </span>
            </span>
            {snap.cores.map((c, i) => (
              <span className="cvm-core" key={i}>
                <span className="mono">{i + 1}</span>
                <span className="cvm-track" role="img" aria-label={t("Núcleo {n}: {pct}%", { n: i + 1, pct: (c.user + c.system).toFixed(0) })}>
                  <span className="u" style={{ width: c.user + "%" }} />
                  <span className="s" style={{ width: c.system + "%" }} />
                </span>
                <span className="mono cvm-pct">{(c.user + c.system).toFixed(1)}%</span>
              </span>
            ))}
            <div className="cvm-spark" role="img" aria-label={t("CPU total no último minuto")}>
              {hist.map((v, i) => (
                <span key={i} className={heat(v)} style={{ height: Math.max(2, Math.min(100, v)) + "%" }} />
              ))}
            </div>
            <span className="cvm-legend muted small mono">
              <span>{t("−{n} s", { n: Math.round((hist.length * rate) / 1000) })}</span>
              <span>{t("CPU total {pct}%", { pct: snap.cpu.toFixed(1) })}</span>
              <span>{t("agora")}</span>
            </span>
          </div>
          <div className="cvm-mem">
            <span className="cvm-legend">
              <b>{t("Memória")}</b>
              <span className="mono">{t("{used} de {total}", { used: fmtBytes(snap.memUsed), total: fmtBytes(snap.memTotal) })}</span>
            </span>
            <span className="cvm-track big" role="img" aria-label={t("Memória em uso")}>
              <span className="u" style={{ width: mem(snap.memUsed) }} />
              <span className="b" style={{ width: mem(snap.memBuffers) }} />
              <span className="c" style={{ width: mem(Math.max(0, snap.memCached)) }} />
            </span>
            <span className="muted small">
              <i className="sw u" />
              {t("em uso")} <i className="sw b" />
              buffers <i className="sw c" />
              cache
            </span>
            <span className="cvm-legend" style={{ marginTop: 8 }}>
              <b>Swap</b>
              <span className="mono">{t("{used} de {total}", { used: fmtBytes(snap.swapUsed), total: fmtBytes(snap.swapTotal) })}</span>
            </span>
            <span className="cvm-track">
              <span className="b" style={{ width: snap.swapTotal ? (snap.swapUsed / snap.swapTotal) * 100 + "%" : 0 }} />
            </span>
            <div className="cvm-cards">
              <span>
                <span className="muted small">{t("Carga 1 · 5 · 15 min")}</span>
                <b className="mono">{snap.load.split(" ").join(" · ")}</b>
              </span>
              <span>
                <span className="muted small">{t("Disco leitura · escrita")}</span>
                <b className="mono">
                  {fmtRate(snap.diskRead)} · {fmtRate(snap.diskWrite)}
                </b>
              </span>
              <span>
                <span className="muted small">{t("Rede entrada · saída")}</span>
                <b className="mono">
                  {fmtRate(snap.netIn)} · {fmtRate(snap.netOut)}
                </b>
              </span>
            </div>
          </div>
        </div>
      </section>
      <section className="panel">
        <div className="cvm-ph">
          <span className="cvm-chips" role="group" aria-label={t("Filtrar por origem")}>
            {KINDS.map(([k, label]) =>
              k && !counts[k] ? null : (
                <button key={k} type="button" aria-pressed={only === k} onClick={() => setOnly(k)}>
                  {k ? <i style={{ background: KIND_COLOR[k] }} /> : null}
                  {label} <span>{counts[k] || 0}</span>
                </button>
              ),
            )}
          </span>
          <input className="input cvm-filter" type="search" placeholder={t("Filtrar por nome ou PID…")} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("Filtrar processos")} />
        </div>
        <div className="table-wrap">
          <table className="grid cvm-ptable">
            <thead>
              <tr>
                <th>PID</th>
                <th>{t("Processo")}</th>
                <th>{t("Estado")}</th>
                <th style={{ width: 180 }}>
                  <button className="cvm-sort" type="button" onClick={() => setBy("cpu")}>
                    CPU {by === "cpu" ? "▾" : ""}
                  </button>
                </th>
                <th style={{ width: 180 }}>
                  <button className="cvm-sort" type="button" onClick={() => setBy("rss")}>
                    {t("Memória")} {by === "rss" ? "▾" : ""}
                  </button>
                </th>
                <th>{t("Usuário")}</th>
                <th>{t("Tempo")}</th>
                <th className="act" />
              </tr>
            </thead>
            <tbody>
              {shown.map((p) => (
                <tr key={p.pid} className={p.cpu > 25 ? "hot" : ""}>
                  <td className="mono muted">{p.pid}</td>
                  <td>
                    <span className="cvm-pname">
                      <i style={{ background: KIND_COLOR[p.kind] || KIND_COLOR.sistema }} />
                      <span>
                        <span className="mono" title={p.cmd}>
                          {p.cmd.length > 90 ? p.cmd.slice(0, 90) + "…" : p.cmd}
                        </span>
                        {p.rosetta ? <span className="badge cvm-ros">amd64 · Rosetta</span> : null}
                        <br />
                        <small style={{ color: KIND_COLOR[p.kind] || KIND_COLOR.sistema }}>{p.kind === "sistema" ? t("sistema") : p.origin}</small>
                      </span>
                    </span>
                  </td>
                  <td>
                    <span className={"cvm-st" + (p.state === "R" ? " r" : "")} title={p.state === "R" ? t("rodando agora") : t("esperando")}>
                      {p.state}
                    </span>
                  </td>
                  <td>
                    <span className="cvm-bar">
                      <span className="cvm-track sm">
                        <span className={"u " + heat(p.cpu)} style={{ width: Math.min(100, p.cpu) + "%" }} />
                      </span>
                      <span className="mono">{p.cpu.toFixed(1)}%</span>
                    </span>
                  </td>
                  <td>
                    <span className="cvm-bar">
                      <span className="cvm-track sm">
                        <span className="b" style={{ width: (p.rss / maxRss) * 100 + "%" }} />
                      </span>
                      <span className="mono">{fmtBytes(p.rss)}</span>
                    </span>
                  </td>
                  <td className="mono">{p.user}</td>
                  <td className="mono muted">{fmtUptime(p.elapsed)}</td>
                  <td className="act">
                    <button className="btn small" type="button" onClick={() => void signal(p, "TERM")}>
                      {t("Encerrar")}
                    </button>
                    <button className="btn small danger" type="button" onClick={() => void signal(p, "KILL")} title={t("Encerrar à força (KILL)")}>
                      KILL
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="muted small cvm-foot">
          {list.length > shown.length ? t("Mostrando os {n} mais pesados de {total}. ", { n: shown.length, total: list.length }) : ""}
          {t("Lido pela conexão da máquina, só enquanto esta aba está aberta e a janela visível.")}
        </p>
      </section>
    </div>
  );
}

const SOURCES: [string, string][] = [
  ["sistema", t("Sistema")],
  ["docker", "Docker"],
  ["podman", "Podman"],
  ["provisionamento", t("Provisionamento")],
  ["console", t("Console")],
  ["vm", t("Máquina")],
];

function Logs({ v }: { v: CvmView }) {
  const [src, setSrc] = useState("sistema");
  const [text, setText] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [follow, setFollow] = useState(true);
  const pre = useRef<HTMLPreElement>(null);
  const local = src === "console" || src === "vm";
  const can = local || v.state === "running";
  usePoll(
    async () => {
      try {
        const r = await api<{ text: string }>("/cvm/" + enc(v.name) + "/logs?fonte=" + src + "&n=500");
        setErr("");
        setText(r.text);
      } catch (e) {
        setErr((e as Error).message);
      }
    },
    3000,
    can && follow,
    [src, v.name],
  );
  useEffect(() => {
    setText(null);
  }, [src]);
  useEffect(() => {
    if (follow && pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [text, follow]);
  const dir = v.dir;
  const ssh = `ssh -i ${dir}/id_ed25519 -o IdentitiesOnly=yes -o ProxyCommand="nc -U ${dir}/ssh.sock" -o UserKnownHostsFile=${dir}/known_hosts gantry@${v.name}`;
  return (
    <div className="cvm-logs">
      <section className="panel">
        <div className="cvm-ph">
          <span className="cvm-chips" role="tablist" aria-label={t("Fonte do log")}>
            {SOURCES.filter(([k]) => (k !== "docker" || v.spec.docker) && (k !== "podman" || v.spec.podman)).map(([k, label]) => (
              <button key={k} type="button" role="tab" aria-pressed={src === k} onClick={() => setSrc(k)}>
                {label}
              </button>
            ))}
          </span>
          <label className="cvm-check small">
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
            <span>{t("Seguir")}</span>
          </label>
        </div>
        {!can ? (
          <p className="cvm-pb muted">{t("A máquina está desligada; Console e Máquina funcionam mesmo assim.")}</p>
        ) : err && text == null ? (
          <div className="cvm-pb">
            <ErrorBox>{err}</ErrorBox>
          </div>
        ) : text == null ? (
          <Loading />
        ) : (
          <pre className="cvm-term" ref={pre}>
            {text || t("(vazio)")}
          </pre>
        )}
      </section>
      <section className="panel panel-pad">
        <h2 style={{ marginBottom: 6 }}>{t("Terminal na máquina")}</h2>
        <p className="muted small" style={{ margin: "0 0 8px" }}>
          {t("Pela vsock, com a chave só desta máquina: sem senha e sem porta de rede.")}
        </p>
        <div className="cvm-actions" style={{ marginBottom: 10 }}>
          <button className="btn primary" type="button" disabled={v.state !== "running"} onClick={() => openShell(v.name, "")}>
            {t("Abrir terminal")}
          </button>
          {["sistema", "docker", "podman"]
            .filter((k) => (k !== "docker" || v.spec.docker) && (k !== "podman" || v.spec.podman))
            .map((k) => (
              <button key={k} className="btn" type="button" disabled={v.state !== "running"} onClick={() => openShell(v.name, k)}>
                {t("Seguir o log: {src}", { src: k === "sistema" ? t("sistema") : k === "docker" ? "Docker" : "Podman" })}
              </button>
            ))}
        </div>
        <p className="muted small" style={{ margin: "0 0 6px" }}>
          {t("Do terminal do Mac:")}
        </p>
        <CopyCommand cmd={ssh} />
      </section>
    </div>
  );
}

// openShell opens a shell (or a followed log) of the machine in the dock.
function openShell(name: string, logs: string) {
  const label = logs ? name + " · log " + logs : name;
  openTerminal("", { id: "cvm-" + name + (logs ? "-" + logs : ""), name: label, service: label, termURL: "/cvm/" + enc(name) + "/terminal" + (logs ? "?logs=" + logs : "") });
}

function Resources({ v, reload }: { v: CvmView; reload: () => void }) {
  const [cpus, setCpus] = useState(v.spec.cpus);
  const [mem, setMem] = useState(Math.round(v.spec.memoryMiB / 1024));
  const [disk, setDisk] = useState(v.spec.dataGiB);
  const [auto, setAuto] = useState(v.spec.autoStart);
  const [idle, setIdle] = useState(v.spec.idleStopMin || 0);
  const [busy, setBusy] = useState(false);
  const [balloonBusy, setBalloonBusy] = useState(false);
  const setBalloon = async (on: boolean) => {
    setBalloonBusy(true);
    try {
      await api("/cvm/" + enc(v.name), { method: "PUT", body: { noBalloon: !on } });
      toast(on ? t("Memória dinâmica ligada: o que sobrar volta ao Mac.") : t("Memória dinâmica desligada: a máquina fica com toda a memória."), "ok");
      reload();
    } catch (e) {
      toast((e as Error).message, "err");
    }
    setBalloonBusy(false);
  };
  const dirty = cpus !== v.spec.cpus || mem * 1024 !== v.spec.memoryMiB || disk !== v.spec.dataGiB || auto !== v.spec.autoStart || idle !== (v.spec.idleStopMin || 0);
  const save = async () => {
    setBusy(true);
    try {
      await api("/cvm/" + enc(v.name), { method: "PUT", body: { cpus, memoryMiB: mem * 1024, dataGiB: disk, autoStart: auto, idleStopMin: idle } });
      toast(v.state === "running" ? t("Salvo. CPU, memória e disco valem quando a máquina reiniciar.") : t("Salvo."), "ok");
      reload();
    } catch (e) {
      toast((e as Error).message, "err");
    }
    setBusy(false);
  };
  const del = async () => {
    if (!(await confirm(t("Apagar {name}?", { name: v.name }), t("A máquina e os dois discos são apagados: imagens, volumes e containers dela somem. Os projetos nas pastas do Mac ficam."), t("Apagar"), true))) return;
    void power(v.name, "apagar", () => (location.hash = "#/local/t/engines"));
  };
  return (
    <div className="cvm-cols">
      <div className="cvm-main">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Processador e memória")}</h2>
          </div>
          <div className="cvm-pb cvm-grid">
            <label className="cvm-field">
              <span>
                {t("CPUs")} <span className="mono muted">{cpus}</span>
              </span>
              <input type="range" min={1} max={16} value={cpus} onChange={(e) => setCpus(+e.target.value)} />
              <span className="muted small">{t("CPU parada não gasta bateria: a máquina usa só o que precisa.")}</span>
            </label>
            <label className="cvm-field">
              <span>
                {t("Memória máxima")} <span className="mono muted">{mem} GiB</span>
              </span>
              <input type="range" min={2} max={64} value={mem} onChange={(e) => setMem(+e.target.value)} />
            </label>
            <label className="cvm-check cvm-wide">
              <input type="checkbox" checked={!v.spec.noBalloon} disabled={balloonBusy} onChange={(e) => void setBalloon(e.target.checked)} />
              <span>
                <b>{t("Memória dinâmica")}</b>
                <br />
                <small className="muted">{t("A máquina fica só com o que os containers usam, mais uma folga; o resto volta ao Mac. Quando precisa, ela pega de volta na hora, até a memória máxima. Vale na hora.")}</small>
              </span>
            </label>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Discos")}</h2>
            <span className="muted small mono">{v.dir}</span>
          </div>
          <div className="table-wrap">
            <table className="grid" style={{ minWidth: 560 }}>
              <thead>
                <tr>
                  <th>{t("Disco")}</th>
                  <th>{t("Ocupa no Mac")}</th>
                  <th>{t("Tamanho máximo")}</th>
                </tr>
              </thead>
              <tbody>
                {v.disks.map((d) => (
                  <tr key={d.name}>
                    <td>
                      <b>{d.name === "dados.raw" ? t("Dados") : t("Sistema")}</b>
                      <br />
                      <small className="muted">{d.name === "dados.raw" ? t("imagens, volumes e containers; nunca é recriado") : t("trocado nas atualizações")}</small>
                    </td>
                    <td className="mono">{fmtBytes(d.occupied)}</td>
                    <td className="mono">
                      {d.name === "dados.raw" ? (
                        <label className="cvm-inline">
                          <input className="input" type="number" min={v.spec.dataGiB} step={16} value={disk} onChange={(e) => setDisk(Math.max(v.spec.dataGiB, +e.target.value || v.spec.dataGiB))} aria-label={t("Tamanho do disco de dados em GiB")} /> GiB
                        </label>
                      ) : (
                        fmtBytes(d.size)
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="muted small cvm-foot">{t("O disco de dados só aumenta; a máquina ocupa o tamanho novo no próximo boot, sem mexer nos dados.")}</p>
        </section>
        <div className="cvm-actions">
          <button className="btn primary" type="button" disabled={!dirty || busy} onClick={() => void save()}>
            {t("Salvar")}
          </button>
          {v.state === "running" && dirty ? <span className="muted small">{t("Vale quando a máquina reiniciar.")}</span> : null}
        </div>
      </div>
      <aside className="cvm-side">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Ligar e desligar")}</h2>
          </div>
          <div className="cvm-pb">
            <label className="cvm-check">
              <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
              <span>{t("Ligar quando o Gantry abrir")}</span>
            </label>
            <label className="cvm-check">
              <input type="checkbox" checked={idle > 0} onChange={(e) => setIdle(e.target.checked ? 30 : 0)} />
              <span>
                {t("Desligar sem nenhum container rodando por")}{" "}
                <select value={idle || 30} disabled={!idle} onChange={(e) => setIdle(+e.target.value)} aria-label={t("Minutos")}>
                  <option value={15}>15 min</option>
                  <option value={30}>30 min</option>
                  <option value={60}>1 h</option>
                  <option value={120}>2 h</option>
                </select>
              </span>
            </label>
            <p className="muted small" style={{ margin: 0 }}>
              {t("Fechar o Gantry não desliga a máquina. Ao suspender o Mac ela pausa junto e volta ao acordar.")}
            </p>
            <span className="muted small">{t("Lembre de salvar.")}</span>
          </div>
        </section>
        <section className="panel panel-pad">
          <h2 style={{ marginBottom: 6 }}>{t("Recriar mantendo os dados")}</h2>
          <p className="muted small" style={{ margin: "0 0 10px" }}>
            {t("Troca o sistema por um novo (outra versão do script, outros engines) e mantém o disco de dados. Tira um snapshot antes.")}
          </p>
          <a className="btn" href={recreateHref(v.name)}>
            {t("Recriar…")}
          </a>
        </section>
        <section className="panel panel-pad">
          <h2 style={{ marginBottom: 6 }}>{t("Apagar a máquina")}</h2>
          <p className="muted small" style={{ margin: "0 0 10px" }}>
            {t("Apaga a VM e os dois discos, com as imagens, volumes e containers dela.")}
          </p>
          <button className="btn danger" type="button" onClick={() => void del()}>
            {t("Apagar {name}…", { name: v.name })}
          </button>
        </section>
      </aside>
    </div>
  );
}

function SavedScript({ name }: { name: string }) {
  const [s, setS] = useState<{ script: string; sha256: string } | null>(null);
  useEffect(() => {
    void api<{ script: string; sha256: string }>("/cvm/" + enc(name) + "/script").then(setS);
  }, [name]);
  if (!s) return <Loading />;
  return (
    <section className="panel">
      <div className="cvm-ph">
        <span>
          <h2>provision.sh</h2>
          <span className="mono muted small">sha256 {s.sha256}</span>
        </span>
        <span className="muted small">{t("O script aprovado na criação, como rodou na máquina.")}</span>
      </div>
      <pre className="cvm-code">{s.script || t("(sem script salvo)")}</pre>
    </section>
  );
}
