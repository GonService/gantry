// The databases and queues a project uses, and the others of the machine.
import { useEffect, useState } from "react";
import { create } from "zustand";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, labHref } from "@/lib/app";
import { Modal, openDialog } from "@/components/dialog";
import { ErrorBox, Loading } from "@/components/ui";
import { copyText } from "@/lib/format";
import { t } from "@/lib/i18n";
import { newSharedDialog } from "@/views/machine/tabs";
import type { SharedSvc } from "@/views/machine/store";
import type { Lab } from "./types";

type Entry = { items?: SharedSvc[]; err?: string; at: number; loading?: boolean };
const useLSV = create<Record<string, Entry>>(() => ({}));

async function loadLabServices(host: string, force = false) {
  const cur = useLSV.getState()[host];
  if (cur && (cur.loading || (!force && Date.now() - cur.at < 10000))) return;
  useLSV.setState({ [host]: { ...(cur ?? { at: 0 }), loading: true } });
  try {
    useLSV.setState({ [host]: { items: await api<SharedSvc[]>("/hosts/" + enc(host) + "/shared"), at: Date.now() } });
  } catch (e) {
    useLSV.setState({ [host]: { err: (e as Error).message, at: Date.now() } });
  }
}

const SHARED_CLIENT: Record<string, string> = { postgres: "psql", mysql: "mysql", redis: "redis-cli", mongo: "mongosh", rabbitmq: t("filas") };

const KV = ({ k, children }: { k: string; children: React.ReactNode }) => (
  <div>
    <span className="k">{k}</span>
    <span className="v">{children}</span>
  </div>
);

function UseDialog({ host, lab, x, close }: { host: string; lab: Lab; x: SharedSvc; close: () => void }) {
  const [ded, setDed] = useState(x.kind !== "redis");
  const [env, setEnv] = useState(true);
  const [res, setRes] = useState<{ values: Record<string, string>; env?: string[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  if (res) {
    const v = res.values || {};
    const rows = (
      [
        [t("Endereço (dos containers)"), v.host + ":" + v.porta],
        [t("Usuário"), v.usuario],
        [t("Senha"), v.senha],
        [x.kind === "rabbitmq" ? "Vhost" : t("Banco"), v.banco],
        ["URL", v.url],
      ] as [string, string][]
    ).filter(([, val]) => val);
    return (
      <Modal
        title={t("{svc} pronto para {lab}", { svc: x.name, lab: lab.name })}
        onClose={close}
        actions={
          <button className="btn primary" onClick={close}>
            {t("Fechar")}
          </button>
        }
      >
        <div className="kv">
          {rows.map(([k, val]) => (
            <KV key={k} k={k}>
              <span
                className="mono small"
                style={{ cursor: "copy" }}
                title={t("Clique para copiar")}
                onClick={async () => {
                  await copyText(val);
                  gantry.toast(t("{what} copiado", { what: k }), "ok");
                }}
              >
                {val}
              </span>
            </KV>
          ))}
        </div>
        {res.env ? (
          <p className="muted small">
            {t("No .env: {vars}. Use {ref} no compose do projeto e Subir.", { vars: res.env.join(", "), ref: "${" + res.env[0] + "}" })}
          </p>
        ) : (
          <p className="muted small">{t("Guarde a senha agora: ela não aparece de novo aqui.")}</p>
        )}
      </Modal>
    );
  }
  return (
    <Modal
      title={t("Usar {svc} em {lab}?", { svc: x.name, lab: lab.name })}
      onClose={close}
      actions={
        <>
          <button className="btn btn-ghost" onClick={close}>
            {t("Cancelar")}
          </button>
          <button
            className="btn primary"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const r = await api<{ values: Record<string, string>; env?: string[] }>("/hosts/" + enc(host) + "/shared/" + enc(x.id) + "/provision", {
                  method: "POST",
                  body: { project: lab.id, dedicated: ded && x.kind !== "redis", writeEnv: env },
                });
                setRes(r);
                loadLabServices(host, true);
              } catch (e) {
                setErr((e as Error).message);
              }
              setBusy(false);
            }}
          >
            {t("Usar")}
          </button>
        </>
      }
    >
      {x.kind !== "redis" ? (
        <label className="check" style={{ display: "flex", gap: 8, alignItems: "center" }}>
          <input type="checkbox" checked={ded} onChange={(e) => setDed(e.target.checked)} />
          {x.kind === "rabbitmq" ? t("Criar um vhost e um usuário só para este projeto") : t("Criar um banco e um usuário só para este projeto")}
        </label>
      ) : null}
      <label className="check" style={{ display: "flex", gap: 8, alignItems: "center", marginTop: 4 }}>
        <input type="checkbox" checked={env} onChange={(e) => setEnv(e.target.checked)} />
        {t("Gravar a conexão no .env do projeto (senha e URL no chaveiro)")}
      </label>
      {err ? <p className="flash flash-err">{err}</p> : null}
    </Modal>
  );
}

