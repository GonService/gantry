import { useEffect, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { b64ToBytes, copyText } from "@/lib/format";
import { t } from "@/lib/i18n";
import { Modal, confirm, formDialog, openDialog } from "@/components/dialog";
import { loadForwards, useKube } from "./store";
import type { Cluster, Row } from "./types";
import { applyEditor, yamlEditor } from "./yamlPane";

const kapi = <T,>(c: Cluster, path: string, opts: { method?: string; body?: unknown } = {}) => api<T>("/clusters/" + enc(c.id) + path, opts);

// The YAML of an object opens in the bottom panel (see yamlPane.tsx).
export function yamlDialog(c: Cluster, kind: string, r: Row, noUpdate = false, onSaved?: () => void) {
  yamlEditor(c, kind, r, noUpdate, onSaved);
}

export function secretDialog(c: Cluster, r: Row) {
  openDialog((close) => <SecretDialog c={c} r={r} close={close} />);
}

function SecretValue({ k, v }: { k: string; v: string }) {
  const [shown, setShown] = useState(false);
  return (
    <tr>
      <td className="mono">{k}</td>
      <td>
        <code className="secret-val">{shown ? v : "••••••••"}</code>
      </td>
      <td className="row-actions">
        <button className="btn small" type="button" onClick={() => setShown(!shown)}>
          {shown ? t("Esconder") : t("Mostrar")}
        </button>
        <button className="btn small" type="button" onClick={async () => gantry.toast((await copyText(v)) ? t("Copiado") : t("Não deu para copiar"), "ok")}>
          {t("Copiar")}
        </button>
      </td>
    </tr>
  );
}

function SecretDialog({ c, r, close }: { c: Cluster; r: Row; close: () => void }) {
  const [data, setData] = useState<Record<string, string> | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    kapi<Record<string, string>>(c, "/r/secrets/" + enc(r.namespace ?? "") + "/" + enc(r.name) + "/dados")
      .then(setData)
      .catch((e: Error) => setErr(e.message));
  }, [c, r]);
  const dec = (s: string) => {
    try {
      return new TextDecoder().decode(b64ToBytes(s));
    } catch {
      return s;
    }
  };
  return (
    <Modal
      title={"Secret " + r.name}
      wide
      onClose={close}
      actions={
        <button className="btn" type="button" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      {err ? (
        <p className="flash flash-err">{err}</p>
      ) : !data ? (
        <span className="muted small">
          <span className="spin" /> {t("carregando…")}
        </span>
      ) : Object.keys(data).length ? (
        <table className="table">
          <tbody>
            {Object.keys(data)
              .sort()
              .map((k) => (
                <SecretValue key={k} k={k} v={dec(data[k])} />
              ))}
          </tbody>
        </table>
      ) : (
        <p className="muted">{t("Secret vazio.")}</p>
      )}
    </Modal>
  );
}

export function applyDialog(c: Cluster, kind: string) {
  applyEditor(c, kind);
}

export function forwardDialog(c: Cluster, kind: string, ns: string, name: string, ports: number[]) {
  const pod = kind === "pods";
  const portLabel = pod ? t("Porta no pod") : t("Porta no Service");
  formDialog({
    title: t("Encaminhar porta de {name}", { name }),
    submit: t("Abrir"),
    fields: [
      ports.length > 1
        ? { id: "port", label: portLabel, type: "select", options: ports.map((p) => [String(p), String(p)] as [string, string]), value: String(ports[0]) }
        : { id: "port", label: portLabel, value: ports[0] ? String(ports[0]) : "", placeholder: t("ex.: 5432") },
      { id: "local", label: t("Porta nesta máquina"), optional: true, value: ports[0] ? String(ports[0]) : "", placeholder: t("em branco: uma livre"), hint: t("Só esta máquina (127.0.0.1) usa a porta, enquanto ela estiver aberta.") },
    ],
    onSubmit: async (v) => {
      const port = parseInt(String(v.port), 10);
      const local = v.local ? parseInt(String(v.local), 10) : 0;
      if (!(port > 0 && port < 65536)) throw new Error(pod ? t("Informe a porta do pod.") : t("Informe a porta do Service."));
      if (v.local && !(local > 0 && local < 65536)) throw new Error(t("Porta desta máquina inválida."));
      const f = await kapi<{ local: number }>(c, "/encaminhar", { method: "POST", body: { ns, kind, name, port, local } });
      const addr = "127.0.0.1:" + f.local;
      void copyText(addr);
      gantry.toast(addr + " → " + name + ":" + port + " " + t("(endereço copiado)"), "ok");
      void loadForwards(c.id);
    },
  });
}

