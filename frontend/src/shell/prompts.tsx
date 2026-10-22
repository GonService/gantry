// Questions of SSH while connecting: a password (of the server or of the
// key) or whether to trust a server seen for the first time.
import { useEffect, useState } from "react";
import { api, apiURL } from "@/lib/api";
import { loadHosts } from "@/lib/store";
import { toast } from "./toasts";
import { Modal } from "@/components/dialog";
import { t } from "@/lib/i18n";

type Prompt = { id: string; kind: string; host: string; text: string };

export function Prompts() {
  const [list, setList] = useState<Prompt[]>([]);
  const [answer, setAnswer] = useState("");
  const [remember, setRemember] = useState(true);
  const [gone, setGone] = useState<Set<string>>(new Set());
  useEffect(() => {
    const es = new EventSource(apiURL("/prompts/stream", true));
    es.onmessage = (e) => {
      try {
        setList(JSON.parse(e.data) || []);
      } catch {
        setList([]);
      }
    };
    return () => es.close();
  }, []);
  const p = list.find((x) => !gone.has(x.id));
  useEffect(() => {
    setAnswer("");
    setRemember(true);
  }, [p?.id]);
  if (!p) return null;
  const secret = p.kind === "secret";
  const send = async (cancel: boolean) => {
    setGone((g) => new Set(g).add(p.id));
    const body = { cancel, answer: cancel ? "" : answer, remember };
    setAnswer("");
    try {
      await api("/prompts/" + p.id, { method: "POST", body });
    } catch (e) {
      toast((e as Error).message, "err");
    }
    if (!cancel) setTimeout(() => loadHosts(), 1500);
  };
  return (
    <Modal
      key={p.id}
      title={(secret ? t("Senha SSH") : t("Confiar neste servidor?")) + " · " + p.host}
      onClose={() => send(true)}
      onSubmit={(e) => {
        e.preventDefault();
        send(false);
      }}
      actions={
        <>
          <button type="button" className="btn btn-ghost" onClick={() => send(true)}>
            {t("Cancelar")}
          </button>
          <button type="submit" className="btn primary" autoFocus={!secret}>
            {secret ? t("Conectar") : t("Confiar e conectar")}
          </button>
        </>
      }
    >
      <pre className="auth-text">{p.text}</pre>
      {secret ? (
        <div>
          <div className="field">
            <label htmlFor="auth-input">{t("Senha")}</label>
            <input id="auth-input" type="password" autoComplete="off" spellCheck={false} autoFocus value={answer} onChange={(e) => setAnswer(e.target.value)} />
          </div>
          <label className="check">
            <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} /> {t("Lembrar até fechar o Gantry")}
          </label>
        </div>
      ) : null}
      <p className="hint">
        {secret
          ? /passphrase/i.test(p.text)
            ? t("Senha da sua chave SSH local.")
            : t("Senha do usuário no servidor.")
          : t("É a primeira conexão com este servidor. Confira a impressão digital antes de aceitar; ela fica salva no seu known_hosts.")}
      </p>
    </Modal>
  );
}
