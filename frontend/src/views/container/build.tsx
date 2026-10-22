// Build an image: from a Dockerfile in the editor, a folder on the host or
// a git repository.
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, hostHref, RT_NAME, rtQuery, type Host } from "@/lib/app";
import { engineChoices } from "@/views/host/obs";
import { RowsEditor, type Row } from "./rows";
import { loadResources } from "./resources";
import { t } from "@/lib/i18n";

const ORIGINS: [string, string][] = [
  ["editor", t("Editor")],
  ["dir", t("Pasta no host")],
  ["git", t("Repositório git")],
];

export function BuildPage({ host }: { host: string }) {
  const hst = gantry.hostByName(host) as Host | undefined;
  const [m, setM] = useState({
    tags: "",
    source: "editor",
    dockerfile: "FROM alpine:3.20\nRUN apk add --no-cache curl\nCMD [\"sh\"]\n",
    dir: "",
    path: "",
    git: "",
    target: "",
    noCache: false,
    pull: false,
    rt: "",
  });
  const [args, setArgs] = useState<Row[]>([]);
  const [engines, setEngines] = useState<[string, string][]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof m, v: string | boolean) => setM((s) => ({ ...s, [k]: v }));
  useEffect(() => {
    if (!hst) return;
    engineChoices(host).then((list) => {
      const others = list.filter((e) => e.runtime !== hst.runtime && (e.runtime === "docker" || e.runtime === "podman"));
      if (others.length) setEngines([["", t("o deste host ({rt})", { rt: hst.runtime || "docker" })], ...others.map((e) => [e.runtime, RT_NAME[e.runtime] || e.runtime] as [string, string])]);
    });
  }, [host]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!hst) return null;
  const field = (label: string, input: React.ReactNode, hint?: string, opt?: boolean) => (
    <div className="field">
      <label>
        {label}
        {opt ? <span className="opt"> {t("opcional")}</span> : null}
      </label>
      {input}
      {hint ? <p className="hint">{hint}</p> : null}
    </div>
  );
  const text = (k: "tags" | "dir" | "path" | "git" | "target", ph: string) => <input value={m[k]} placeholder={ph} spellCheck={false} autoComplete="off" onChange={(e) => set(k, e.target.value)} />;
  const check = (k: "pull" | "noCache", label: string) => (
    <label className="check">
      <input type="checkbox" checked={m[k]} onChange={(e) => set(k, e.target.checked)} /> {label}
    </label>
  );
  const shim = hst.runtime === "nerdctl" || hst.runtime === "k3s";
  const imagesHref = hostHref(host) + "/t/containers";
  const go = async () => {
    setErr("");
    const tags = m.tags.split(/[,\s]+/).filter(Boolean);
    const a = Object.fromEntries(args.filter(([k]) => k && k.trim()).map(([k, v]) => [k.trim(), v || ""]));
    setBusy(true);
    try {
      const r = await api<{ job: string }>("/hosts/" + enc(host) + "/build" + rtQuery(m.rt), {
        method: "POST",
        body: { tags, source: m.source, dockerfile: m.dockerfile, dir: m.dir, git: m.git, path: m.path, args: a, target: m.target, noCache: m.noCache, pull: m.pull },
      });
      gantry.openJob(r.job, t("Construir {tag}", { tag: tags[0] }), async () => {
        loadResources(host, "images", m.rt);
      });
      gantry.toast(t("Construindo {tag}… acompanhe no painel de baixo", { tag: tags[0] }), "ok");
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };
  return (
    <div className="ctr-form">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={hostHref(host)}>{host}</a>
            <span aria-hidden="true">/</span>
            <a href={imagesHref}>{t("Imagens")}</a>
          </nav>
          <h1>{t("Construir imagem")}</h1>
        </div>
      </div>
      {shim ? <div className="flash flash-warn">{t("Este host usa {rt}: o Gantry ainda não constrói imagens nele. Use o build do próprio engine no servidor.", { rt: hst.runtime })}</div> : null}
      <div className="panel panel-pad cf-top">
        {field(t("Nome da imagem"), text("tags", t("meu-app:1.0")), t("Mais de um nome: separe por vírgula (ex.: meu-app:1.0, meu-app:latest)."))}
        {engines.length
          ? field(
              "Engine",
              <select value={m.rt} onChange={(e) => set("rt", e.target.value)}>
                {engines.map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </select>,
            )
          : null}
      </div>
      <div className="tabs" role="tablist" aria-label={t("Origem")}>
        {ORIGINS.filter(([k]) => k !== "dir" || hst.files).map(([k, l]) => (
          <button key={k} type="button" className="tab" role="tab" aria-selected={m.source === k} onClick={() => set("source", k)}>
            {l}
          </button>
        ))}
      </div>
      <div className="panel panel-pad">
        {m.source === "editor"
          ? field(
              "Dockerfile",
              <textarea className="code build-df" rows={16} spellCheck={false} value={m.dockerfile} onChange={(e) => set("dockerfile", e.target.value)} />,
              t("Sem outros arquivos: COPY e ADD só funcionam com a origem Pasta ou Git."),
            )
          : m.source === "dir"
            ? (
              <>
                {field(t("Pasta no host"), text("dir", t("~/projetos/meu-app")), t("É enviada como contexto do build. O .dockerignore da pasta é respeitado."))}
                {field("Dockerfile", text("path", "Dockerfile"), t("Caminho dentro da pasta."), true)}
              </>
            )
            : (
              <>
                {field(t("Repositório"), text("git", t("https://github.com/usuario/projeto.git#main:pasta")), t("Depois do # vêm o branch e, após :, a subpasta. O engine baixa o repositório; repositórios privados precisam de acesso no host."))}
                {field("Dockerfile", text("path", "Dockerfile"), t("Caminho dentro do repositório (ou da subpasta)."), true)}
              </>
            )}
        <h3 className="sub">{t("Argumentos (--build-arg)")}</h3>
        <RowsEditor
          rows={args}
          cols={[
            { ph: t("NOME"), label: t("Nome"), w: "1" },
            { ph: t("valor"), label: t("Valor"), w: "2" },
          ]}
          addLabel={t("argumento")}
          onChange={setArgs}
        />
        <div className="cols2" style={{ marginTop: 14 }}>
          {field(t("Estágio (--target)"), text("target", t("producao")), undefined, true)}
          <div />
        </div>
        <div className="checks">
          {check("pull", t("Baixar de novo as imagens base"))}
          {check("noCache", t("Sem cache"))}
        </div>
      </div>
      {err ? <p className="flash flash-err">{err}</p> : null}
      <div className="cf-actions">
        <a className="btn btn-ghost" href={imagesHref}>
          {t("Cancelar")}
        </a>
        <button type="button" className="btn primary" disabled={busy} onClick={go}>
          {t("Construir")}
        </button>
      </div>
    </div>
  );
}