export function forwardsDialog(c: Cluster) {
  openDialog((close) => <ForwardsDialog c={c} close={close} />);
}

function ForwardsDialog({ c, close }: { c: Cluster; close: () => void }) {
  const list = useKube((s) => s.forwards[c.id]) ?? [];
  return (
    <Modal
      title={t("Portas encaminhadas")}
      wide
      onClose={close}
      actions={
        <button className="btn" type="button" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      {list.length ? (
        <table className="table">
          <tbody>
            {list.map((f) => {
              const addr = "127.0.0.1:" + f.local;
              return (
                <tr key={f.id}>
                  <td>
                    <code>{addr}</code>{" "}
                    <button className="kd-copy" type="button" title={t("Copiar")} onClick={async () => (await copyText(addr)) && gantry.toast(t("Copiado"), "ok")}>
                      ⧉
                    </button>
                  </td>
                  <td>
                    → {f.kind === "services" ? "Service " : "pod "}
                    <b>{f.name}</b>:{f.port}
                    <div className="muted small">
                      {f.ns} · {t("{now} conexões agora, {total} ao todo", { now: f.conns, total: f.total })}
                    </div>
                    {f.lastError ? (
                      <div className="small" style={{ color: "var(--warn)" }}>
                        {f.lastError}
                      </div>
                    ) : null}
                  </td>
                  <td className="row-actions">
                    <button
                      className="btn small danger"
                      type="button"
                      onClick={async () => {
                        try {
                          await api("/encaminhamentos/" + enc(f.id), { method: "DELETE" });
                        } catch (e) {
                          gantry.toast((e as Error).message, "err");
                        }
                        await loadForwards(c.id);
                      }}
                    >
                      {t("Fechar")}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <p className="muted">{t("Nenhuma porta encaminhada.")}</p>
      )}
    </Modal>
  );
}

type DrainResult = { evicted: string[]; skipped: { pod: string; reason: string }[]; failed: { pod: string; reason: string }[] };

export async function drainNode(c: Cluster, node: string, after?: () => void) {
  const ok = await confirm(
    t("Esvaziar o nó {node}?", { node }),
    t("O nó deixa de receber pods e os pods dele são despejados para outros nós (respeitando os PodDisruptionBudgets). Pods de DaemonSet e pods sem dono ficam. Dados em emptyDir são perdidos. Depois, use Liberar para o nó voltar a receber pods."),
    t("Esvaziar"),
    true,
  );
  if (!ok) return;
  let r: { drain?: DrainResult };
  try {
    r = await kapi(c, "/r/nodes/-/" + enc(node) + "/esvaziar", { method: "POST", body: {} });
  } catch (e) {
    gantry.toast((e as Error).message, "err");
    return;
  }
  const d = r.drain ?? { evicted: [], skipped: [], failed: [] };
  openDialog((close) => (
    <Modal
      title={t("Nó {node} esvaziado", { node })}
      wide
      onClose={close}
      actions={
        <button className="btn" type="button" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      <p className="hint">{t("O nó está isolado. Os pods despejados terminam e são recriados em outros nós pelos seus donos.")}</p>
      {d.evicted.length ? (
        <>
          <h3 className="small">{t("Despejados")} · {d.evicted.length}</h3>
          <ul className="small">
            {d.evicted.map((x) => (
              <li key={x}>
                <code>{x}</code>
              </li>
            ))}
          </ul>
        </>
      ) : null}
      {d.failed.length ? (
        <>
          <h3 className="small">{t("Não puderam sair")} · {d.failed.length}</h3>
          <ul className="small">
            {d.failed.map((x) => (
              <li key={x.pod}>
                <code>{x.pod}</code> — {x.reason}
              </li>
            ))}
          </ul>
        </>
      ) : null}
      {d.skipped.length ? (
        <>
          <h3 className="small">{t("Ficaram")} · {d.skipped.length}</h3>
          <ul className="small">
            {d.skipped.map((x) => (
              <li key={x.pod}>
                <code>{x.pod}</code>
                <span className="muted"> — {x.reason}</span>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </Modal>
  ));
  after?.();
}
