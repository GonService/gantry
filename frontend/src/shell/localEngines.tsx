// The engines of this computer (Docker, Podman, containerd…): start,
// install, and the hosts hidden from the list.
import { api, enc } from "@/lib/api";
import { hostHref } from "@/lib/app";
import { loadHidden, loadHosts, loadLocal, useStore } from "@/lib/store";
import { confirm } from "@/components/dialog";
import { Loading, ErrorBox } from "@/components/ui";
import { openJob } from "./dock";
import { openHostDialog } from "./hostDialog";
import { toast } from "./toasts";
import { t } from "@/lib/i18n";

const ENGINE_STATE: Record<string, [string, string]> = {
  running: ["ok", t("rodando")],
  starting: ["warn", t("iniciando")],
  stopped: ["warn", t("parado")],
  "no-machine": ["warn", t("sem máquina")],
  "socket-off": ["warn", t("socket desligado")],
  missing: ["", t("não instalado")],
  unresponsive: ["err", t("sem resposta")],
};

type Action = { id: string; label: string; note?: string; primary?: boolean };

async function runLocalAction(a: Action) {
  if (useStore.getState().localBusy) return;
  if (a.note && !(await confirm(a.label + "?", a.note, a.label))) return;
  useStore.setState({ localBusy: true });
  try {
    const r = await api<{ id: string }>("/local/" + a.id, { method: "POST" });
    openJob(r.id, a.label);
  } catch (e) {
    toast((e as Error).message, "err");
    useStore.setState({ localBusy: false });
  }
}

export function LocalEngines() {
  const st = useStore((s) => s.local);
  const hosts = useStore((s) => s.hosts);
  const hidden = useStore((s) => s.hidden);
  const busy = useStore((s) => s.localBusy);
  if (!st) return <Loading>{t("Procurando engines instalados…")}</Loading>;
  const hostForSocket = (sock?: string) => (sock ? hosts.find((x) => x.url === "unix://" + sock) : undefined);
  return (
    <div>
      {st.error ? <ErrorBox>{st.error}</ErrorBox> : null}
      {!st.engines.length ? (
        <>
          <div className="banner info">
            <span className="grow">{t("Nenhum engine de containers instalado. Para rodar containers aqui, instale um deles. Servidores remotos funcionam sem isso, via SSH.")}</span>
          </div>
          {st.install.length ? (
            <div className="cards">
              {st.install.map((a: Action) => (
                <div className="panel card" key={a.id}>
                  <span className="card-top">
                    <span className="card-name">{a.label.replace(/^(Instalar o|Install) /, "")}</span>
                    {a.primary ? <span className="badge accent">{t("recomendado")}</span> : null}
                  </span>
                  <span className="muted small">{a.note}</span>
                  <div style={{ marginTop: 8 }}>
                    <button className={"btn" + (a.primary ? " primary" : "")} disabled={busy} onClick={() => runLocalAction(a)}>
                      {busy ? (
                        <>
                          <span className="spin" />
                          {t("Aguarde…")}
                        </>
                      ) : (
                        a.label
                      )}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <div className="panel panel-pad">
              <p style={{ margin: "0 0 8px" }}>{st.os === "darwin" ? t("Instale o Homebrew e depois o Podman:") : t("Instale o Podman pelo gerenciador de pacotes:")}</p>
              <pre className="error-box" style={{ background: "var(--surface-2)", color: "var(--text)" }}>
                {st.os === "darwin" ? "brew install podman docker-compose\npodman machine init --now" : "sudo dnf install podman docker-compose\nsystemctl --user enable --now podman.socket"}
              </pre>
            </div>
          )}
        </>
      ) : (
        <div className="cards">
          {st.engines.map((e: { name: string; state: string; detail?: string; socket?: string; hint?: string; actions: Action[] }) => {
            const [kind, label] = ENGINE_STATE[e.state] || ["", e.state];
            const host = hostForSocket(e.socket);
            return (
              <div className="panel card" key={e.name}>
                <span className="card-top">
                  <span className="card-name">{e.name}</span>
                  <span className={"badge " + kind}>{label}</span>
                </span>
                {e.detail ? <span className="muted small">{e.detail}</span> : null}
                {e.socket ? <span className="sub">{e.socket}</span> : null}
                <div className="actions" style={{ marginTop: 8 }}>
                  {host ? (
                    <a className="btn primary" href={hostHref(host.name)}>
                      {t("Ver containers")}
                    </a>
                  ) : null}
                  {e.actions.map((a) => (
                    <button key={a.id} className={"btn" + (a.primary ? " primary" : "")} disabled={busy} title={a.note} onClick={() => runLocalAction(a)}>
                      {a.label}
                    </button>
                  ))}
                </div>
                {e.state !== "running" && e.hint ? (
                  <span className="hint">
                    {t("Ou no terminal:")} <code>{e.hint}</code>
                  </span>
                ) : null}
              </div>
            );
          })}
        </div>
      )}
      <div className="block">
        <div className="block-head">
          <h2 className="block-title">{t("Servidores remotos")}</h2>
        </div>
        <div className="panel panel-pad">
          <p style={{ margin: "0 0 10px" }}>{t("Para gerenciar containers de outro computador, adicione um host via SSH. O app usa o ssh do sistema e o seu ~/.ssh/config, sem instalar nada no servidor.")}</p>
          <button className="btn" onClick={() => openHostDialog()}>
            {t("+ Adicionar host")}
          </button>
        </div>
      </div>
      {hidden && hidden.length ? (
        <div className="block">
          <div className="block-head">
            <h2 className="block-title">{t("Hosts ocultos")}</h2>
          </div>
          <div className="panel panel-pad kv">
            {hidden.map((u) => (
              <div key={u}>
                <span className="v" style={{ textAlign: "left" }}>
                  {u}
                </span>
                <button
                  className="btn small"
                  onClick={async () => {
                    try {
                      await api("/hidden?url=" + enc(u), { method: "DELETE" });
                      await loadHosts(true);
                      await loadHidden();
                    } catch (e) {
                      toast((e as Error).message, "err");
                    }
                  }}
                >
                  {t("Mostrar")}
                </button>
              </div>
            ))}
          </div>
        </div>
      ) : null}
      <div className="actions" style={{ marginTop: 12 }}>
        <button
          className="btn"
          onClick={() => {
            useStore.setState({ local: null });
            loadLocal();
            loadHosts(true);
          }}
        >
          {t("Verificar de novo")}
        </button>
      </div>
    </div>
  );
}
