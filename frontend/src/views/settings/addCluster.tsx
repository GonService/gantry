import { useEffect, useState } from "react";
import { APIError, api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { Modal, confirm, openDialog } from "@/components/dialog";
import { kubeHref } from "@/views/kube/types";
import { folderOptions, type Groups } from "./folders";

type Ctx = { name: string; server: string; auth: string; current?: boolean; added?: boolean; exec?: string };

function AddCluster({ groups, close }: { groups: Groups; close: () => void }) {
  const [source, setSource] = useState<"arquivo" | "colar">("arquivo");
  const [path, setPath] = useState("");
  const [paste, setPaste] = useState("");
  const [list, setList] = useState<Ctx[] | null>(null);
  const [reading, setReading] = useState(false);
  const [picked, setPicked] = useState<Ctx | null>(null);
  const [name, setName] = useState("");
  const [mode, setMode] = useState("livre");
  const [folder, setFolder] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const load = async (src = source, p = path) => {
    setErr("");
    setReading(true);
    setPicked(null);
    try {
      const r = await api<{ path: string; contexts: Ctx[] | null }>("/kubeconfig", { method: "POST", body: src === "colar" ? { yaml: paste } : { path: p.trim() } });
      if (src === "arquivo") setPath(r.path);
      setList(r.contexts ?? []);
    } catch (e) {
      setList(null);
      setErr((e as Error).message);
    } finally {
      setReading(false);
    }
  };
  useEffect(() => {
    void load("arquivo", "");
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const add = async () => {
    setErr("");
    if (!picked) {
      setErr(t("Escolha um contexto."));
      return;
    }
    setBusy(true);
    try {
      const body: Record<string, unknown> = { context: picked.name, name: name.trim(), mode, folder };
      if (source === "colar") body.yaml = paste;
      else body.path = path.trim();
      let c: { id: string };
      try {
        c = await api("/clusters", { method: "POST", body });
      } catch (e) {
        if (!(e instanceof APIError) || e.status !== 412) throw e;
        if (!(await confirm(t("Confiar neste kubeconfig?"), t("{msg}. Só continue se o kubeconfig veio de uma fonte sua (o gcloud, o seu provedor).", { msg: e.message }), t("Confio")))) throw new Error(t("Cancelado."));
        c = await api("/clusters", { method: "POST", body: { ...body, trust: true } });
      }
      close();
      await gantry.loadClusters();
      await gantry.loadGroups();
      location.hash = kubeHref(c.id);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={t("Adicionar cluster")}
      wide
      onClose={close}
      actions={
        <>
          <button className="btn" type="button" onClick={close}>
            {t("Cancelar")}
          </button>
          <button className="btn primary" type="button" disabled={busy} onClick={add}>
            {busy ? (
              <>
                <span className="spin" />
                {t("Conectando…")}
              </>
            ) : (
              t("Adicionar")
            )}
          </button>
        </>
      }
    >
      <p className="hint" style={{ marginTop: 0 }}>
        {t("O Gantry usa as credenciais do kubeconfig (certificado, token ou plugins como o gke-gcloud-auth-plugin). Do seu arquivo, ele só lê: um token renovado pelo gcloud vale na hora.")}
      </p>
      <div className="seg">
        {(
          [
            ["arquivo", t("Do meu kubeconfig")],
            ["colar", t("Colar kubeconfig")],
          ] as const
        ).map(([v, l]) => (
          <button
            key={v}
            type="button"
            className={"seg-btn" + (source === v ? " sel" : "")}
            onClick={() => {
              setSource(v);
              setList(null);
              setPicked(null);
            }}
          >
            {l}
          </button>
        ))}
      </div>
      {source === "arquivo" ? (
        <div className="input-pick">
          <input className="mono" spellCheck={false} placeholder="~/.kube/config" value={path} onChange={(e) => setPath(e.target.value)} />
          <button
            type="button"
            className="btn small"
            onClick={async () => {
              const p = await gantry.pickPath({ kind: "abrir", title: t("Arquivo kubeconfig"), dir: "~/.kube" });
              if (p) {
                setPath(p);
                void load("arquivo", p);
              }
            }}
          >
            {t("Escolher…")}
          </button>
          <button type="button" className="btn small" onClick={() => load()}>
            {t("Ler")}
          </button>
        </div>
      ) : (
        <div>
          <textarea className="mono-input" rows={6} spellCheck={false} placeholder={"apiVersion: v1\nkind: Config\nclusters: …"} value={paste} onChange={(e) => setPaste(e.target.value)} />
          <button type="button" className="btn small" style={{ marginTop: 6 }} onClick={() => load()}>
            {t("Ler contextos")}
          </button>
        </div>
      )}
      {reading ? (
        <span className="muted small">
          <span className="spin" /> {t("lendo…")}
        </span>
      ) : list ? (
        <div className="ctx-list">
          {list.map((x) => (
            <label key={x.name} className={"ctx-item" + (x.added ? " muted" : "")}>
              <input
                type="radio"
                name="ctx"
                disabled={x.added}
                checked={picked?.name === x.name}
                onChange={() => {
                  setPicked(x);
                  if (!name) setName(x.name);
                }}
              />
              <span>
                <b>{x.name}</b>
                {x.current ? (
                  <span className="badge accent" style={{ marginLeft: 6 }}>
                    {t("atual")}
                  </span>
                ) : null}
                {x.added ? t(" (já adicionado)") : ""}
                <div className="muted small mono">{x.server + " · " + x.auth}</div>
                {x.exec ? (
                  <div className="small" style={{ color: "var(--warn)" }}>
                    {t("roda: {cmd}", { cmd: x.exec })}
                  </div>
                ) : null}
              </span>
            </label>
          ))}
        </div>
      ) : null}
      <div className="field">
        <label>{t("Nome")}</label>
        <input placeholder={t("nome na barra lateral")} value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="field">
        <label>{t("Modo")}</label>
        <select value={mode} onChange={(e) => setMode(e.target.value)}>
          <option value="livre">{t("Normal")}</option>
          <option value="leitura">{t("Só leitura (nada muda no cluster, sem shell, sem valores de Secrets)")}</option>
        </select>
      </div>
      {groups.folders.length ? (
        <div className="field">
          <label>{t("Pasta")}</label>
          <select value={folder} onChange={(e) => setFolder(e.target.value)}>
            {folderOptions(groups).map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </div>
      ) : null}
      {err ? <p className="flash flash-err">{err}</p> : null}
    </Modal>
  );
}

export function addClusterDialog(groups: Groups) {
  openDialog((close) => <AddCluster groups={groups} close={close} />);
}
