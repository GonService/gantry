// Filtering and pages of long lists, as in the rest of the interface: the
// text of each filter and the page are kept per list while the app is open,
// the page size per viewer.
import { useEffect, useRef, useState } from "react";
import { create } from "zustand";
import { t } from "@/lib/i18n";

const PAGE_SIZES = [10, 25, 50, 100];

function storedSize(): number {
  try {
    return Number(localStorage.getItem("gantry.pageSize")) || 25;
  } catch {
    return 25;
  }
}

type PG = { q: Record<string, string>; page: Record<string, number>; size: number };
const usePG = create<PG>(() => ({ q: {}, page: {}, size: storedSize() }));

export function useFilter(key: string): [string, (v: string) => void] {
  const q = usePG((s) => s.q[key] ?? "");
  const set = (v: string) => usePG.setState((s) => ({ q: { ...s.q, [key]: v }, page: { ...s.page, [key]: 0 } }));
  return [q, set];
}

export function filterItems<T>(q: string, items: T[], text: (x: T) => string): T[] {
  const t = q.trim().toLowerCase();
  if (!t) return items;
  const words = t.split(/\s+/);
  return items.filter((x) => {
    const s = text(x).toLowerCase();
    return words.every((w) => s.includes(w));
  });
}

export function FilterInput({ listKey, placeholder }: { listKey: string; placeholder: string }) {
  const [q, setQ] = useFilter(listKey);
  const [v, setV] = useState(q);
  const timer = useRef(0);
  useEffect(() => setV(q), [q]);
  return (
    <input
      className="input res-filter"
      type="search"
      placeholder={placeholder}
      aria-label={placeholder}
      spellCheck={false}
      autoComplete="off"
      value={v}
      onChange={(e) => {
        const nv = e.target.value;
        setV(nv);
        clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setQ(nv), 200);
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape" && v) {
          e.preventDefault();
          setV("");
          setQ("");
        }
      }}
    />
  );
}

// usePage gives the items of the current page and the bar (null when
// everything fits in one page).
export function usePage<T>(key: string, items: T[]): { items: T[]; bar: React.ReactNode } {
  const per = usePG((s) => s.size);
  const cur = usePG((s) => s.page[key] ?? 0);
  const pages = Math.max(1, Math.ceil(items.length / per));
  const page = Math.min(cur, pages - 1);
  const slice = items.slice(page * per, page * per + per);
  if (items.length <= PAGE_SIZES[0]) return { items: slice, bar: null };
  const go = (p: number) => usePG.setState((s) => ({ page: { ...s.page, [key]: Math.max(0, Math.min(pages - 1, p)) } }));
  const near = [...new Set([0, pages - 1, page - 1, page, page + 1].filter((p) => p >= 0 && p < pages))].sort((a, b) => a - b);
  const nums: React.ReactNode[] = [];
  let last = -1;
  for (const p of near) {
    if (last >= 0 && p - last > 1) nums.push(<span key={"g" + p} className="pg-gap">…</span>);
    nums.push(
      <button key={p} type="button" className={"pg-btn" + (p === page ? " sel" : "")} aria-current={p === page ? "page" : undefined} onClick={() => go(p)}>
        {p + 1}
      </button>,
    );
    last = p;
  }
  const bar = (
    <div className="pager">
      <span className="pg-info">{items.length ? t("{from}–{to} de {total}", { from: page * per + 1, to: Math.min(items.length, (page + 1) * per), total: items.length }) : t("0 itens")}</span>
      <label className="pg-size">
        {t("Por página")}{" "}
        <select
          aria-label={t("Itens por página")}
          value={per}
          onChange={(e) => {
            const n = Number(e.target.value);
            try {
              localStorage.setItem("gantry.pageSize", String(n));
            } catch {
              /* no storage */
            }
            usePG.setState({ size: n, page: {} });
          }}
        >
          {PAGE_SIZES.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      </label>
      <span className="grow" />
      {pages > 1 ? (
        <nav className="pg-nav" aria-label={t("Páginas")}>
          <button type="button" className="pg-btn" disabled={page === 0} aria-label={t("Página anterior")} onClick={() => go(page - 1)}>
            ‹
          </button>
          {nums}
          <button type="button" className="pg-btn" disabled={page === pages - 1} aria-label={t("Próxima página")} onClick={() => go(page + 1)}>
            ›
          </button>
        </nav>
      ) : null}
    </div>
  );
  return { items: slice, bar };
}

// Sorting of a table by a column, remembered per list (localStorage key).
export type Sort = { col: string; dir: 1 | -1 };

export function useSort(storeKey: string, def: Sort, cols: string[]): [Sort, (col: string) => void] {
  const read = (): Sort => {
    try {
      const v = JSON.parse(localStorage.getItem("gantry." + storeKey) || "null");
      if (v && typeof v.col === "string" && (v.dir === 1 || v.dir === -1)) return v;
    } catch {
      /* nothing */
    }
    return def;
  };
  const [sort, setSort] = useState<Sort>(read);
  const cur = cols.includes(sort.col) ? sort : def;
  const by = (col: string) => {
    const next: Sort = { col, dir: cur.col === col ? (-cur.dir as 1 | -1) : 1 };
    setSort(next);
    try {
      localStorage.setItem("gantry." + storeKey, JSON.stringify(next));
    } catch {
      /* no storage */
    }
  };
  return [cur, by];
}

export function SortTh({ col, sort, onSort }: { col: string; sort: Sort; onSort: (c: string) => void }) {
  const on = sort.col === col;
  return (
    <th aria-sort={on ? (sort.dir > 0 ? "ascending" : "descending") : "none"}>
      <button type="button" className={"th-sort" + (on ? " on" : "")} title={t("Ordenar por {col}", { col: col.toLowerCase() })} onClick={() => onSort(col)}>
        {col}
        <span className="th-arrow" aria-hidden="true">
          {on ? (sort.dir > 0 ? "▲" : "▼") : "↕"}
        </span>
      </button>
    </th>
  );
}
