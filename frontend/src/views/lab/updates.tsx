// New versions of the images of a project: a badge in the list of projects
// and the "Versões das imagens" block on the project page.
import { useEffect } from "react";
import { create } from "zustand";
import { hostByName } from "@/lib/store";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc } from "@/lib/app";
import { t } from "@/lib/i18n";

type Upd = { service: string; image: string; var?: string; current: string; minor?: string; major?: string; newer?: boolean; pinned?: boolean; checked?: boolean; error?: string };
type Entry = { items?: Upd[]; err?: string; at: number; loading?: boolean };

export const useUpdates = create<Record<string, Entry | null>>(() => ({}));

// updCount is how many images of a project have something new (from the
// last check; the list of projects asks in the background).
export const updCount = (e: Entry | null | undefined) => (e?.items ? e.items.filter((u) => u.minor || u.major || u.newer).length : 0);

export async function loadUpdates(host: string, id: string, force = false) {
  const k = host + "/" + id;
  const cur = useUpdates.getState()[k];
  if (cur?.loading) return;
  if (cur && !force && Date.now() - cur.at < 3600e3) return;
  useUpdates.setState({ [k]: { ...(cur ?? { at: 0 }), loading: true } });
  try {
    useUpdates.setState({ [k]: { items: await api<Upd[]>("/hosts/" + enc(host) + "/labs/" + enc(id) + "/atualizacoes" + (force ? "?force=1" : "")), at: Date.now() } });
  } catch (e) {
    useUpdates.setState({ [k]: { err: (e as Error).message, at: Date.now() } });
  }
}

// The checks of the list run one at a time: each asks registries and may
// take long, and the browser has few connections to the app.
const queue = new Set<string>();
let chain: Promise<void> = Promise.resolve();
export function queueUpdateCheck(host: string, id: string) {
  const k = host + "/" + id;
  if (useUpdates.getState()[k] || queue.has(k)) return;
  queue.add(k);
  chain = chain.then(async () => {
    await new Promise((r) => setTimeout(r, 300));
    // a machine disconnected meanwhile is not woken up by a check
    if (hostByName(host)?.ok) await loadUpdates(host, id);
    queue.delete(k);
  });
}

export function UpdatesBlock({ host, lab }: { host: string; lab: string }) {
  const k = host + "/" + lab;
  const st = useUpdates((s) => s[k]);
  useEffect(() => {
    if (!useUpdates.getState()[k]) loadUpdates(host, lab);
  }, [k]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!st?.items) return null;
  const news = st.items.filter((u) => u.minor || u.major || u.newer);
  const floating = st.items.filter((u) => !u.pinned && !u.newer);
  if (!news.length && !floating.length) return null;
  const again = () => {
    useUpdates.setState({ [k]: null });
    loadUpdates(host, lab, true);
  };
  const use = async (u: Upd, tag: string, btn: HTMLButtonElement) => {
    btn.disabled = true;
    try {
      const r = await api<{ file: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab) + "/atualizacoes", { method: "POST", body: { service: u.service, tag } });
      gantry.toast(t("{service}: {tag} gravado em {file}. Use Subir para aplicar.", { service: u.service, tag, file: r.file }), "ok");
      again();
    } catch (e) {
      gantry.toast((e as Error).message, "err");
      btn.disabled = false;
    }
  };
  return (
    <div className="block">
      <div className="block-head">
        <h2 className="block-title">{t("Versões das imagens")}</h2>
        <button className="btn small ghost" onClick={again}>
          {t("Verificar de novo")}
        </button>
      </div>
      <div className="panel">
        <table className="table">
          <thead>
            <tr>
              {[t("Serviço"), t("Imagem"), t("Nesta linha"), t("Versão nova"), ""].map((h, i) => (
                <th key={i}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {[...news, ...floating].map((u, i) => (
              <tr key={u.service + i}>
                <td>{u.service}</td>
                <td className="mono small">
                  {u.image}
                  {u.var ? <div className="muted small">{t("versão em {var} no .env", { var: u.var })}</div> : null}
                </td>
                <td>
                  {u.minor ? (
                    <button className="btn small" title={t("Mesma versão principal: costuma ser só correção")} onClick={(e) => use(u, u.minor!, e.currentTarget)}>
                      {t("Usar {tag}", { tag: u.minor })}
                    </button>
                  ) : (
                    <span className="muted small">{u.pinned ? t("em dia") : "—"}</span>
                  )}
                </td>
                <td>
                  {u.major ? (
                    <button className="btn small" title={t("Outra versão principal: leia as notas da versão antes")} onClick={(e) => use(u, u.major!, e.currentTarget)}>
                      {t("Usar {tag}", { tag: u.major })}
                    </button>
                  ) : u.newer ? (
                    <span className="badge warn" title={t("O registry tem outra imagem com a tag {tag} do que a desta máquina", { tag: u.current })}>
                      {t("nova imagem em :{tag} — Atualizar imagens e Subir", { tag: u.current })}
                    </span>
                  ) : u.pinned ? null : (
                    <span className="muted small">{u.checked ? t("sem versão fixa ({tag}): a desta máquina é a mais nova", { tag: u.current }) : t("sem versão fixa ({tag}): muda a cada pull", { tag: u.current })}</span>
                  )}
                </td>
                <td className="muted small">{u.error || ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="muted small">{t("Trocar a versão só grava o arquivo. Depois use Atualizar imagens e Subir.")}</p>

    </div>
  );
}
