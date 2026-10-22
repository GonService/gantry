// Gerar projeto: a template becomes a project (compose.yaml and .env) in a
// folder of a host, and goes up.
import { useEffect, useState } from "react";
import { NeedImage } from "@/views/shared/vmimages";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { FolderInput } from "@/components/editor";
import { ErrorBox, Loading } from "@/components/ui";
import { checkYAML, createLab, envText, randomSecret, slug, tplHref, type Check, type Target, type Template } from "./store";
import { CheckSummary } from "./CheckSummary";
import { useShared, VarFields } from "./shared";

export type DeployProps = { id: string; host?: string; nome?: string; senhas?: string; targets: Target[] };

export function Deploy(p: DeployProps) {
  const [data, setData] = useState<{ t: Template; chk: Check } | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    setData(null);
    api<Template>("/templates/" + enc(p.id))
      .then(async (tpl) => setData({ t: tpl, chk: await checkYAML(tpl.yaml) }))
      .catch((e) => setErr((e as Error).message));
  }, [p.id]);
  if (err) return <ErrorBox>{err}</ErrorBox>;
  if (!data) return <Loading>{t("Abrindo…")}</Loading>;
  return <DeployForm {...p} t={data.t} chk={data.chk} />;
}

function targetLabel(x: Target): string {
  const mach = gantry.machines().find((mm) => mm.hosts.some((y: { name: string }) => y.name === x.name));
  const where = mach && mach.local ? gantry.localName() : mach ? mach.name : x.name;
  return (where !== x.name ? where + " · " : "") + x.name + (x.runtime ? " (" + x.runtime + ")" : "") + (x.ok ? "" : x.idle ? t(" — não conectado") : t(" — sem conexão"));
}