export function LabServicesTab({ host, lab }: { host: string; lab: Lab }) {
  const st = useLSV((s) => s[host]);
  useEffect(() => {
    if (!st || Date.now() - (st.at || 0) > 10000) loadLabServices(host);
  }, [host]); // eslint-disable-line react-hooks/exhaustive-deps
  const client = (x: SharedSvc) =>
    gantry.openTerminal(host, {
      id: x.id,
      name: x.name,
      service: (SHARED_CLIENT[x.kind] || x.kind) + " · " + x.name,
      termURL: "/hosts/" + enc(host) + "/shared/" + enc(x.id) + "/client?project=" + enc(lab.id),
    });
  const head = (
    <div className="banner info">
      <span className="grow">{t("Bancos e filas que este projeto usa e os outros da máquina. O cliente abre logado: com o usuário do projeto quando ele tem um, senão como administrador do serviço.")}</span>
      <button className="btn small primary" onClick={() => newSharedDialog(host)}>
        {t("Novo serviço compartilhado…")}
      </button>
    </div>
  );
  if (!st?.items)
    return (
      <div>
        {head}
        {st?.err ? <ErrorBox>{st.err}</ErrorBox> : <Loading>{t("Procurando serviços…")}</Loading>}
      </div>
    );
  const items = st.items;
  const mine = items.filter((x) => (x.consumers || {})[lab.id]);
  const own = items.filter((x) => x.lab === lab.id && !(x.consumers || {})[lab.id]);
  const others = items.filter((x) => x.lab !== lab.id && !(x.consumers || {})[lab.id]);
  const clientBtn = (x: SharedSvc) => (
    <button className="btn small" disabled={!x.running} onClick={() => client(x)}>
      {t("Abrir {client}", { client: SHARED_CLIENT[x.kind] || t("cliente") })}
    </button>
  );
  const card = (x: SharedSvc, actions: React.ReactNode, extra?: React.ReactNode) => (
    <div className="panel panel-pad svc-card" key={x.id}>
      <div style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap" }}>
        <span className="state">
          <span className={"dot" + (x.running ? " on" : "")} />
          <strong>{x.name}</strong>
        </span>
        <span className="badge">{x.title}</span>
        {x.port ? <span className="badge mono">:{x.port}</span> : <span className="badge warn">{t("porta não publicada")}</span>}
        {x.lab ? (
          <a className="muted small" href={labHref(host, x.lab)}>
            {t("projeto {name}", { name: x.lab })}
          </a>
        ) : null}
        <span className="grow" />
        {actions}
      </div>
      {extra}
    </div>
  );
  const section = (title: string, list: SharedSvc[], mk: (x: SharedSvc) => React.ReactNode) =>
    list.length ? (
      <div className="block" key={title}>
        <div className="block-head">
          <h2 className="block-title">{title}</h2>
        </div>
        {list.map(mk)}
      </div>
    ) : null;
  const secs = [
    section(t("Usados por este projeto"), mine, (x) => {
      const u = x.consumers![lab.id];
      return card(
        x,
        clientBtn(x),
        <div className="kv" style={{ marginTop: 8 }}>
          <KV k={t("Dos containers")}>
            <span className="mono small">host.containers.internal:{x.port || "?"}</span>
          </KV>
          {u.db ? (
            <KV k={x.kind === "rabbitmq" ? "Vhost" : t("Banco")}>
              <span className="mono small">{u.db}</span>
            </KV>
          ) : null}
          {u.user ? (
            <KV k={t("Usuário")}>
              <span className="mono small">{u.user}</span>
            </KV>
          ) : null}
        </div>,
      );
    }),
    section(t("Do próprio projeto"), own, (x) =>
      card(
        x,
        clientBtn(x),
        Object.keys(x.consumers || {}).length ? (
          <p className="muted small" style={{ margin: "6px 0 0" }}>
            {t("Compartilhado com: {list}", { list: Object.keys(x.consumers!).join(", ") })}
          </p>
        ) : null,
      ),
    ),
    section(t("Outros na máquina"), others, (x) =>
      card(
        x,
        <button
          className="btn small primary"
          disabled={!x.running || !x.port}
          title={x.port ? "" : t("Publique a porta do serviço para outros projetos chegarem nele")}
          onClick={() => openDialog((close) => <UseDialog host={host} lab={lab} x={x} close={close} />)}
        >
          {t("Usar neste projeto…")}
        </button>,
      ),
    ),
  ].filter(Boolean);
  return (
    <div>
      {head}
      {secs.length ? (
        secs
      ) : (
        <div className="panel panel-pad">
          <p className="muted" style={{ margin: 0 }}>
            {t("Nenhum banco ou fila nesta máquina ainda. Use Novo serviço compartilhado para criar um.")}

          </p>
        </div>
      )}
    </div>
  );
}
