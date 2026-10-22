import { useMemo, useRef, useState, type ReactNode } from "react";
import { t, tn } from "@/lib/i18n";
import { gantry } from "@/lib/gantry";

// Selecting several rows of a list to act on them at once (delete pods,
// stop containers…). A checkbox per row, one in the header for what is in
// view, Shift+click for a range, and a bar with the actions while there is
// something selected.

export type Selection = {
  selected: string[]; // the selected keys still in the list
  has: (k: string) => boolean;
  toggle: (k: string, range?: boolean) => void;
  setMany: (keys: string[], on: boolean) => void;
  clear: () => void;
};

// useSelection: all is every key of the list (what is gone is forgotten);
// order is the keys in view, in the order shown (for Shift+click).
export function useSelection(all: string[], order: string[] = all): Selection {
  const [sel, setSel] = useState<Set<string>>(() => new Set());
  const last = useRef<string | null>(null);
  const allSet = useMemo(() => new Set(all), [all.join("\n")]); // eslint-disable-line react-hooks/exhaustive-deps
  const selected = [...sel].filter((k) => allSet.has(k));
  return {
    selected,
    has: (k) => sel.has(k) && allSet.has(k),
    toggle: (k, range) => {
      // The row clicked before this one (read now: the update runs later).
      const prev = last.current;
      setSel((s) => {
        const n = new Set(s);
        const on = !n.has(k);
        if (range && prev && order.includes(prev) && order.includes(k)) {
          const [a, b] = [order.indexOf(prev), order.indexOf(k)].sort((x, y) => x - y);
          for (const x of order.slice(a, b + 1)) on ? n.add(x) : n.delete(x);
        } else if (on) n.add(k);
        else n.delete(k);
        return n;
      });
      last.current = k;
    },
    setMany: (keys, on) =>
      setSel((s) => {
        const n = new Set(s);
        for (const k of keys) on ? n.add(k) : n.delete(k);
        return n;
      }),
    clear: () => setSel(new Set()),
  };
}

export function SelectCell({ s, k, label }: { s: Selection; k: string; label: string }) {
  return (
    <td className="sel">
      <input
        type="checkbox"
        aria-label={t("Selecionar {name}", { name: label })}
        checked={s.has(k)}
        onChange={() => {}}
        onClick={(e) => {
          e.stopPropagation();
          s.toggle(k, e.shiftKey);
        }}
      />
    </td>
  );
}

// SelectAll: the header box, for the rows in view.
export function SelectAll({ s, keys, as = "th" }: { s: Selection; keys: string[]; as?: "th" | "span" }) {
  const n = keys.filter((k) => s.has(k)).length;
  const box = (
    <input
      type="checkbox"
      aria-label={t("Selecionar todos os itens em vista")}
      title={t("Selecionar todos os itens em vista")}
      checked={n > 0 && n === keys.length}
      ref={(el) => {
        if (el) el.indeterminate = n > 0 && n < keys.length;
      }}
      onChange={() => s.setMany(keys, n !== keys.length)}
    />
  );
  return as === "th" ? <th className="sel">{box}</th> : <span className="sel">{box}</span>;
}

// BulkBar: what to do with the selection; busy shows the progress.
export function BulkBar({ s, busy, children }: { s: Selection; busy?: string; children: ReactNode }) {
  if (!s.selected.length && !busy) return null;
  return (
    <div className="bulk-bar" role="toolbar" aria-label={t("Ações para os selecionados")}>
      <b>{tn(s.selected.length, "{n} selecionado", "{n} selecionados")}</b>
      {busy ? (
        <span className="muted small">
          <span className="spin" /> {busy}
        </span>
      ) : (
        <>
          {children}
          <button type="button" className="btn small btn-ghost" onClick={s.clear}>
            {t("Limpar seleção")}
          </button>
        </>
      )}
    </div>
  );
}

// runBulk runs fn for each item, a few at a time, telling the progress;
// it answers the failures (name and reason).
export async function runBulk<T>(items: T[], name: (x: T) => string, fn: (x: T) => Promise<void>, progress: (msg: string) => void, verb: string, parallel = 4): Promise<[string, string][]> {
  const fails: [string, string][] = [];
  let done = 0;
  const queue = [...items];
  progress(t("{verb} {done} de {total}…", { verb, done, total: items.length }));
  const worker = async () => {
    for (let x = queue.shift(); x !== undefined; x = queue.shift()) {
      try {
        await fn(x);
      } catch (e) {
        fails.push([name(x), (e as Error).message]);
      }
      done++;
      progress(t("{verb} {done} de {total}…", { verb, done, total: items.length }));
    }
  };
  await Promise.all(Array.from({ length: Math.min(parallel, items.length) }, worker));
  return fails;
}

// confirmMany: the names in the question, up to a few.
export function someNames(names: string[], max = 8): string {
  return names.length <= max ? names.join(", ") : t("{list} e mais {n}", { list: names.slice(0, max).join(", "), n: names.length - max });
}

// reportBulk: one toast for the whole (ok: the message when all went
// well); the failures go to the output panel.
export function reportBulk(total: number, fails: [string, string][], ok: string) {
  if (!fails.length) {
    gantry.toast(ok, "ok");
    return;
  }
  gantry.toast(t("{bad} de {total} falharam", { bad: fails.length, total }), "err");
  gantry.showOutput(t("Falhas"), { ok: false, output: fails.map(([n, e]) => n + ": " + e).join("\n") });
}
