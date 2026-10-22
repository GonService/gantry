// Editable lists of rows (ports, volumes, labels…) and the env editor
// that also takes a .env text.
import { useState } from "react";
import { t } from "@/lib/i18n";

export type Col = { ph?: string; label: string; w?: string; list?: string; options?: [string, string][] };
export type Row = string[];

export function RowsEditor({ rows, cols, addLabel, onChange }: { rows: Row[]; cols: Col[]; addLabel: string; onChange: (rows: Row[]) => void }) {
  const set = (ri: number, ci: number, v: string) => onChange(rows.map((r, i) => (i === ri ? r.map((x, j) => (j === ci ? v : x)) : r)));
  return (
    <div className="rows-ed">
      {rows.map((row, ri) => (
        <div className="rows-ed-row" key={ri}>
          {cols.map((c, ci) =>
            c.options ? (
              <select key={ci} aria-label={c.label} style={c.w ? { flex: c.w } : undefined} value={row[ci] ?? ""} onChange={(e) => set(ri, ci, e.target.value)}>
                {c.options.map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </select>
            ) : (
              <input
                key={ci}
                value={row[ci] || ""}
                placeholder={c.ph || ""}
                aria-label={c.label}
                spellCheck={false}
                list={c.list}
                className="mono-input"
                style={c.w ? { flex: c.w } : undefined}
                onChange={(e) => set(ri, ci, e.target.value)}
              />
            ),
          )}
          <button type="button" className="btn small btn-ghost" title={t("Remover")} aria-label={t("Remover linha")} onClick={() => onChange(rows.filter((_, i) => i !== ri))}>
            ×
          </button>
        </div>
      ))}
      <button type="button" className="btn small" onClick={() => onChange([...rows, cols.map((c) => (c.options ? c.options[0][0] : ""))])}>
        + {addLabel}
      </button>
    </div>
  );
}

const lines = (s: string) =>
  (s || "")
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);

export const envRows = (list: string[]): Row[] =>
  (list || []).map((e) => {
    const i = e.indexOf("=");
    return i < 0 ? [e, ""] : [e.slice(0, i), e.slice(i + 1)];
  });

// EnvEditor: rows of KEY=value, or the same as text (like a .env file).
export function EnvEditor({ rows, onChange }: { rows: Row[]; onChange: (rows: Row[]) => void }) {
  const [text, setText] = useState<string | null>(null);
  return (
    <div>
      <div className="sub-tools">
        <button
          type="button"
          className="btn small btn-ghost"
          onClick={() => {
            if (text != null) {
              onChange(envRows(lines(text).filter((l) => !l.startsWith("#"))));
              setText(null);
            } else setText(rows.map(([k, v]) => k + "=" + v).join("\n"));
          }}
        >
          {text != null ? t("Voltar às linhas") : t("Editar como texto (.env)")}
        </button>
      </div>
      {text != null ? (
        <textarea
          rows={Math.max(6, rows.length + 2)}
          className="mono-input"
          spellCheck={false}
          placeholder={"TZ=America/Sao_Paulo\nDEBUG=1"}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            onChange(envRows(lines(e.target.value).filter((l) => !l.startsWith("#"))));
          }}
        />
      ) : (
        <RowsEditor
          rows={rows}
          cols={[
            { ph: t("NOME"), label: t("Nome"), w: "1" },
            { ph: t("valor"), label: t("Valor"), w: "2" },
          ]}
          addLabel={t("variável")}
          onChange={onChange}
        />
      )}
    </div>
  );
}
