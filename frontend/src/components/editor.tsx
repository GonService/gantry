import { useEffect, useRef, useState } from "react";
import type { EditorHandle, Problem } from "./monaco";

// What a check of the cluster (or of anything beyond the YAML) finds: the
// document and the field; the editor puts it on its line.
export type FieldProblem = { doc: number; field?: string; message: string };
export type { Problem };
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { api } from "@/lib/api";
import { pickFolder } from "./folderpick";

// CodeEditor: Monaco (the editor of VS Code and Lens), with search
// (Cmd/Ctrl+F), replace, folding and the minimap; Cmd/Ctrl+S calls onSave.
// It loads on the first use; if it cannot, a plain text box takes its place.
let monacoP: Promise<typeof import("./monaco")> | null = null;
const loadMonaco = () => (monacoP ??= import("./monaco"));

export function CodeEditor({
  value,
  onChange,
  readOnly,
  label = "YAML",
  onSave,
  placeholder,
  language,
  file,
  tall,
  lint,
  onProblems,
  handle,
  fill,
}: {
  value: string;
  onChange: (v: string) => void;
  readOnly?: boolean;
  label?: string;
  onSave?: () => void;
  placeholder?: string;
  bare?: boolean;
  language?: string; // yaml when neither this nor file says otherwise
  file?: string; // the language comes from the file name
  tall?: boolean; // as tall as the window allows (dialogs of a cluster)
  // lint: checks beyond the YAML syntax (the cluster's dry run), after a
  // pause in the typing; onProblems gets null while it runs.
  lint?: (text: string) => Promise<FieldProblem[]>;
  onProblems?: (p: Problem[] | null) => void;
  handle?: React.MutableRefObject<EditorHandle | null>;
  fill?: boolean; // fills the space of its parent (a tab of the bottom panel)
}) {
  const box = useRef<HTMLDivElement>(null);
  const ed = useRef<EditorHandle | null>(null);
  const cb = useRef({ onChange, onSave, lint, onProblems });
  cb.current = { onChange, onSave, lint, onProblems };
  const [failed, setFailed] = useState(false);
  const [ready, setReady] = useState(false);
  const lang = (m: typeof import("./monaco")) => language || (file ? m.languageOf(file) : "yaml");
  useEffect(() => {
    let gone = false;
    loadMonaco()
      .then((m) => {
        if (gone || !box.current) return;
        ed.current = m.create(box.current, {
          value,
          language: lang(m),
          readOnly,
          label,
          onChange: (v) => cb.current.onChange(v),
          onSave: () => cb.current.onSave?.(),
        });
        if (handle) handle.current = ed.current;
        setReady(true);
      })
      .catch(() => setFailed(true));
    return () => {
      gone = true;
      ed.current?.dispose();
      ed.current = null;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (ed.current && ed.current.getValue() !== value) ed.current.setValue(value);
  }, [value, ready]);
  useEffect(() => {
    ed.current?.setReadOnly(!!readOnly);
  }, [readOnly, ready]);
  // The check: the YAML at once, the cluster after a pause in the typing.
  const run = useRef(0);
  useEffect(() => {
    const e = ed.current;
    if (!e || !monacoP) return;
    const isYaml = !(language && language !== "yaml") && !(file && !/\.ya?ml$/i.test(file));
    if (!isYaml) return;
    const n = ++run.current;
    const tm = setTimeout(async () => {
      const m = await monacoP!;
      if (n !== run.current || !ed.current) return;
      const syntax = value.trim() ? m.yamlSyntax(value) : [];
      if (syntax.length || !cb.current.lint || !value.trim()) {
        ed.current.setProblems(syntax);
        cb.current.onProblems?.(syntax);
        return;
      }
      ed.current.setProblems([]);
      cb.current.onProblems?.(null);
      let found: FieldProblem[] = [];
      try {
        found = await cb.current.lint(value);
      } catch (err) {
        found = [{ doc: 0, message: (err as Error).message }];
      }
      if (n !== run.current || !ed.current) return;
      const list: Problem[] = found.map((f) => {
        const at = m.fieldLine(value, f.doc, f.field);
        return { line: at.line, col: at.col, endCol: at.endCol, message: f.field ? f.field + ": " + f.message.replace(f.field + ": ", "") : f.message };
      });
      ed.current.setProblems(list);
      cb.current.onProblems?.(list);
    }, 700);
    return () => clearTimeout(tm);
  }, [value, ready, language, file]);
  useEffect(() => {
    if (ed.current && monacoP) void monacoP.then((m) => ed.current?.setLanguage(lang(m)));
  }, [language, file, ready]); // eslint-disable-line react-hooks/exhaustive-deps
  if (failed) return <PlainEditor value={value} onChange={onChange} readOnly={readOnly} label={label} onSave={onSave} placeholder={placeholder} />;
  return (
    <div className={"editor-body monaco-host" + (tall ? " tall" : "") + (fill ? " fill" : "")}>
      <div className="monaco-box" ref={box} />
      {!ready ? <div className="monaco-wait muted small">{t("Abrindo o editor…")}</div> : null}
      {ready && !value && placeholder ? <pre className="monaco-placeholder">{placeholder}</pre> : null}
    </div>
  );
}

// PlainEditor: a textarea with line numbers (when Monaco does not load).
function PlainEditor({ value, onChange, readOnly, label, onSave, placeholder }: { value: string; onChange: (v: string) => void; readOnly?: boolean; label?: string; onSave?: () => void; placeholder?: string }) {
  const gutter = useRef<HTMLPreElement>(null);
  const area = useRef<HTMLTextAreaElement>(null);
  const n = value.split("\n").length;
  let lines = "";
  for (let i = 1; i <= n; i++) lines += i + "\n";
  useEffect(() => {
    if (gutter.current && area.current) gutter.current.scrollTop = area.current.scrollTop;
  });
  return (
    <div className="editor-body">
      <pre className="gutter" aria-hidden="true" ref={gutter}>
        {lines}
      </pre>
      <textarea
        ref={area}
        className="code"
        spellCheck={false}
        autoComplete="off"
        readOnly={readOnly}
        placeholder={placeholder}
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onScroll={(e) => {
          if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
        }}
        onKeyDown={(e) => {
          if (e.key === "Tab" && !e.shiftKey && !readOnly) {
            e.preventDefault();
            const el = e.currentTarget;
            const a = el.selectionStart,
              b = el.selectionEnd;
            const next = el.value.slice(0, a) + "  " + el.value.slice(b);
            onChange(next);
            requestAnimationFrame(() => {
              el.selectionStart = el.selectionEnd = a + 2;
            });
          }
          if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s" && onSave) {
            e.preventDefault();
            onSave();
          }
        }}
      />
    </div>
  );
}

