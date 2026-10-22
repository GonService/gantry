import type { ReactNode } from "react";

export function Meter({ frac }: { frac: number }) {
  const f = Math.max(0, Math.min(1, frac || 0));
  return (
    <div className="meter">
      <i style={{ width: (f * 100).toFixed(1) + "%" }} />
    </div>
  );
}

export function Tile({ label, value, meta, frac, href }: { label: string; value: ReactNode; meta?: ReactNode; frac?: number | null; href?: string }) {
  const t = (
    <div className="panel tile">
      <span className="label">{label}</span>
      <span className="value">{value}</span>
      {frac != null ? <Meter frac={frac} /> : null}
      <span className="meta">{meta}</span>
    </div>
  );
  return href ? (
    <a className="tile-link" href={href}>
      {t}
    </a>
  ) : (
    t
  );
}

// A meter with its text beside (tables of nodes).
export function KMeter({ frac, text }: { frac: number; text: string }) {
  return (
    <div className="kmeter">
      <Meter frac={frac} />
      <span>{text}</span>
    </div>
  );
}
