// A line chart of time series (CPU, memory, a PromQL query), drawn in SVG
// at the width of its box, with a crosshair and a tooltip.
import { useLayoutEffect, useRef, useState } from "react";
import { fmtBytes } from "@/lib/format";
import type { Series } from "@/lib/series";
import { t, LOCALE } from "@/lib/i18n";

function niceMax(v: number) {
  if (v <= 0) return 1;
  const e = Math.pow(10, Math.floor(Math.log10(v)));
  for (const k of [1, 2, 2.5, 5, 10]) if (k * e >= v) return k * e;
  return 10 * e;
}
// Axis labels share the unit of the top tick (no "1000 KB" under "1.5 MB").
function fmtBytesIn(v: number, max: number) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0,
    unit = 1;
  while (max / unit >= 1024 && i < u.length - 1) {
    unit *= 1024;
    i++;
  }
  const n = v / unit;
  return (Number.isInteger(n) ? n : n.toFixed(1)) + " " + u[i];
}
function niceBytes(v: number) {
  if (v <= 0) return 1024 * 1024;
  let unit = 1;
  while (v / unit >= 1024) unit *= 1024;
  return niceMax(v / unit) * unit;
}

type P = { t: number } & Record<string, number | undefined>;

export function LineChart({
  series,
  val,
  fmt,
  t0,
  t1,
  minMax = 0,
  focus,
}: {
  series: (Omit<Series, "points"> & { points: P[] })[] | Series[];
  val: (p: any) => number; // eslint-disable-line @typescript-eslint/no-explicit-any
  fmt: "bytes" | ((v: number) => string);
  t0: number;
  t1: number;
  minMax?: number;
  focus?: string; // the label of the series to highlight; the others fade
}) {
  const box = useRef<HTMLDivElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);
  const [W, setW] = useState(600);
  const [hover, setHover] = useState<number | null>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      if (el.clientWidth) setW(Math.max(200, el.clientWidth));
    });
    ro.observe(el);
    if (el.clientWidth) setW(Math.max(200, el.clientWidth));
    return () => ro.disconnect();
  }, []);
  const isBytes = fmt === "bytes";
  const f = isBytes ? fmtBytes : (fmt as (v: number) => string);
  const H = 200;
  const m = { l: 64, r: 12, t: 8, b: 24 };
  let max = 0;
  for (const s of series) for (const p of s.points) if (val(p) > max) max = val(p);
  max = 4 * (isBytes ? niceBytes(max / 4) : niceMax(Math.max(max, minMax ? 5 : 0) / 4));
  const x = (t: number) => m.l + ((t - t0) / (t1 - t0 || 1)) * (W - m.l - m.r);
  const y = (v: number) => m.t + (1 - v / max) * (H - m.t - m.b);

  const grid: React.ReactNode[] = [];
  for (let i = 0; i <= 4; i++) {
    const v = (max / 4) * i,
      yy = Math.round(y(v)) + 0.5;
    grid.push(<line key={"l" + i} x1={m.l} x2={W - m.r} y1={yy} y2={yy} className={i ? "gl" : "gl base"} />);
    grid.push(
      <text key={"t" + i} x={m.l - 8} y={yy + 4} textAnchor="end" className="tick">
        {isBytes ? fmtBytesIn(v, max) : f(v)}
      </text>,
    );
  }
  const span = t1 - t0;
  const tstep = [300, 900, 1800, 3600, 7200, 4 * 3600].find((s) => span / s <= 6) || 4 * 3600;
  for (let t = Math.ceil(t0 / tstep) * tstep; t <= t1; t += tstep) {
    grid.push(
      <text key={"x" + t} x={x(t)} y={H - 6} textAnchor="middle" className="tick">
        {new Date(t * 1000).toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" })}
      </text>,
    );
  }
  const paths = series.map((s, i) => {
    let d = "",
      prev: number | null = null;
    const gap = Math.max(s.step * 3, 45);
    for (const p of s.points) {
      if (p.t < t0) continue;
      d += (prev == null || p.t - prev > gap ? "M" : "L") + x(p.t).toFixed(1) + " " + y(val(p)).toFixed(1);
      prev = p.t;
    }
    const cls = "line" + (focus ? (s.label === focus ? " focus" : " faded") : s.slot === "x" ? " minor" : "");
    return d ? <path key={i} d={d} className={cls} style={{ stroke: "var(--s" + s.slot + ")" }} /> : null;
  });
  // The highlighted series is drawn last, over the others.
  if (focus) {
    const i = series.findIndex((s) => s.label === focus);
    if (i >= 0) paths.push(paths.splice(i, 1)[0]);
  }

  let tip: React.ReactNode = null;
  let cx = 0;
  if (hover != null) {
    cx = x(hover);
    const rows: [(typeof series)[number], number][] = [];
    for (const s of series) {
      if (focus && s.label !== focus) continue;
      const pt = (s.points as P[]).find((p) => Math.abs(p.t - hover) <= s.step);
      if (pt) rows.push([s, val(pt)]);
    }
    rows.sort((a, b) => b[1] - a[1]);
    tip = (
      <>
        <div className="tip-time">{new Date(hover * 1000).toLocaleTimeString(LOCALE)}</div>
        {rows.map(([s, v], i) => (
          <div className="tip-row" key={i}>
            <i className="swatch" style={{ background: "var(--s" + s.slot + ")" }} />
            <span>{s.label}</span>
            <b>{f(v)}</b>
          </div>
        ))}
      </>
    );
  }
  const tipW = tipRef.current?.offsetWidth ?? 200;
  const left = cx + 14 + 220 > W ? cx - 14 - tipW : cx + 14;
  return (
    <div className="chart" ref={box}>
      <svg width={W} height={H} viewBox={"0 0 " + W + " " + H} role="img">
        <g className="grid-lines">{grid}</g>
        {paths}
        <line y1={m.t} y2={H - m.b} x1={cx} x2={cx} className="cross" visibility={hover == null ? "hidden" : "visible"} />
        <rect
          x={m.l}
          y={m.t}
          width={Math.max(0, W - m.l - m.r)}
          height={H - m.t - m.b}
          fill="transparent"
          onMouseLeave={() => setHover(null)}
          onMouseMove={(e) => {
            const r = e.currentTarget.ownerSVGElement!.getBoundingClientRect();
            const t = t0 + ((e.clientX - r.left - m.l) / (W - m.l - m.r)) * (t1 - t0);
            let best: number | null = null;
            for (const s of series) for (const p of s.points) if (best == null || Math.abs(p.t - t) < Math.abs(best - t)) best = p.t;
            setHover(best);
          }}
        />
      </svg>
      <div className="chart-tip" ref={tipRef} hidden={hover == null} style={{ left: Math.max(0, left), top: m.t }}>
        {tip}
      </div>
    </div>
  );
}

export function Legend({ series, focus, onFocus }: { series: { label: string; slot: number | string }[]; focus?: string; onFocus?: (label: string | undefined) => void }) {
  return (
    <div className="legend" role="list" aria-label={t("Séries")}>
      {series.map((s, i) =>
        onFocus ? (
          <button
            type="button"
            className={"legend-item legend-btn" + (focus && focus !== s.label ? " faded" : "")}
            role="listitem"
            key={i}
            aria-pressed={focus === s.label}
            title={focus === s.label ? t("Mostrar todos") : t("Destacar {name}", { name: s.label })}
            onClick={() => onFocus(focus === s.label ? undefined : s.label)}
          >
            <i className="swatch" style={{ background: "var(--s" + s.slot + ")" }} />
            {s.label}
          </button>
        ) : (
          <span className="legend-item" role="listitem" key={i}>
            <i className="swatch" style={{ background: "var(--s" + s.slot + ")" }} />
            {s.label}
          </span>
        ),
      )}
    </div>
  );
}