// FolderInput: a folder field with Escolher…, which opens the system dialog
// when the host is this computer (servers have no dialog).
export function FolderInput({ value, onChange, host, title, placeholder }: { value: string; onChange: (v: string) => void; host: string; title: string; placeholder?: string }) {
  return (
    <div className="input-pick">
      <input spellCheck={false} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      <button
        type="button"
        className="btn small"
        onClick={async () => {
          const x = gantry.hostByName(host);
          if (x?.kind === "tcp") {
            gantry.toast(t("Este host não dá acesso às pastas da máquina: digite o caminho."), "err");
            return;
          }
          // This computer: the system's dialog, when there is one; else (a
          // server, a Linux without zenity) the app's own picker.
          if (x?.kind === "unix") {
            try {
              const r = await api<{ canceled?: boolean; path: string }>("/escolher", { method: "POST", body: { kind: "pasta", title, dir: (value || "~").replace(/\/[^/]*$/, "") || "~" } });
              if (!r.canceled && r.path) onChange(r.path);
              return;
            } catch {
              /* no native dialog: the app's picker below */
            }
          }
          const p = await pickFolder(host, title, value || "~");
          if (p) onChange(p);
        }}
      >
        {t("Escolher…")}
      </button>
    </div>
  );
}

export function useDebounced(fn: () => void, ms: number, deps: unknown[]) {
  useEffect(() => {
    const t = setTimeout(fn, ms);
    return () => clearTimeout(t);
  }, deps); // eslint-disable-line react-hooks/exhaustive-deps
}