function DeployForm({ t: tpl, chk, host: want = "", nome, senhas, targets }: DeployProps & { t: Template; chk: Check }) {
  const [host, setHost] = useState(() => (targets.find((x) => x.name === want) || targets.find((x) => x.ok) || targets[0])?.name ?? "");
  const [name, setName] = useState(nome || slug(tpl.name));
  const [dir, setDir] = useState("");
  const [up, setUp] = useState(true);
  const [keychain, setKeychain] = useState(true);
  const [extra, setExtra] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [vals, setVals] = useState<Record<string, string>>(() => {
    const v: Record<string, string> = {};
    for (const x of chk.vars ?? []) v[x.name] = x.default || "";
    if (senhas === "gerar") for (const x of chk.vars ?? []) if (!v[x.name] && /PASS|SENHA|SECRET/i.test(x.name)) v[x.name] = randomSecret();
    return v;
  });
  const shared = useShared(host, chk);
  const fromShared = shared.vars(name);
  const theDir = dir || "~/gantry/" + (name || "projeto");
  const q = want ? "?host=" + enc(want) : "";

  const go = async () => {
    setErr("");
    if (!/^[a-z0-9][a-z0-9_-]*$/.test(name)) return setErr(t("Nome do projeto: letras minúsculas, números, - ou _."));
    if (!host) return setErr(t("Escolha onde gerar o projeto."));
    const missing = (chk.vars ?? []).filter((v) => v.required && !String(vals[v.name] || "").trim() && !fromShared[v.name]).map((v) => v.name);
    if (missing.length) return setErr(t("Preencha: {list}", { list: missing.join(", ") }));
    setBusy(true);
    try {
      const v = { ...vals };
      await shared.provision(name, v);
      const env = envText(v) + (extra.trim() ? "\n" + extra.trim() : "");
      await createLab(host, { name, dir: theDir, compose: tpl.yaml, env, up, keychain, files: tpl.files || {} });
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };

  return (
    <div className="tpl-deploy">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={"#/modelos" + q}>{t("Modelos")}</a>
            <span aria-hidden="true">/</span>
            <a href={tplHref(tpl.id) + q}>{tpl.name}</a>
          </nav>
          <h1>{t("Gerar projeto")}</h1>
          {tpl.description ? <div className="sub">{tpl.description}</div> : null}
        </div>
        <div className="actions">
          <a className="btn btn-ghost" href={"#/modelos" + q}>
            {t("Cancelar")}
          </a>
          <button className="btn primary" disabled={busy} onClick={go}>
            {busy ? (
              <>
                <span className="spin" />
                {up ? t("Gerando e subindo…") : t("Gerando…")}
              </>
            ) : up ? (
              t("Gerar e subir")
            ) : (
              t("Só gerar os arquivos")
            )}
          </button>
        </div>
      </div>
      {err ? <p className="flash flash-err">{err}</p> : null}
      {!targets.length ? <ErrorBox>{t("Nenhum host que execute compose. Adicione um servidor via ssh:// ou inicie um engine nesta máquina.")}</ErrorBox> : null}
      <div className="deploy-grid">
        <div>
          <div className="panel panel-pad">
            <h2 style={{ marginBottom: 10 }}>{t("Onde")}</h2>
            <div className="field">
              <label>Host</label>
              <select value={host} onChange={(e) => setHost(e.target.value)}>
                {targets.map((x) => (
                  <option key={x.name} value={x.name}>
                    {targetLabel(x)}
                  </option>
                ))}
              </select>
              <p className="hint">{t("Serviços com x-gantry vão para o engine pedido na mesma máquina; VMs rodam no QEMU dela.")}</p>
            </div>
            {host ? <NeedImage host={host} yaml={tpl.yaml} /> : null}
            <div className="grid2">
              <div className="field">
                <label>{t("Nome do projeto")}</label>
                <input value={name} spellCheck={false} onChange={(e) => setName(e.target.value.trim())} />
                <p className="hint">{t("Vira o nome do projeto do compose.")}</p>
              </div>
              <div className="field">
                <label>{t("Pasta no host")}</label>
                <FolderInput value={theDir} onChange={setDir} host={host} title={t("Pasta do projeto {name}", { name })} />
                <p className="hint">{t("O compose.yaml e o .env ficam aqui; a pasta é criada.")}</p>
              </div>
            </div>
            <label className="check">
              <input type="checkbox" checked={up} onChange={(e) => setUp(e.target.checked)} /> {t("Subir depois de gerar")}
            </label>
            <label className="check" title={t("Senhas e tokens ficam no chaveiro deste computador em vez do .env; o Gantry os entrega ao compose")}>
              <input type="checkbox" checked={keychain} onChange={(e) => setKeychain(e.target.checked)} /> {t("Guardar senhas e tokens no chaveiro (fora do .env)")}
            </label>
          </div>
          {shared.section(name)}
          <div className="panel panel-pad" style={{ marginTop: 16 }}>
            <h2 style={{ marginBottom: 10 }}>{t("Variáveis")}</h2>
            <VarFields
              vars={chk.vars ?? []}
              vals={vals}
              setVal={(k, v) => setVals((s) => ({ ...s, [k]: v }))}
              fromShared={fromShared}
              empty={
                <p className="muted small" style={{ margin: "0 0 8px" }}>
                  {t("Este modelo não tem variáveis.")}
                </p>
              }
            />
            <div className="field">
              <label>
                {t("Outras variáveis (.env)")}<span className="opt">{t(" opcional")}</span>
              </label>
              <textarea className="mono-input" rows={3} spellCheck={false} placeholder="TZ=America/Sao_Paulo" value={extra} onChange={(e) => setExtra(e.target.value)} />
            </div>
          </div>
        </div>
        <div className="side-panels">
          <div className="panel panel-pad">
            <h2 style={{ marginBottom: 6 }}>{t("O que vai ser criado")}</h2>
            <CheckSummary r={chk} />
          </div>
          <details className="panel panel-pad">
            <summary>{t("Ver o YAML")}</summary>
            <pre className="code yaml-pre">{tpl.yaml}</pre>
          </details>
        </div>
      </div>
    </div>
  );
}
