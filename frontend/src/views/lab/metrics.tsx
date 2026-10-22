// Metrics of a project (CPU and memory of its containers and VMs) and of a
// single container or VM (UsagePanel).
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, fmtPct, type Host } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { LOCALE, t } from "@/lib/i18n";
import { LineChart, Legend } from "@/components/chart";
import { ErrorBox, Loading } from "@/components/ui";
import { chartStart, prepSeries, RANGES, type Series } from "@/lib/series";
import { loadObs, PromQuery, useObs } from "@/views/host/obs";
import type { Lab } from "./types";

type Res = { series: { kind?: string; name: string; service?: string; points: { t: number; c: number; m: number; d?: number }[] }[]; step: number; since: number; interval: number };

function RangeSeg({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  return (
    <div className="seg" role="group" aria-label={t("Período")}>
      {RANGES.map(([v, label]) => (
        <button key={v} type="button" className="seg-btn" aria-pressed={value === v} onClick={() => onChange(v)}>
          {label}
        </button>
      ))}
    </div>
  );
}

function SummaryTable({ series, focus, onFocus }: { series: Series[]; focus?: string; onFocus: (label: string | undefined) => void }) {
  const stat = (pts: Series["points"], f: (p: Series["points"][number]) => number): [number | null, number | null, number | null] => {
    if (!pts.length) return [null, null, null];
    let sum = 0,
      mx = 0;
    for (const p of pts) {
      const v = f(p);
      sum += v;
      if (v > mx) mx = v;
    }
    return [sum / pts.length, mx, f(pts[pts.length - 1])];
  };
  return (
    <div className="panel table-wrap" style={{ marginTop: 16 }}>
      <table className="grid compact">
        <thead>
          <tr>
            {[t("Container ou VM"), t("CPU média"), t("CPU máx."), t("Memória média"), t("Memória máx."), t("Memória agora"), t("Disco")].map((h, i) => (
              <th key={i} className={i ? "num" : undefined}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {series.map((s, i) => {
            const c = stat(s.points, (p) => p.c),
              mm = stat(s.points, (p) => p.m);
            const last = s.points[s.points.length - 1];
            return (
              <tr key={i} className={"row-click" + (focus === s.label ? " selected" : "")} onClick={() => onFocus(focus === s.label ? undefined : s.label)}>
                <td>
                  <span className="legend-item">
                    <i className="swatch" style={{ background: "var(--s" + s.slot + ")" }} />
                    {s.label}
                  </span>
                </td>
                <td className="num">{fmtPct(c[0])}</td>
                <td className="num">{fmtPct(c[1])}</td>
                <td className="num">{fmtBytes(mm[0])}</td>
                <td className="num">{fmtBytes(mm[1])}</td>
                <td className="num">{fmtBytes(mm[2])}</td>
                <td className="num">{last && last.d ? fmtBytes(last.d) : "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function MetricsTab({ hst, lab }: { hst: Host; lab: Lab }) {
  const [range, setRange] = useState(() => gantry.store.get("metrics.range", 3600));
  const [source, setSource] = useState(() => gantry.store.get("metrics.source", "gantry"));
  const [data, setData] = useState<{ series: Series[]; since: number; until: number; note: string } | null>(null);
  const [err, setErr] = useState("");
  const [focus, setFocus] = useState<string | undefined>();
  const obs = useObs((s) => s[hst.name]);
  useEffect(() => {
    if (obs === undefined) loadObs(hst.name);
  }, [hst.name]); // eslint-disable-line react-hooks/exhaustive-deps
  const prom = !!obs?.services.prometheus?.ok;
  const src = prom ? source : "gantry";
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        const r = await api<{ result: Res; source: string }>("/hosts/" + enc(hst.name) + "/metrics?lab=" + enc(lab.id) + "&range=" + range + (src === "prometheus" ? "&source=prometheus" : ""));
        if (!live) return;
        const res = r.result;
        const until = Math.floor(Date.now() / 1000);
        const since = chartStart(until, range, res);
        const series = prepSeries(res.series, res.step);
        const startedLate = res.since > since + res.interval * 2;
        const parts: string[] = [];
        if (r.source === "prometheus") parts.push(t("Do Prometheus (métricas do cAdvisor)."));
        else if (r.source === "agent") parts.push(t("Histórico guardado pelo agente no servidor, mesmo com o app fechado."));
        else {
          parts.push(t("Coletado pelo app enquanto ele está aberto (até 6 h)."));
          if (hst.kind === "ssh") parts.push(t("Instale o agente na página do host para guardar o histórico com o app fechado."));
        }
        if (series.some((x) => x.vm)) parts.push(t("VMs: CPU e memória do processo QEMU na máquina, coletados pelo app."));
        if (startedLate && r.source !== "prometheus") parts.push(t("Dados desde {time}.", { time: new Date(res.since * 1000).toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" }) }));
        setData({ series, since, until, note: parts.join(" ") });
        setErr("");
      } catch (e) {
        if (live) setErr((e as Error).message);
      }
    };
    load();
    const timer = setInterval(() => {
      if (!document.hidden) load();
    }, 15000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [hst.name, lab.id, range, src]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div>
      <div className="block-head metrics-head">
        <p className="muted small metrics-note">{data?.note}</p>
        {prom ? (
          <div className="seg" role="group" aria-label={t("Fonte")}>
            {(
              [
                ["gantry", "Gantry"],
                ["prometheus", "Prometheus"],
              ] as [string, string][]
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                className="seg-btn"
                aria-pressed={src === v}
                onClick={() => {
                  setSource(v);
                  gantry.store.set("metrics.source", v);
                }}
              >
                {label}
              </button>
            ))}
          </div>
        ) : null}
        <RangeSeg
          value={range}
          onChange={(v) => {
            setRange(v);
            gantry.store.set("metrics.range", v);
          }}
        />
      </div>
      {err && !data ? (
        <ErrorBox>{err}</ErrorBox>
      ) : !data ? (
        <Loading>{t("Carregando métricas…")}</Loading>
      ) : !data.series.length ? (
        <div className="panel panel-pad">
          <p className="muted" style={{ margin: 0 }}>
            {t("Ainda não há amostras neste período. Os containers e as VMs precisam estar rodando; a primeira coleta leva alguns segundos.")}
          </p>
        </div>
      ) : (
        <div>
          <Legend series={data.series} focus={focus} onFocus={setFocus} />
          <div className="metrics-grid">
            <section className="panel panel-pad">
              <h2 className="chart-title">CPU</h2>
              <p className="muted small chart-sub">{t("% de um núcleo")}</p>
              <LineChart series={data.series} val={(p) => p.c} fmt={fmtPct as (v: number) => string} t0={data.since} t1={data.until} minMax={100} focus={focus} />
            </section>
            <section className="panel panel-pad">
              <h2 className="chart-title">{t("Memória")}</h2>
              <p className="muted small chart-sub">{t("em uso, sem cache")}</p>
              <LineChart series={data.series} val={(p) => p.m} fmt="bytes" t0={data.since} t1={data.until} focus={focus} />
            </section>
          </div>
          <SummaryTable series={data.series} focus={focus} onFocus={setFocus} />
        </div>
      )}
      <VMInside hst={hst} lab={lab} prom={prom} />
      {prom ? <PromQuery host={hst.name} lab={lab.id} range={range} /> : null}
    </div>
  );
}

type VMPoint = { t: number; cpu: number; cpus: number; memUsed: number; memTotal: number; diskUsed: number; diskTotal: number; rx: number; tx: number; load1: number; uptime: number };
type VMItem = { vm: string; port: number; point?: VMPoint; error?: string };

const fmtUptime = (s: number) => (s >= 86400 ? Math.floor(s / 86400) + " d " : "") + Math.floor((s % 86400) / 3600) + " h " + Math.floor((s % 3600) / 60) + " min";

// VMInside: the graphs from inside the VMs with metrics: true (their
// node_exporter), collected while the tab is open.
function VMInside({ hst, lab, prom }: { hst: Host; lab: Lab; prom: boolean }) {
  const [items, setItems] = useState<VMItem[] | null>(null);
  const [hist, setHist] = useState<Record<string, VMPoint[]>>({});
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        const r = await api<{ vms: VMItem[] }>("/hosts/" + enc(hst.name) + "/labs/" + enc(lab.id) + "/vm-metricas");
        if (!live) return;
        r.vms.sort((a, b) => a.vm.localeCompare(b.vm));
        setItems(r.vms);
        setHist((h) => {
          const out: Record<string, VMPoint[]> = {};
          for (const it of r.vms) {
            const prev = (h[it.vm] ?? []).filter((p) => p.t > Date.now() / 1000 - 900);
            out[it.vm] = it.point && it.point.cpu >= 0 ? [...prev, it.point] : prev;
          }
          return out;
        });
      } catch {
        if (live) setItems((x) => x ?? []);
      }
    };
    load();
    const timer = setInterval(() => !document.hidden && load(), 5000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [hst.name, lab.id]);
  if (!items || !items.length) return null;
  const series = items.map((it, i) => ({ label: it.vm, slot: (i % 8) + 1, step: 5, points: (hist[it.vm] ?? []).map((p) => ({ t: p.t, c: p.cpu, m: p.memUsed, v: p.rx + p.tx })) }));
  const now = Math.floor(Date.now() / 1000);
  const t0 = Math.min(now - 300, ...series.flatMap((x) => x.points.map((p) => p.t)));
  // The Prometheus of the lab (a container) reaches the VMs on the project
  // network (lan: true) by name; the forwarded port is only for this machine.
  const onLan = items.filter((it) => lab.vms?.find((v) => v.name === it.vm)?.lan);
  const scrape = "scrape_configs:\n  - job_name: gantry-vms\n    static_configs:\n" + onLan.map((it) => "      - targets: [\"" + it.vm + ":9100\"]\n        labels: { vm: " + it.vm + " }").join("\n");
  return (
    <div className="block vm-inside">
      <div className="block-head">
        <h2 className="block-title">{t("Dentro das VMs")}</h2>
        <span className="muted small">{t("do node_exporter de cada VM com metrics: true, a cada 5 s, enquanto esta aba está aberta")}</span>
      </div>
      <Legend series={series} />
      <div className="metrics-grid">
        <section className="panel panel-pad">
          <h2 className="chart-title">CPU</h2>
          <p className="muted small chart-sub">{t("% de um núcleo, de dentro da VM")}</p>
          <LineChart series={series} val={(p) => p.c} fmt={fmtPct as (v: number) => string} t0={t0} t1={now} minMax={100} />
        </section>
        <section className="panel panel-pad">
          <h2 className="chart-title">{t("Memória")}</h2>
          <p className="muted small chart-sub">{t("em uso (total menos disponível)")}</p>
          <LineChart series={series} val={(p) => p.m} fmt="bytes" t0={t0} t1={now} />
        </section>
        <section className="panel panel-pad">
          <h2 className="chart-title">{t("Rede")}</h2>
          <p className="muted small chart-sub">{t("bytes por segundo, entrada + saída")}</p>
          <LineChart series={series} val={(p) => p.v} fmt="bytes" t0={t0} t1={now} />
        </section>
      </div>
      <div className="panel table-wrap" style={{ marginTop: 16 }}>
        <table className="grid compact">
          <thead>
            <tr>
              {["VM", t("CPU agora"), t("Carga (1 min)"), t("Memória"), t("Disco /"), t("Ligada há")].map((h, i) => (
                <th key={i} className={i ? "num" : undefined}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {items.map((it) => {
              const p = it.point;
              return (
                <tr key={it.vm}>
                  <td>{it.vm}</td>
                  {p ? (
                    <>
                      <td className="num">{p.cpu >= 0 ? fmtPct(p.cpu) : "…"}</td>
                      <td className="num">{p.load1.toFixed(2) + " / " + p.cpus}</td>
                      <td className="num">{fmtBytes(p.memUsed) + " / " + fmtBytes(p.memTotal)}</td>
                      <td className="num">{fmtBytes(p.diskUsed) + " / " + fmtBytes(p.diskTotal)}</td>
                      <td className="num">{p.uptime ? fmtUptime(p.uptime) : "—"}</td>
                    </>
                  ) : (
                    <td colSpan={5} className="muted small">
                      {it.error === "parada" ? t("parada") : it.error}
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {prom ? (
        <details className="np-adv">
          <summary>{t("Coletar também no Prometheus do lab")}</summary>
          <p className="muted small">{t("O Prometheus (um container do lab) alcança pelo nome as VMs na rede do projeto (lan: true). Cole no prometheus.yml:")}</p>
          {onLan.length ? <pre className="kd-pre">{scrape}</pre> : null}
          {onLan.length < items.length ? (
            <p className="muted small">{t("Sem lan: true (fora do Prometheus): {list}. A porta delas no host só atende esta máquina.", { list: items.filter((x) => !onLan.includes(x)).map((x) => x.vm).join(", ") })}</p>
          ) : null}
        </details>
      ) : null}
    </div>
  );
}

// UsagePanel shows the history of one item (a container id or
// "vm:projeto/vm").
export function UsagePanel({ host, id, vm, memLimit, running }: { host: string; id: string; vm?: boolean; memLimit?: number; running?: boolean }) {
  const [range, setRange] = useState(() => gantry.store.get("usage.range", 3600));
  const [data, setData] = useState<{ s: Series; since: number; until: number } | null>(null);
  const [msg, setMsg] = useState(t("Carregando…"));
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        const r = await api<{ result: Res }>("/hosts/" + enc(host) + "/metrics?id=" + enc(id) + "&range=" + range);
        if (!live) return;
        const res = r.result;
        const until = Math.floor(Date.now() / 1000);
        const since = chartStart(until, range, res);
        const list = prepSeries(res.series, res.step);
        const s = list[0] || { label: "", slot: 1, step: res.step, points: [] };
        if (s.points.length) s.slot = 1;
        setData({ s, since, until });
        setMsg(
          s.points.length
            ? ""
            : running === false
              ? t("Sem amostras neste período. As métricas são coletadas enquanto ele está rodando.")
              : t("Coletando… a primeira amostra aparece em alguns segundos."),
        );
      } catch (e) {
        if (live) setMsg((e as Error).message);
      }
    };
    load();
    const timer = setInterval(() => {
      if (!document.hidden) load();
    }, 10000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [host, id, range, running]);
  const s = data?.s;
  const tile = (label: string, value: string, sub?: string | null) => (
    <div className="usage-tile">
      <span className="muted small">{label}</span>
      <b>{value}</b>
      {sub ? <span className="muted small">{sub}</span> : null}
    </div>
  );
  let now: React.ReactNode = null;
  if (s && s.points.length) {
    const last = s.points[s.points.length - 1];
    let peak = 0,
      sum = 0;
    for (const p of s.points) {
      sum += p.c;
      if (p.c > peak) peak = p.c;
    }
    now = (
      <div className="usage-now">
        {tile(t("CPU agora"), fmtPct(last.c), t("média {avg} · pico {peak}", { avg: fmtPct(sum / s.points.length), peak: fmtPct(peak) }))}
        {tile(t("Memória agora"), fmtBytes(last.m), memLimit ? t("de {limit}", { limit: fmtBytes(memLimit) }) : null)}
        {last.d ? tile(t("Disco"), fmtBytes(last.d), t("ocupado na máquina")) : null}
      </div>
    );
  }
  return (
    <div className="panel panel-pad usage">
      <div className="block-head">
        <h2 style={{ margin: 0 }}>{t("Uso")}</h2>
        <RangeSeg
          value={range}
          onChange={(v) => {
            setRange(v);
            gantry.store.set("usage.range", v);
          }}
        />
      </div>
      {now}
      {msg ? (
        <p className="muted small" style={{ margin: 0 }}>
          {msg}
        </p>
      ) : null}
      {s && s.points.length && data ? (
        <div className="metrics-grid usage-charts">
          <div>
            <h3 className="chart-title">CPU</h3>
            <p className="muted small chart-sub">{vm ? t("% de um núcleo, processo QEMU") : t("% de um núcleo")}</p>
            <LineChart series={[s]} val={(p) => p.c} fmt={fmtPct as (v: number) => string} t0={data.since} t1={data.until} minMax={100} />
          </div>
          <div>
            <h3 className="chart-title">{t("Memória")}</h3>
            <p className="muted small chart-sub">{vm ? t("do processo QEMU") : t("em uso, sem cache")}</p>

            <LineChart series={[s]} val={(p) => p.m} fmt="bytes" t0={data.since} t1={data.until} />
          </div>
        </div>
      ) : null}
    </div>
  );
}
