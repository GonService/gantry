import { useEffect, useRef, useState } from "react";
import { gantry } from "@/lib/gantry";
import { Chev } from "@/components/icons";
import { t } from "@/lib/i18n";

// The namespace of the cluster page: a button that opens a search (as in
// Lens), with the last ones used on top. A <details>, so the automatic
// refresh waits while it is open.
export function NSPicker({ clusterId, names, cur, pick }: { clusterId: string; names: string[]; cur: string; pick: (v: string) => void }) {
  const recentKey = "kube-ns-recentes." + clusterId;
  const box = useRef<HTMLDetailsElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [hi, setHi] = useState(0);
  const [toLeft, setToLeft] = useState(false);

  const recent = gantry.store.get<string[]>(recentKey, []).filter((x) => names.includes(x));
  const rest = names.filter((x) => !recent.includes(x)).sort();
  const qq = q.trim().toLowerCase();
  const items: { v: string; label: string; group?: string }[] = [];
  if (!qq) items.push({ v: "", label: t("Todos os namespaces") });
  for (const [arr, group] of [
    [recent, "recentes"],
    [rest, "todos"],
  ] as const)
    arr.filter((x) => !qq || x.toLowerCase().includes(qq)).forEach((x) => items.push({ v: x, label: x, group }));
  const hiN = Math.min(hi, Math.max(items.length - 1, 0));

  const choose = (v: string) => {
    if (box.current) box.current.open = false;
    if (v) gantry.store.set(recentKey, [v, ...gantry.store.get<string[]>(recentKey, []).filter((x) => x !== v)].slice(0, 5));
    if (v !== cur) pick(v);
  };

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) box.current.open = false;
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);
  useEffect(() => {
    const el = list.current?.querySelectorAll(".ns-item")[hiN];
    el?.scrollIntoView({ block: "nearest" });
  }, [hiN, open, q]);

  let lastGroup = "";
  return (
    <details
      className="ns-picker"
      ref={box}
      onToggle={() => {
        const now = !!box.current?.open;
        setOpen(now);
        if (!now) return;
        // Open toward the side with room: the sidebar on the left would cover it.
        const r = box.current!.getBoundingClientRect();
        const main = (box.current!.closest("main") ?? document.body).getBoundingClientRect();
        setToLeft(r.left + 340 > Math.min(window.innerWidth, main.right));
        setQ("");
        setHi(0);
        setTimeout(() => input.current?.focus(), 0);
      }}
    >
      <summary className="select ns-current" title={t("Namespace (clique para buscar)")}>
        <span className="muted small">Namespace</span>
        <b>{cur || t("Todos")}</b>
        <Chev />
      </summary>
      <div className={"ns-pop" + (toLeft ? " to-left" : "")}>
        <input
          ref={input}
          className="input"
          type="search"
          placeholder={t("Buscar namespace…")}
          spellCheck={false}
          aria-label={t("Buscar namespace")}
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setHi(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setHi(Math.min(hiN + 1, items.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setHi(Math.max(hiN - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              if (items[hiN]) choose(items[hiN].v);
            } else if (e.key === "Escape") {
              e.preventDefault();
              if (box.current) box.current.open = false;
            }
          }}
        />
        <div className="ns-list" role="listbox" ref={list}>
          {items.length ? (
            items.map((it, i) => {
              const head = !qq && it.group && it.group !== lastGroup && recent.length ? (
                <div className="ns-group" key={"g" + it.group}>
                  {it.group === "recentes" ? t("Usados por último") : t("Todos")}
                </div>
              ) : null;
              lastGroup = it.group || lastGroup;
              return [
                head,
                <button
                  key={it.v || "*"}
                  type="button"
                  role="option"
                  className={"ns-item" + (i === hiN ? " hi" : "") + (it.v === cur ? " cur" : "")}
                  aria-selected={it.v === cur}
                  onMouseEnter={() => setHi(i)}
                  onClick={() => choose(it.v)}
                >
                  {it.label}
                  {it.v === cur ? <span className="ns-check">✓</span> : null}
                </button>,
              ];
            })
          ) : (
            <div className="muted small" style={{ padding: "8px 10px" }}>
              {t("Nenhum namespace com \"{q}\".", { q: q.trim() })}
            </div>
          )}
        </div>
      </div>
    </details>
  );
}
