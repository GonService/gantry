import { useEffect, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { AsyncButton, Loading } from "@/components/ui";
import { needsLoad, toolsURL, useTools } from "@/views/tools/store";
import { t, tn } from "@/lib/i18n";

type SSHHost = { alias: string; hostname?: string; user?: string; port?: string; added: boolean };
type KubeCtx = { name: string; server: string; auth: string; added?: boolean };
type KubeList = { path: string; contexts: KubeCtx[] | null };

export type WelcomeProps = {
  // the engines of this computer, as the app knows them
  engines: { name: string; state: string }[] | null;
  localHost?: string;
};

function finish(to = "#/local") {
  gantry.store.set("boas-vindas", true);
  location.hash = to;
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div className="panel panel-pad welcome-step">
      <h2>
        <span className="welcome-n">{n}</span>
        {title}
      </h2>
      {children}
    </div>
  );
}

export function Welcome({ engines, localHost }: WelcomeProps) {
  const [ssh, setSSH] = useState<SSHHost[] | null>(null);
  const [kube, setKube] = useState<KubeList | null>(null);
  const [kubeErr, setKubeErr] = useState("");
  const [pickSSH, setPickSSH] = useState<Record<string, boolean>>({});
  const [pickKube, setPickKube] = useState<Record<string, boolean>>({});
  const tools = useTools((s) => s.byKey.local);
  const loadTools = useTools((s) => s.load);

  const readSSH = () => api<SSHHost[]>("/ssh-config").then(setSSH).catch(() => setSSH([]));
  const readKube = () =>
    api<KubeList>("/kubeconfig", { method: "POST", body: {} })
      .then(setKube)
      .catch((e: Error) => {
        setKube({ path: "", contexts: [] });
        setKubeErr(e.message);
      });
  useEffect(() => {
    void readSSH();
    void readKube();
    if (needsLoad(tools)) void loadTools("local", toolsURL(true));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const running = engines?.filter((e) => e.state === "running") ?? [];
  const missing = tools?.data?.tools.filter((x) => !x.have && x.need !== "opcional") ?? [];

  const addSSH = async () => {
    const pick = (ssh ?? []).filter((x) => !x.added && pickSSH[x.alias]);
    if (!pick.length) throw new Error(t("Marque pelo menos um servidor."));
    let ok = 0;
    for (const x of pick) {
      try {
        await api("/hosts", { method: "POST", body: { name: x.alias, url: "ssh://" + x.alias } });
        ok++;
      } catch (e) {
        gantry.toast(x.alias + ": " + (e as Error).message, "err");
      }
    }
    if (ok) gantry.toast(tn(ok, "{n} servidor adicionado.", "{n} servidores adicionados."), "ok");
    setPickSSH({});
    await gantry.loadHosts();
    await readSSH();
  };
  const addKube = async () => {
    const pick = (kube?.contexts ?? []).filter((x) => !x.added && pickKube[x.name]);
    if (!pick.length) throw new Error(t("Marque pelo menos um cluster."));
    let ok = 0;
    for (const x of pick) {
      try {
        await api("/clusters", { method: "POST", body: { path: kube!.path, context: x.name, name: x.name, mode: "livre" } });
        ok++;
      } catch (e) {
        gantry.toast(x.name + ": " + (e as Error).message, "err");
      }
    }
    if (ok) gantry.toast(tn(ok, "{n} cluster adicionado.", "{n} clusters adicionados."), "ok");
    setPickKube({});
    await gantry.loadClusters();
    await readKube();
  };

  return (
    <div className="welcome">
      <div className="page-head">
        <div className="page-title">
          <h1>{t("Bem-vindo ao Gantry")}</h1>
          <div className="sub">{t("Containers, VMs e clusters desta máquina e dos seus servidores. Veja o que já dá para trazer:")}</div>
        </div>
        <div className="actions">
          <button className="btn btn-ghost" type="button" onClick={() => finish()}>
            {t("Pular")}
          </button>
        </div>
      </div>

      <Step n={1} title={t("Este computador")}>
        <p>
          {!engines
            ? t("Verificando os engines…")
            : running.length
              ? t("Containers: {names} rodando.", { names: running.map((e) => e.name).join(", ") })
              : engines.length
                ? t("Há engine instalado, mas parado: {names}.", { names: engines.map((e) => e.name).join(", ") })
                : t("Nenhum engine de containers instalado ainda.")}{" "}
          {engines && !running.length ? <a href="#/local/t/engines">{engines.length ? t("Iniciar") : t("Instalar um")}</a> : null}
        </p>
        {tools?.data ? (
          missing.length ? (
            <p className="muted">
              {t("Falta para tudo funcionar: {names}.", { names: missing.map((x) => x.name).join(", ") })} <a href="#/local/t/ferramentas">{t("Ver ferramentas")}</a>
            </p>
          ) : (
            <p className="muted">{t("Tudo o que o Gantry usa está instalado.")}</p>
          )
        ) : null}
      </Step>

      <Step n={2} title={t("Servidores do ~/.ssh/config")}>
        {ssh === null ? (
          <Loading>{t("Lendo…")}</Loading>
        ) : !ssh.length ? (
          <p className="muted">{t("Nenhum Host no seu ~/.ssh/config. Servidores entram depois pelo \"+ Adicionar um servidor\", na barra lateral.")}</p>
        ) : (
          <>
            <p className="muted">{t("Marque os que quer ver no Gantry. Eles só conectam quando você abrir cada um, usando as suas chaves e o seu ~/.ssh/config.")}</p>
            <div className="welcome-list">
              {ssh.map((x) => (
                <label key={x.alias} className={"welcome-item" + (x.added ? " added" : "")}>
                  <input type="checkbox" checked={x.added || !!pickSSH[x.alias]} disabled={x.added} onChange={(e) => setPickSSH({ ...pickSSH, [x.alias]: e.target.checked })} />
                  <span className="welcome-name">{x.alias}</span>
                  <span className="muted small">
                    {(x.user ? x.user + "@" : "") + (x.hostname ?? "") + (x.port ? ":" + x.port : "") + (x.added ? t(" · já no Gantry") : "")}
                  </span>
                </label>
              ))}
            </div>
            <AsyncButton className="btn primary" onClick={addSSH}>
              {t("Adicionar os marcados")}
            </AsyncButton>
          </>
        )}
      </Step>

      <Step n={3} title={t("Clusters do ~/.kube/config")}>
        {kube === null ? (
          <Loading>{t("Lendo…")}</Loading>
        ) : !kube.contexts?.length ? (
          <p className="muted">
            {kubeErr
              ? t("Nenhum kubeconfig aqui. Clusters entram depois por Configuração › Clusters (escolhendo um arquivo ou colando o conteúdo).")
              : t("Nenhum contexto no seu kubeconfig.")}
          </p>
        ) : (
          <>
            <p className="muted">
              {t("Não precisa do kubectl: o Gantry fala direto com a API de cada cluster. Entram no modo Normal; o modo só leitura fica em Configuração › Clusters.")}
            </p>
            <div className="welcome-list">
              {kube.contexts.map((x) => (
                <label key={x.name} className={"welcome-item" + (x.added ? " added" : "")}>
                  <input type="checkbox" checked={!!x.added || !!pickKube[x.name]} disabled={!!x.added} onChange={(e) => setPickKube({ ...pickKube, [x.name]: e.target.checked })} />
                  <span className="welcome-name">{x.name}</span>
                  <span className="muted small">{[x.server, x.auth].filter(Boolean).join(" · ") + (x.added ? t(" · já no Gantry") : "")}</span>
                </label>
              ))}
            </div>
            <AsyncButton className="btn primary" onClick={addKube}>
              {t("Adicionar os marcados")}
            </AsyncButton>
          </>
        )}
      </Step>

      <Step n={4} title={t("Um primeiro projeto")}>
        <p className="muted">
          {t("Um Postgres com o Adminer para ver como funciona: o Gantry escolhe portas livres, cria a senha e a guarda no chaveiro; é só clicar em Gerar e subir e, depois, abrir o Adminer.")}
        </p>
        <div className="btn-row">
          {localHost ? (
            <button className="btn primary" type="button" onClick={() => finish("#/modelos/gantry-postgres-adminer/gerar?host=" + enc(localHost) + "&nome=teste-postgres&senhas=gerar")}>
              {t("Criar o Postgres de teste")}
            </button>
          ) : (
            <span className="muted">{t("Instale ou inicie um engine (passo 1) para criar projetos aqui.")}</span>
          )}
          <button className="btn" type="button" onClick={() => finish()}>
            {t("Pronto, ir para o Gantry")}
          </button>
        </div>
      </Step>
    </div>
  );
}
