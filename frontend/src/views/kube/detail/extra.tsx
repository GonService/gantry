/* eslint-disable @typescript-eslint/no-explicit-any */
import { LineChart } from "@/components/chart";
import { LOCALE, t } from "@/lib/i18n";
import { Sec } from "./parts";

function Chart({ series, val, fmt, t0, t1 }: { series: any[]; val: (p: any) => number; fmt: "bytes" | ((v: number) => string); t0: number; t1: number }) {
  return <LineChart series={series} val={val} fmt={fmt} t0={t0} t1={t1} />;
}

// CPU and memory of the last hour (sampled by Gantry while the cluster is
// open; Kubernetes keeps only the value of now).
export function History({ kind, d }: { kind: string; d: any }) {
  const pts = d.history ?? [];
  const what = kind === "pods" ? t("do pod") : kind === "nodes" ? t("do nó") : t("somando os pods");
  if (pts.length < 2) {
    return (
      <Sec title={t("Uso · última hora")}>
        <p className="muted small">
          {(d.usage && Object.keys(d.usage).length) || d.node || d.containers
            ? t("Juntando dados: o Gantry anota o uso a cada {n} s enquanto o cluster está aberto.", { n: d.step || 15 })
            : t("Sem métricas: o cluster precisa do metrics-server.")}
        </p>
      </Sec>
    );
  }
  const fmtCPU = (v: number) => (v >= 1000 ? t("{n} núcleos", { n: (v / 1000).toLocaleString(LOCALE, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) }) : Math.round(v) + "m");
  const series = [{ label: t("Uso {what}", { what }), slot: 1, step: d.step || 15, points: pts.map((p: any) => ({ t: p.t, c: p.c, m: p.m })) }];
  const t1 = Math.max(pts[pts.length - 1].t, Math.floor(Date.now() / 1000));
  const t0 = Math.max(pts[0].t, t1 - 3600);
  return (
    <Sec title={t("Uso · última hora")}>
      <div className="kd-charts">
        <div>
          <div className="muted small">{t("CPU {what}", { what })}</div>
          <Chart series={series} val={(p) => p.c} fmt={fmtCPU} t0={t0} t1={t1} />
        </div>
        <div>
          <div className="muted small">{t("Memória {what}", { what })}</div>
          <Chart series={series} val={(p) => p.m} fmt="bytes" t0={t0} t1={t1} />
        </div>
      </div>
    </Sec>
  );
}
