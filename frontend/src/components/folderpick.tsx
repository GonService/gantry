// The folder picker inside the app: browses the folders of a machine (this
// computer or a server over SSH), creates folders and returns the chosen
// one. The native dialog only sees this computer.
import { useEffect, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { t } from "@/lib/i18n";
import { localStore } from "@/shell/toasts";
import { Modal, openDialog } from "./dialog";

type Folder = { path: string; home: string; parent: string; writable: boolean; dirs: string[]; asked?: string };

const join = (dir: string, name: string) => (dir.endsWith("/") ? dir : dir + "/") + name;

// pickFolder resolves to the chosen folder, or null when canceled.
export function pickFolder(host: string, title: string, start: string): Promise<string | null> {
  return new Promise((resolve) => {
    let done = false;
    openDialog(
      (close) => (
        <FolderPicker
          host={host}
          title={title}
          start={start}
          onPick={(p) => {
            done = true;
            resolve(p);
            close();
          }}
          onClose={close}
        />
      ),
      () => {
        if (!done) resolve(null);
      },
    );
  });
}

function FolderPicker({ host, title, start, onPick, onClose }: { host: string; title: string; start: string; onPick: (p: string) => void; onClose: () => void }) {
  const [f, setF] = useState<Folder | null>(null);
  const [typed, setTyped] = useState(start || "~");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [hidden, setHidden] = useState(() => localStore.get("pastas.ocultas", false));
  const [newName, setNewName] = useState<string | null>(null);
  const newRef = useRef<HTMLInputElement>(null);
  const base = "/hosts/" + enc(host) + "/pastas";

  const go = async (path: string) => {
    setBusy(true);
    try {
      const r = await api<Folder>(base + "?path=" + enc(path));
      setF(r);
      setTyped(r.asked || r.path);
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const mkdir = async (path: string, use = false) => {
    setBusy(true);
    try {
      const r = await api<Folder>(base, { method: "POST", body: { path } });
      if (use) return onPick(r.path);
      setF(r);
      setTyped(r.path);
      setErr("");
      setNewName(null);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    void go(start || "~");
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (newName !== null) newRef.current?.focus();
  }, [newName]);

  const dirs = (f?.dirs ?? []).filter((d) => hidden || !d.startsWith("."));
  // A path typed that does not exist yet: offer to create it.
  const missing = f?.asked && f.asked !== f.path ? f.asked : "";
  return (
    <Modal
      title={title}
      wide
      onClose={onClose}
      className="folder-picker"
      actions={
        <>
          <button className="btn btn-ghost" type="button" onClick={onClose}>
            {t("Cancelar")}
          </button>
          {missing ? (
            <button className="btn primary" type="button" disabled={busy} onClick={() => void mkdir(missing, true)}>
              {t("Criar e usar esta pasta")}
            </button>
          ) : (
            <button className="btn primary" type="button" disabled={busy || !f} onClick={() => f && onPick(f.path)}>
              {t("Usar esta pasta")}
            </button>
          )}
        </>
      }
    >
      <form
        className="fp-bar"
        onSubmit={(e) => {
          e.preventDefault();
          void go(typed.trim() || "~");
        }}
      >
        <button className="btn small" type="button" disabled={busy || !f?.parent} onClick={() => f && void go(f.parent)} title={t("Pasta de cima")} aria-label={t("Pasta de cima")}>
          ↑
        </button>
        <button className="btn small" type="button" disabled={busy} onClick={() => void go("~")} title={f?.home || "~"}>
          {t("Início")}
        </button>
        <input className="input mono" spellCheck={false} value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={t("Caminho")} />
        <button className="btn small" type="submit" disabled={busy}>
          {t("Ir")}
        </button>
      </form>
      {missing ? <p className="hint">{t("{path} ainda não existe. A lista mostra a pasta mais próxima que existe.", { path: missing })}</p> : null}
      {err ? <p className="flash flash-err">{err}</p> : null}
      <div className="fp-list" role="listbox" aria-label={t("Pastas")}>
        {!f ? (
          <p className="muted small">{t("Carregando…")}</p>
        ) : !dirs.length ? (
          <p className="muted small">{t("Nenhuma pasta aqui.")}</p>
        ) : (
          dirs.map((d) => (
            <button key={d} type="button" className="fp-item" role="option" aria-selected={false} disabled={busy} onClick={() => void go(join(f.path, d))}>
              <span className="fp-icon" aria-hidden>
                📁
              </span>
              <span className="mono">{d}</span>
            </button>
          ))
        )}
      </div>
      <div className="fp-foot">
        <label className="small">
          <input
            type="checkbox"
            checked={hidden}
            onChange={(e) => {
              setHidden(e.target.checked);
              localStore.set("pastas.ocultas", e.target.checked);
            }}
          />{" "}
          {t("Mostrar pastas ocultas")}
        </label>
        <span className="grow" />
        {newName === null ? (
          <button className="btn small" type="button" disabled={busy || !f?.writable} title={f && !f.writable ? t("Sem permissão para criar pastas aqui") : undefined} onClick={() => setNewName("")}>
            {t("+ Nova pasta")}
          </button>
        ) : (
          <form
            className="fp-new"
            onSubmit={(e) => {
              e.preventDefault();
              const n = newName.trim();
              if (!n || n.includes("/") || !f) return;
              void mkdir(join(f.path, n));
            }}
          >
            <input ref={newRef} className="input mono" spellCheck={false} placeholder={t("nome-da-pasta")} value={newName} onChange={(e) => setNewName(e.target.value)} aria-label={t("Nome da nova pasta")} />
            <button className="btn small primary" type="submit" disabled={busy || !newName.trim() || newName.includes("/")}>
              {t("Criar")}
            </button>
            <button className="btn small btn-ghost" type="button" onClick={() => setNewName(null)}>
              {t("Cancelar")}
            </button>
          </form>
        )}
      </div>
      {f ? <p className="muted small mono fp-host">{host + ": " + f.path}</p> : null}
    </Modal>
  );
}
