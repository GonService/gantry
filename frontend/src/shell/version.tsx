// New versions of the app: a notice, and Atualizar agora where the app can
// update itself (Homebrew, or the zip of the release on macOS).
import { create } from "zustand";
import { api } from "@/lib/api";
import { copyText } from "@/lib/format";
import { Modal, openDialog } from "@/components/dialog";
import { localStore, toast } from "./toasts";
import { openJob } from "./dock";
import { t, LOCALE } from "@/lib/i18n";

type Ver = { current: string; latest?: string; newer?: boolean; error?: string; date?: string; notes?: string; brew?: string; via?: "brew" | "zip" };
export const useVersion = create<{ v: Ver | null }>(() => ({ v: null }));

export async function checkAppVersion(force = false) {
  let v: Ver;
  try {
    v = await api<Ver>("/versao" + (force ? "?force=1" : ""));
  } catch (e) {
    if (force) toast((e as Error).message, "err");
    return;
  }
  if (force && !v.newer) toast(v.error ? t("Não deu para verificar: {error}", { error: v.error }) : t("Você está na versão mais nova ({version}).", { version: v.latest || v.current }), v.error ? "err" : "ok");
  if (!v.newer || (!force && localStore.get("versao-dispensada", "") === v.latest)) {
    useVersion.setState({ v: null });
    return;
  }
  useVersion.setState({ v });
}

export function appVersionDialog(v: Ver) {
  openDialog((close) => (
    <Modal
      title={t("Gantry {version} disponível", { version: v.latest })}
      onClose={close}
      actions={
        <>
          <button
            className="btn btn-ghost"
            onClick={() => {
              localStore.set("versao-dispensada", v.latest);
              useVersion.setState({ v: null });
              close();
            }}
          >
            {t("Não avisar desta")}
          </button>
          <button className="btn" onClick={close}>
            {t("Depois")}
          </button>
          <button
            className={"btn" + (v.via ? "" : " primary")}
            onClick={async () => {
              try {
                await api("/versao/abrir", { method: "POST" });
                close();
              } catch (e) {
                toast((e as Error).message, "err");
              }
            }}
          >
            {t("Ver a versão")}
          </button>
          {v.via ? (
            <button
              className="btn primary"
              onClick={async () => {
                try {
                  const r = await api<{ id: string }>("/versao/atualizar", { method: "POST" });
                  close();
                  openJob(r.id, t("Atualizar o Gantry para {version}", { version: v.latest }), async (end) => {
                    if (end.ok) toast(t("Gantry atualizado; ele reabre na versão nova."), "ok");
                  });
                } catch (e) {
                  toast((e as Error).message, "err");
                }
              }}
            >
              {t("Atualizar agora")}
            </button>
          ) : null}
        </>
      }
    >
      <p className="muted">
        {t("Você está com a {version}", { version: v.current })}
        {v.date ? t(". Publicada em {date}", { date: new Date(v.date).toLocaleDateString(LOCALE) }) : ""}
        {v.via === "brew"
          ? t(". Atualizar agora roda o brew upgrade e reabre o app; hosts, projetos e clusters continuam.")
          : v.via === "zip"
            ? t(". Atualizar agora baixa a versão, confere o SHA256 e a assinatura, troca o app e reabre; se algo falhar, o instalado fica como está.")
            : t(". O Gantry não atualiza sozinho: baixe a versão nova quando quiser.")}
      </p>
      {v.brew ? (
        <>
          <p>{t("Instalado pelo Homebrew. Para atualizar, rode no Terminal:")}</p>
          <div className="brew-cmd">
            <code>{v.brew}</code>
            <button className="btn small" onClick={async () => toast((await copyText(v.brew!)) ? t("Comando copiado.") : t("Não deu para copiar; selecione o comando."), "ok")}>
              {t("Copiar")}
            </button>
          </div>
        </>
      ) : null}
      {v.notes ? <pre className="release-notes">{v.notes}</pre> : null}
    </Modal>
  ));
}
