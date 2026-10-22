// Shared services (x-gantry: usa): a template can take a database or a
// queue that already runs on the machine. The choice and the list of the
// machine's services live in the page that generates the project.
import { useEffect, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import type { Check } from "./store";

const TITLE: Record<string, string> = { postgres: "PostgreSQL", mysql: "MySQL / MariaDB", redis: "Redis", rabbitmq: "RabbitMQ", mongo: "MongoDB" };

type Svc = { id: string; kind: string; name: string; title: string; port?: number; running: boolean; consumers?: Record<string, unknown> };
type Choice = { svc: string; dedicated: boolean; auto?: boolean };

// sharedIdent is a preview of the server's name for the project's database
// and user (g_<nome>_<hash>; the hash is computed there).
const ident = (p: string) => "g_" + String(p).toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 40) + "_…";

export function useShared(host: string, chk: Check | null) {
  const [items, setItems] = useState<Svc[] | null>(null);
  const [choice, setChoice] = useState<Record<string, Choice>>({});
  const kinds = Object.keys(chk?.uses ?? {});
  const need = kinds.length > 0 && !!host;
  useEffect(() => {
    if (!need) return;
    setItems(null);
    setChoice({});
    api<Svc[]>("/hosts/" + enc(host) + "/shared")
      .then(setItems)
      .catch(() => setItems([]));
  }, [host, need]);
  // The first running service of each kind is picked by default.
  useEffect(() => {
    if (!items) return;
    setChoice((cur) => {
      const next = { ...cur };
      for (const k of kinds) {
        const c = next[k] ?? { svc: "", dedicated: true };
        if (c.auto !== false) {
          const run = items.find((x) => x.kind === k && x.running && x.port);
          if (run && !c.svc) c.svc = run.id;
          c.auto = false;
        }
        next[k] = c;
      }
      return next;
    });
  }, [items, kinds.join(",")]); // eslint-disable-line react-hooks/exhaustive-deps

  // The variables a chosen service fills, with what the field shows.
  const vars = (project: string): Record<string, string> => {
    const out: Record<string, string> = {};
    for (const [k, map] of Object.entries(chk?.uses ?? {})) {
      const cur = choice[k];
      if (!cur?.svc) continue;
      const svc = (items ?? []).find((x) => x.id === cur.svc);
      const ded = cur.dedicated && k !== "redis";
      const id = ident(project || "projeto");
      const show: Record<string, string> = {
        host: "host.containers.internal",
        porta: svc?.port ? String(svc.port) : "",
        usuario: ded ? id : t("(do serviço)"),
        senha: ded ? t("(gerada)") : t("(do serviço)"),
        banco: ded ? id : t("(do serviço)"),
        url: t("(montada ao gerar)"),
      };
      for (const [role, v] of Object.entries(map)) out[v] = show[role] ?? t("(do serviço)");
    }
    return out;
  };

  // provision asks the machine to prepare each chosen service and writes the
  // values into vals (the .env of the project).
  const provision = async (project: string, vals: Record<string, string>) => {
    for (const [k, map] of Object.entries(chk?.uses ?? {})) {
      const cur = choice[k];
      if (!cur?.svc) continue;
      let r: { values: Record<string, string>; dedicated: boolean; service: string };
      try {
        r = await api("/hosts/" + enc(host) + "/shared/" + enc(cur.svc) + "/provision", { method: "POST", body: { project, dedicated: cur.dedicated && k !== "redis" } });
      } catch (e) {
        throw new Error((TITLE[k] ?? k) + ": " + (e as Error).message);
      }
      for (const [role, v] of Object.entries(map)) if (r.values[role] !== undefined) vals[v] = r.values[role];
      gantry.toast(r.dedicated ? t("{title}: banco e usuário prontos em {svc}", { title: TITLE[k] ?? k, svc: r.service }) : t("{title}: usando {svc}", { title: TITLE[k] ?? k, svc: r.service }), "ok");
    }
  };

  const section = (project: string) =>
    !need ? null : (
      <div className="panel panel-pad" style={{ marginTop: 16 }}>
        <h2 style={{ marginBottom: 6 }}>{t("Serviços da máquina")}</h2>
        <p className="muted small" style={{ margin: "0 0 10px" }}>
          {t("Este modelo pode usar um serviço que já roda em {host} em vez de subir o seu.", { host })}
        </p>
        {kinds.map((k) => {
          const cands = (items ?? []).filter((x) => x.kind === k);
          const cur = choice[k] ?? { svc: "", dedicated: true };
          const set = (c: Partial<Choice>) => setChoice((s) => ({ ...s, [k]: { ...cur, ...c } }));
          const title = TITLE[k] ?? k;
                    return (
            <div className="field" key={k}>
              <label>{t("Usar um {title} existente", { title })}</label>
              {items === null ? (
                <span className="muted small">
                  <span className="spin" /> {t("procurando…")}
                </span>
              ) : cands.length ? (
                <select value={cur.svc} onChange={(e) => set({ svc: e.target.value })}>
                  <option value="">{t("Não, informo os dados à mão")}</option>
                  {cands.map((x) => (
                    <option key={x.id} value={x.id} disabled={!x.running || !x.port}>
                      {x.name +
                        " (" +
                        x.title +
                        (x.port ? t(", porta {port}", { port: x.port }) : t(", sem porta publicada")) +
                        (x.running ? "" : t(", parado")) +
                        (Object.keys(x.consumers ?? {}).length ? t(", usado por {list}", { list: Object.keys(x.consumers!).join(", ") }) : "") +
                        ")"}
                    </option>
                  ))}
                </select>
              ) : (
                <p className="hint" style={{ margin: 0 }}>
                  {t("Nenhum {title} rodando nesta máquina. Preencha as variáveis abaixo.", { title })}
                </p>
              )}
              {cur.svc && k !== "redis" ? (
                <label className="check" style={{ display: "flex", gap: 8, alignItems: "center", marginTop: 6 }}>
                  <input type="checkbox" checked={cur.dedicated} onChange={(e) => set({ dedicated: e.target.checked })} />
                  {k === "rabbitmq" ? t("Criar um vhost e um usuário só para este projeto (recomendado)") : t("Criar um banco e um usuário só para este projeto (recomendado)")}
                </label>
              ) : null}
              {cur.svc ? (
                <p className="hint">
                  {cur.dedicated && k !== "redis"
                    ? k === "rabbitmq"
                      ? t('Ao gerar, o Gantry cria o vhost "{name}" com uma senha nova; os dados ficam separados dos outros projetos.', { name: ident(project || "projeto") })
                      : t('Ao gerar, o Gantry cria o banco "{name}" com uma senha nova; os dados ficam separados dos outros projetos.', { name: ident(project || "projeto") })
                    : t("O projeto usa o login do próprio serviço.")}
                </p>
              ) : null}
            </div>
          );
        })}
      </div>
    );

  return { vars, provision, section };
}

