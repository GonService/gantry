// Dialogs: one host for the whole interface (mounted by the app), opened
// from anywhere with openDialog / confirm / formDialog / pickOption.
import { CodeEditor, FolderInput } from "./editor";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { create } from "zustand";
import { t } from "@/lib/i18n";

type Entry = { id: number; node: (close: () => void) => ReactNode; onClose: () => void };

const useDialogs = create<{ list: Entry[] }>(() => ({ list: [] }));
let seq = 0;

// openDialog shows what render returns; close() (or Esc) takes it away.
export function openDialog(render: (close: () => void) => ReactNode, onClose: () => void = () => {}): () => void {
  const id = ++seq;
  const close = () => {
    const cur = useDialogs.getState().list;
    if (!cur.some((e) => e.id === id)) return;
    useDialogs.setState({ list: cur.filter((e) => e.id !== id) });
    onClose();
  };
  useDialogs.setState((s) => ({ list: [...s.list, { id, node: render, onClose: close }] }));
  return close;
}

export function DialogHost() {
  const list = useDialogs((s) => s.list);
  return (
    <>
      {list.map((e) => (
        <DialogSlot key={e.id} entry={e} />
      ))}
    </>
  );
}

function DialogSlot({ entry }: { entry: Entry }) {
  return <>{entry.node(entry.onClose)}</>;
}

export function Modal({
  title,
  wide,
  onClose,
  children,
  actions,
  className,
  onSubmit,
}: {
  title: ReactNode;
  wide?: boolean;
  onClose: () => void;
  children: ReactNode;
  actions?: ReactNode;
  className?: string;
  // a form: Enter submits (the padding comes from the form, as in the CSS)
  onSubmit?: (e: React.FormEvent) => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
    const onCancel = (e: Event) => {
      e.preventDefault();
      onClose();
    };
    d?.addEventListener("cancel", onCancel);
    return () => d?.removeEventListener("cancel", onCancel);
  }, [onClose]);
  const inner = (
    <>
      <h1>{title}</h1>
      {children}
      {actions !== undefined ? <div className="dialog-actions">{actions}</div> : null}
    </>
  );
  return (
    <dialog ref={ref} className={["form-dialog", onSubmit ? "" : "padded", wide ? "wide-dialog" : "", className ?? ""].filter(Boolean).join(" ")}>
      {onSubmit ? <form onSubmit={onSubmit}>{inner}</form> : inner}
    </dialog>
  );
}

export function confirm(title: string, message: ReactNode, okLabel = t("Confirmar"), danger = false): Promise<boolean> {
  return new Promise((resolve) => {
    let answered = false;
    openDialog(
      (close) => (
        <Modal
          title={title}
          onClose={close}
          actions={
            <>
              <button className="btn btn-ghost" type="button" onClick={close}>
                {t("Cancelar")}
              </button>
              <button
                className={"btn " + (danger ? "danger-solid" : "primary")}
                type="button"
                autoFocus
                onClick={() => {
                  answered = true;
                  resolve(true);
                  close();
                }}
              >
                {okLabel}
              </button>
            </>
          }
        >
          <div className="ask-msg">{message}</div>
        </Modal>
      ),
      () => {
        if (!answered) resolve(false);
      },
    );
  });
}

export type Field = {
  id: string;
  label: string;
  type?: "text" | "select" | "textarea" | "code" | "checkbox" | "file" | "folder" | "password" | "number";
  accept?: string; // file
  host?: string; // folder: the host where the folder is
  value?: string | boolean;
  options?: [string, string][];
  placeholder?: string;
  hint?: string;
  optional?: boolean;
  rows?: number;
};

export type FormValues = Record<string, string | boolean | File | null>;
// derive: when a field changes, the form may change others (a name that
// follows a choice).
type Derive = (id: string, v: FormValues[string], vals: FormValues) => FormValues | null;

