// Modelos: compose files kept in the app that projects are created from.
// Variables written as ${NAME:-padrão} become the fields of "Gerar projeto".
import { useEffect } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { confirm } from "@/components/dialog";
import { ErrorBox, Loading } from "@/components/ui";
import { loadTemplates, tplHref, tplTags, useTemplates, type Template } from "./store";

// pickTextFile opens the system file picker and gives the chosen file.
export function pickTextFile(accept: string): Promise<File | null> {
  return new Promise((resolve) => {
    const inp = document.createElement("input");
    inp.type = "file";
    inp.accept = accept;
    inp.style.display = "none";
    inp.onchange = () => {
      const f = inp.files?.[0] ?? null;
      inp.remove();
      resolve(f);
    };
    document.body.append(inp);
    inp.click();
  });
}

export async function duplicateTemplate(tpl: Template) {
  try {
    const n = await api<{ id: string }>("/templates", { method: "POST", body: { name: t("{name} (cópia)", { name: tpl.name }), category: tpl.category, description: tpl.description, yaml: tpl.yaml } });
    useTemplates.setState({ list: null });
    location.hash = tplHref(n.id);
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

async function importTemplate() {
  const f = await pickTextFile(".yaml,.yml,text/yaml");
  if (!f) return;
  const yaml = await f.text();
  const name = f.name.replace(/\.(ya?ml)$/i, "").replace(/^(docker-)?compose$/i, t("Importado"));
  try {
    const n = await api<{ id: string }>("/templates", { method: "POST", body: { name, yaml } });
    useTemplates.setState({ list: null });
    location.hash = tplHref(n.id);
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

export async function deleteTemplate(tpl: Template): Promise<boolean> {
  if (!(await confirm(t('Excluir o modelo "{name}"?', { name: tpl.name }), t("Os projetos já gerados com ele não mudam."), t("Excluir"), true))) return false;
  try {
    await api("/templates/" + enc(tpl.id), { method: "DELETE" });
    gantry.toast(t("Modelo excluído"), "ok");
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
  await loadTemplates();
  return true;
}

export function TemplatesPage({ host = "" }: { host?: string }) {
  const { list, err } = useTemplates();
  useEffect(() => {
    if (!list) loadTemplates();
  }, [list]);
  const q = host ? "?host=" + enc(host) : "";

  const card = (tpl: Template) => (
    <div className="panel card tpl-card" key={tpl.id}>
      <span className="card-top">
        <a className="card-name" href={tplHref(tpl.id)}>
          {tpl.name}
        </a>
        {tpl.category && !tpl.builtin ? <span className="badge">{tpl.category}</span> : null}
      </span>
      {tpl.description ? <span className="muted small">{tpl.description}</span> : null}
      <span className="tpl-tags">
        {tplTags(tpl.yaml).map((x) => (
          <span key={x} className={"rt-chip rt-" + x}>
            {x}
          </span>
        ))}
      </span>
      <div className="actions tpl-actions">
        <a className="btn small primary" href={tplHref(tpl.id, true) + q}>
          {t("Gerar projeto")}
        </a>
        <a className="btn small" href={tplHref(tpl.id)}>
          {tpl.builtin ? t("Ver") : t("Editar")}
        </a>
        <button className="btn small" onClick={() => duplicateTemplate(tpl)}>
          {t("Duplicar")}
        </button>
        {tpl.builtin ? null : (
          <button className="btn small danger" onClick={() => deleteTemplate(tpl)}>
            {t("Excluir")}
          </button>
        )}
      </div>
    </div>
  );

  const own = (list ?? []).filter((x) => !x.builtin);
  const builtin = (list ?? []).filter((x) => x.builtin);
  const cats = [...new Set(builtin.map((x) => x.category || t("Outros")))];
  return (
    <div id="tpl-view">
      <div className="page-head">
        <div className="page-title">
          <h1>{t("Modelos")}</h1>
          <div className="sub">
            {t("Arquivos compose para gerar projetos: containers em qualquer engine (x-gantry) e VMs (x-vms). Variáveis como")} <code>{"${SENHA}"}</code> {t("viram campos na hora de gerar.")}
          </div>
        </div>
        <div className="actions">
          <button className="btn" onClick={importTemplate}>
            {t("Importar arquivo…")}
          </button>
          <a className="btn primary" href={"#/modelos/novo" + q}>
            {t("Novo modelo")}
          </a>
        </div>
      </div>
      {host ? (
        <div className="banner info">
          <span className="grow">{t("Escolha um modelo para gerar um projeto em {host}.", { host })}</span>
        </div>
      ) : null}
      {err && !list ? (
        <ErrorBox>{err}</ErrorBox>
      ) : !list ? (
        <Loading />
      ) : (
        <>
          <div className="block">
            <div className="block-head">
              <h2 className="block-title">{t("Meus modelos")}</h2>
            </div>
            {own.length ? (
              <div className="cards">{own.map(card)}</div>
            ) : (
              <div className="panel panel-pad">
                <p className="muted" style={{ margin: 0 }}>
                  {t("Nenhum ainda. Crie um, importe um compose ou duplique um dos modelos abaixo.")}
                </p>
              </div>
            )}
          </div>
          <div className="block">
            <div className="block-head">
              <h2 className="block-title">{t("Do Gantry")}</h2>
            </div>
            {cats.map((c) => (
              <div className="tpl-cat" key={c}>
                <h3 className="sub">{c}</h3>
                <div className="cards">{builtin.filter((x) => (x.category || t("Outros")) === c).map(card)}</div>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
