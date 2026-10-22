// Time series of the metrics: shaping what the server sends for the charts.
export type Point = { t: number; c: number; m: number; d?: number; v?: number };
export type Series = { label: string; name?: string; vm?: boolean; slot: number | string; step: number; points: Point[] };

export const RANGES: [number, string][] = [
  [900, "15 min"],
  [3600, "1 h"],
  [6 * 3600, "6 h"],
  [24 * 3600, "24 h"],
];
const MAX_SERIES = 8;

// chartStart: the chart starts when collection started, if after the start
// of the period, so a short history does not become a dash in the corner.
export function chartStart(until: number, range: number, res: { series: { points: Point[] }[]; since?: number; step?: number }) {
  const from = until - range;
  const first = Math.min(...res.series.map((s) => (s.points[0] ? s.points[0].t : Infinity)), res.since || Infinity);
  if (!isFinite(first) || first <= from) return from;
  return Math.max(from, Math.min(first - (res.step || 10), until - 300));
}

type Raw = { kind?: string; name: string; service?: string; points: Point[] };

// Containers recreated by compose get a new id but keep the name; join them.
// Every container and VM stays its own series, heaviest first (average CPU
// and memory, each against the largest); the eight heaviest get a color and
// the rest are drawn in gray, still with their own name.
export function prepSeries(raw: Raw[], step: number): Series[] {
  const byName = new Map<string, Series>();
  for (const s of raw) {
    const vm = s.kind === "vm";
    const k = (vm ? "vm:" : "") + s.name;
    if (!byName.has(k)) byName.set(k, { label: vm ? "VM " + s.name : s.service || s.name, name: s.name, vm, points: [], slot: 0, step });
    byName.get(k)!.points.push(...s.points);
  }
  const list = [...byName.values()];
  for (const s of list) s.points.sort((a, b) => a.t - b.t);
  const avg = (s: Series, f: (p: Point) => number) => (s.points.length ? s.points.reduce((n, p) => n + f(p), 0) / s.points.length : 0);
  const cpu = new Map(list.map((s) => [s, avg(s, (p) => p.c)]));
  const mem = new Map(list.map((s) => [s, avg(s, (p) => p.m)]));
  const maxC = Math.max(1e-9, ...cpu.values()),
    maxM = Math.max(1e-9, ...mem.values());
  const weight = (s: Series) => cpu.get(s)! / maxC + mem.get(s)! / maxM;
  list.sort((a, b) => weight(b) - weight(a) || a.label.localeCompare(b.label));
  list.forEach((s, i) => {
    s.slot = i < MAX_SERIES ? i + 1 : "x";
    s.step = step;
  });
  return list;
}

export function fmtNum(v: number): string {
  const a = Math.abs(v);
  if (a >= 1e9) return (v / 1e9).toFixed(1) + "G";
  if (a >= 1e6) return (v / 1e6).toFixed(1) + "M";
  if (a >= 1e3) return (v / 1e3).toFixed(1) + "k";
  if (a >= 10 || a === 0) return v.toFixed(0);
  return v.toFixed(2);
}