function FormDialog({
  title,
  intro,
  fields,
  submit,
  onSubmit,
  derive,
  close,
}: {
  title: string;
  intro?: ReactNode;
  fields: Field[];
  submit: string;
  onSubmit: (v: FormValues) => Promise<void> | void;
  derive?: Derive;
  close: () => void;
}) {
  const [vals, setVals] = useState<FormValues>(() =>
    Object.fromEntries(fields.map((f) => [f.id, f.value ?? (f.type === "checkbox" ? false : f.type === "file" ? null : f.type === "select" ? (f.options?.[0]?.[0] ?? "") : "")])),
  );
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (id: string, v: FormValues[string]) =>
    setVals((s) => {
      const next = { ...s, [id]: v };
      return { ...next, ...(derive?.(id, v, next) ?? {}) };
    });
  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const out: FormValues = {};
      for (const [k, v] of Object.entries(vals)) out[k] = typeof v === "string" ? v.trim() : v;
      await onSubmit(out);
      close();
    } catch (x) {
      setErr(x instanceof Error ? x.message : String(x));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={title}
      wide={fields.some((f) => f.type === "code")}
      onClose={close}
      onSubmit={send}
      actions={
        <>
          <button type="button" className="btn btn-ghost" onClick={close}>
            {t("Cancelar")}
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            {busy ? (
              <>
                <span className="spin" />
                {submit}…
              </>
            ) : (
              submit
            )}
          </button>
        </>
      }
    >
      {intro ? (
        <p className="hint" style={{ marginTop: 0 }}>
          {intro}
        </p>
      ) : null}
      {fields.map((f, i) =>
        f.type === "checkbox" ? (
          <label className="check" key={f.id}>
            <input type="checkbox" checked={!!vals[f.id]} onChange={(e) => set(f.id, e.target.checked)} /> {f.label}
          </label>
        ) : (
          <div className="field" key={f.id}>
            <label htmlFor={"fd-" + f.id}>
              {f.label}
              {f.optional ? <span className="opt"> {t("opcional")}</span> : null}
            </label>
            {f.type === "select" ? (
              <select id={"fd-" + f.id} value={String(vals[f.id])} onChange={(e) => set(f.id, e.target.value)} autoFocus={i === 0}>
                {(f.options ?? []).map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </select>
            ) : f.type === "file" ? (
              <input id={"fd-" + f.id} type="file" accept={f.accept} onChange={(e) => set(f.id, e.target.files?.[0] ?? null)} autoFocus={i === 0} />
            ) : f.type === "folder" ? (
              <FolderInput value={String(vals[f.id] ?? "")} onChange={(v) => set(f.id, v)} host={f.host ?? ""} title={f.label} placeholder={f.placeholder} />
            ) : f.type === "code" ? (
              <CodeEditor value={String(vals[f.id] ?? "")} onChange={(v) => set(f.id, v)} label={f.label} placeholder={f.placeholder} />
            ) : f.type === "textarea" ? (
              <textarea
                id={"fd-" + f.id}
                rows={f.rows ?? 3}
                placeholder={f.placeholder}
                spellCheck={false}
                className="mono-input"
                value={String(vals[f.id])}
                onChange={(e) => set(f.id, e.target.value)}
                autoFocus={i === 0}
              />
            ) : (
              <input
                id={"fd-" + f.id}
                type={f.type === "password" || f.type === "number" ? f.type : "text"}
                placeholder={f.placeholder}
                autoComplete="off"
                spellCheck={false}
                value={String(vals[f.id])}
                onChange={(e) => set(f.id, e.target.value)}
                autoFocus={i === 0}
              />
            )}
            {f.hint ? <p className="hint">{f.hint}</p> : null}
          </div>
        ),
      )}
      {err ? <p className="flash flash-err">{err}</p> : null}
    </Modal>
  );
}

export function formDialog(opts: { title: string; intro?: ReactNode; fields: Field[]; submit: string; onSubmit: (v: FormValues) => Promise<void> | void; derive?: Derive; onClose?: () => void }): void {
  const { onClose, ...rest } = opts;
  openDialog((close) => <FormDialog {...rest} close={close} />, onClose);
}

// pickOption asks for one of the options; null when canceled. One option
// answers at once.
export function pickOption(list: string[], title: string, submit = t("Abrir")): Promise<string | null> {
  if (list.length <= 1) return Promise.resolve(list[0] ?? "");
  return new Promise((resolve) => {
    let done = false;
    openDialog(
      (close) => (
        <FormDialog
          title={title}
          submit={submit}
          fields={[{ id: "c", label: t("Opção"), type: "select", options: list.map((x) => [x, x]), value: list[0] }]}
          onSubmit={(v) => {
            done = true;
            resolve(String(v.c));
          }}
          close={close}
        />
      ),
      () => {
        if (!done) resolve(null);
      },
    );
  });
}