// VarFields: the fields of the template's variables. fromShared are filled
// by a shared service (shown, not editable); notes are hints under a field
// (ports in use).
export function VarFields({
  vars,
  vals,
  setVal,
  fromShared,
  notes,
  empty,
}: {
  vars: { name: string; required?: boolean; message?: string }[];
  vals: Record<string, string>;
  setVal: (k: string, v: string) => void;
  fromShared: Record<string, string>;
  notes?: Record<string, { text: string; warn?: boolean } | null>;
  empty: React.ReactNode;
}) {
  if (!vars.length) return <>{empty}</>;
  return (
    <div className="grid2">
      {vars.map((v) => {
        if (fromShared[v.name])
          return (
            <div className="field var-field" key={v.name}>
              <label>
                <span className="mono">{v.name}</span>
                <span className="badge accent">{t("do serviço compartilhado")}</span>
              </label>
              <input disabled value={fromShared[v.name]} />
            </div>
          );
        const secret = /SENHA|PASS|SECRET|TOKEN|KEY/i.test(v.name);
        const note = notes?.[v.name];
        return (
          <div className="field var-field" key={v.name}>
            <label>
              <span className="mono">{v.name}</span>
              {v.required ? <span className="badge warn">{t("obrigatória")}</span> : null}
            </label>
            <input
              type={secret ? "password" : "text"}
              placeholder={v.required ? v.message || t("obrigatório") : ""}
              spellCheck={false}
              autoComplete="off"
              value={vals[v.name] ?? ""}
              onChange={(e) => setVal(v.name, e.target.value)}
            />
            {note ? <p className={"hint port-note" + (note.warn ? " warn" : "")}>{note.text}</p> : v.message && v.required && !notes ? <p className="hint">{v.message}</p> : null}
          </div>
        );
      })}
    </div>
  );
}
