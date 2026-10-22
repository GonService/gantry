// Short notices at the bottom right, and the viewer's preferences kept in
// this browser (gantry.* in localStorage). Errors stay until closed, so a
// long message can be read and copied; the others go away on their own.
import { create } from "zustand";
import { onToast } from "@/lib/store";
import { t as tr } from "@/lib/i18n";

type Toast = { id: number; msg: string; kind: string };
const useToasts = create<{ list: Toast[] }>(() => ({ list: [] }));
let seq = 0;

export function toast(msg: string, kind: "ok" | "err" | "" = "") {
  // The same error again (a retry, a polling loop) does not stack.
  if (kind === "err" && useToasts.getState().list.some((x) => x.kind === "err" && x.msg === msg)) return;
  const id = ++seq;
  useToasts.setState((s) => ({ list: [...s.list, { id, msg, kind }] }));
  if (kind !== "err") setTimeout(() => dismiss(id), 3500);
}

const dismiss = (id: number) => useToasts.setState((s) => ({ list: s.list.filter((t) => t.id !== id) }));
onToast(toast);

export function Toasts() {
  const list = useToasts((s) => s.list);
  return (
    <div className="toasts" id="toasts" aria-live="polite">
      {list.map((t) => (
        <div key={t.id} className={"toast " + t.kind} role={t.kind === "err" ? "alert" : "status"}>
          <span className="toast-msg">{t.msg}</span>
          {t.kind === "err" ? (
            <button type="button" className="toast-close" aria-label={tr("Fechar")} title={tr("Fechar")} onClick={() => dismiss(t.id)}>
              ✕
            </button>
          ) : null}
        </div>
      ))}
    </div>
  );
}

export const localStore = {
  get<T>(k: string, d: T): T {
    try {
      const v = localStorage.getItem("gantry." + k);
      return v == null ? d : (JSON.parse(v) as T);
    } catch {
      return d;
    }
  },
  set(k: string, v: unknown) {
    try {
      localStorage.setItem("gantry." + k, JSON.stringify(v));
    } catch {
      /* no storage */
    }
  },
};
