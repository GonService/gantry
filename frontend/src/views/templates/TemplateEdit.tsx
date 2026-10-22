// The editor of a template (or a new one): name, category, description and
// the compose file, with what it creates beside it.
import { useEffect, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { confirm } from "@/components/dialog";
import { CodeEditor, useDebounced } from "@/components/editor";
import { ErrorBox, Loading } from "@/components/ui";
import { checkYAML, loadTemplates, tplHref, useTemplates, type Check, type Template } from "./store";
import { CheckSummary } from "./CheckSummary";
import { duplicateTemplate } from "./TemplatesPage";

const SAMPLE = 'services:\n  app:\n    image: ${IMAGEM:-nginx:alpine}\n    ports:\n      - "${PORTA:-8080}:80"\n';

export function TemplateEdit({ id, host = "" }: { id: string; host?: string }) {
  const [tpl, setTpl] = useState<Template | null>(id === "novo" ? { id: "", name: "", category: "", description: "", yaml: SAMPLE } : null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (id === "novo") return;
    setTpl(null);
    api<Template>("/templates/" + enc(id))
      .then(setTpl)
      .catch((e) => setErr((e as Error).message));
  }, [id]);
  if (err) return <ErrorBox>{err}</ErrorBox>;
  if (!tpl) return <Loading>{t("Abrindo…")}</Loading>;
  return <Editor key={tpl.id || "novo"} t={tpl} host={host} />;
}

function Editor({ t: tpl, host }: { t: Template; host: string }) {
  const ro = !!tpl.builtin;
  const [m, setM] = useState({ name: tpl.name, category: tpl.category || "", description: tpl.description || "", yaml: tpl.yaml });
  const [chk, setChk] = useState<Check | null>(null);
  const [err, setErr] = useState("");
  const idRef = useRef(tpl.id);
  const dirty = useRef(false);
  const seq = useRef(0);
  const list = useTemplates((s) => s.list);
  useEffect(() => {
    if (!tpl.id && !list) loadTemplates();
  }, [tpl.id, list]);
  useDebounced(
    () => {
      const my = ++seq.current;
      checkYAML(m.yaml).then((r) => {
        if (my === seq.current) setChk(r);
      });
    },
    350,
    [m.yaml],
  );
  const set = (k: keyof typeof m, v: string) => {
    dirty.current = true;
    setM((s) => ({ ...s, [k]: v }));
  };
  const q = host ? "?host=" + enc(host) : "";

  const save = async (deploy: boolean) => {
    setErr("");
    try {
      const body = { name: m.name, category: m.category, description: m.description, yaml: m.yaml };
      const r = idRef.current
        ? await api<{ id: string }>("/templates/" + enc(idRef.current), { method: "PUT", body })
        : await api<{ id: string }>("/templates", { method: "POST", body });
      dirty.current = false;
      useTemplates.setState({ list: null });
      gantry.toast(t("Modelo salvo"), "ok");
      if (deploy) location.hash = tplHref(r.id, true) + q;
      else if (!idRef.current) location.hash = tplHref(r.id) + q;
      else idRef.current = r.id;
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const startFrom = async (srcId: string) => {
    const src = (list ?? []).find((x) => x.id === srcId);
    if (!src) return;
    if (dirty.current && !(await confirm(t("Trocar o conteúdo?"), t("O YAML atual será substituído pelo do modelo escolhido."), t("Trocar")))) return;
    setM((s) => ({ ...s, yaml: src.yaml, name: s.name || src.name, category: s.category || src.category || "" }));
    dirty.current = false;
  };

  const input = (k: "name" | "category" | "description", ph: string, idAttr?: string) => (
    <input id={idAttr} value={m[k]} placeholder={ph} readOnly={ro} spellCheck={false} onChange={(e) => set(k, e.target.value)} />
  );

  return (
    <div className="tpl-edit">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={"#/modelos" + q}>{t("Modelos")}</a>
          </nav>
          <h1>{tpl.id ? (ro ? tpl.name : t("Editar modelo")) : t("Novo modelo")}</h1>
          {ro ? <div className="sub">{t("Modelo do Gantry: só leitura. Duplique para mudar.")}</div> : null}
        </div>
        <div className="actions">
          <a className="btn btn-ghost" href={"#/modelos" + q}>
            {ro ? t("Voltar") : t("Cancelar")}
          </a>
          {ro ? (
            <button className="btn" onClick={() => duplicateTemplate(tpl)}>
              {t("Duplicar")}
            </button>
          ) : (
            <button className="btn" onClick={() => save(false)}>
              {t("Salvar")}
            </button>
          )}
          {ro ? (
            <a className="btn primary" href={tplHref(tpl.id, true) + q}>
              {t("Gerar projeto")}
            </a>
          ) : (
            <button className="btn primary" onClick={() => save(true)}>
              {t("Salvar e gerar projeto")}
            </button>
          )}
        </div>
      </div>
      {err ? <p className="flash flash-err">{err}</p> : null}
      <div className="panel panel-pad tpl-meta">
        <div className="grid3">
          <div className="field">
            <label htmlFor="tpl-name">{t("Nome")}</label>
            {input("name", t("Meu app"), "tpl-name")}
          </div>
          <div className="field">
            <label htmlFor="tpl-cat">
              {t("Categoria")}<span className="opt">{t(" opcional")}</span>
            </label>
            {input("category", "Web", "tpl-cat")}
          </div>
          {tpl.id ? (
            <div />
          ) : (
            <div className="field">
              <label>{t("Ponto de partida")}</label>
              <select className="select" aria-label={t("Começar de um modelo")} value="" onChange={(e) => startFrom(e.target.value)}>
                <option value="">{t("Começar de um modelo…")}</option>
                {(list ?? [])
                  .filter((x) => x.builtin)
                  .map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.name}
                    </option>
                  ))}
              </select>
            </div>
          )}
        </div>
        <div className="field">
          <label>
            {t("Descrição")}<span className="opt">{t(" opcional")}</span>
          </label>
          {input("description", t("O que este modelo cria"))}
        </div>
      </div>
      <div className="editor-layout">
        <section className="editor" aria-label={t("YAML do modelo")}>
          <div className="editor-bar">
            <span className="mono">compose.yaml</span>
            <span className="muted small">{t("x-gantry: engine por serviço · x-vms: máquinas virtuais · ${VAR:-padrão}: campos")}</span>
          </div>
          <CodeEditor value={m.yaml} onChange={(v) => set("yaml", v)} readOnly={ro} label={t("compose do modelo")} onSave={() => save(false)} />
        </section>
        <div className="side-panels">
          <div className="panel panel-pad">
            <h2 style={{ marginBottom: 6 }}>{t("O que este modelo cria")}</h2>
            <CheckSummary r={chk} />
          </div>
          <div className="panel panel-pad tpl-help">
            <h2 style={{ marginBottom: 6 }}>{t("Dicas")}</h2>
            <ul>
              <li>
                <code>{"${SENHA:?mensagem}"}</code> {t("torna o campo obrigatório.")}
              </li>
              <li>
                <code>{"x-gantry: {runtime: podman}"}</code> {t("no topo ou num serviço escolhe o engine.")}
              </li>
              <li>
                <code>x-vms:</code> {t("cria VMs no QEMU da máquina (image, cpus, memory, ports, packages).")}
              </li>
              <li>
                {t("O nome do projeto é escolhido ao gerar; não precisa de")} <code>name:</code>.
              </li>
            </ul>
          </div>
        </div>
      </div>
    </div>
  );
}
