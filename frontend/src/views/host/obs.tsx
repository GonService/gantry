// Observability of a host (Prometheus, Loki, Grafana found in its
// containers or configured), the Gantry agent on a server, and the other
// engines of a server.
import { useEffect, useState } from "react";
import { create } from "zustand";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, hostHref, RT_NAME, type Host } from "@/lib/app";
import { confirm, openDialog, Modal } from "@/components/dialog";
import { Loading } from "@/components/ui";
import { LineChart, Legend } from "@/components/chart";
import { fmtNum } from "@/lib/series";
import { t, LOCALE } from "@/lib/i18n";

type ObsSvc = { ok: boolean; version?: string; url: string; source: string; container?: string; lab?: string; error?: string };
export type Obs = { services: Record<string, ObsSvc>; config: Record<string, string>; error?: string };

export const useObs = create<Record<string, Obs | null>>(() => ({}));

export async function loadObs(host: string, fresh = false): Promise<Obs> {
  let o: Obs;
  try {
    o = await api<Obs>("/hosts/" + enc(host) + "/obs" + (fresh ? "?fresh=1" : ""));
  } catch (e) {
    o = { services: {}, config: {}, error: (e as Error).message };
  }
  useObs.setState({ [host]: o });
  return o;
}
export const obsOK = (host: string, kind: string) => !!useObs.getState()[host]?.services[kind]?.ok;

export async function obsOpen(host: string, kind: string, path = "") {
  try {
    await api("/hosts/" + enc(host) + "/obs/open", { method: "POST", body: { kind, path } });
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

const OBS_LABEL: Record<string, string> = { prometheus: "Prometheus", loki: "Loki", grafana: "Grafana" };

const KV = ({ k, children }: { k: string; children: React.ReactNode }) => (
  <div>
    <span className="k">{k}</span>
    <span className="v">{children}</span>
  </div>
);

export function ObsBlock({ host }: { host: string }) {
  const o = useObs((s) => s[host]);
  useEffect(() => {
    if (o === undefined) loadObs(host);
  }, [host]); // eslint-disable-line react-hooks/exhaustive-deps
  const head = (
    <div className="block-head">
      <h2 className="block-title">{t("Observabilidade")}</h2>
      <span className="grow" />
      <button
        className="btn small"
        onClick={() => {
          useObs.setState({ [host]: null });
          loadObs(host, true);
        }}
      >
        {t("Procurar de novo")}
      </button>
      <button className="btn small" onClick={() => obsDialog(host, o?.config ?? {})}>
        {t("Configurar")}
      </button>
    </div>
  );
  if (!o)
    return (
      <div className="block">
        {head}
        <Loading>{t("Procurando Prometheus, Loki e Grafana…")}</Loading>
      </div>
    );
  const found = Object.keys(o.services).length;
  return (
    <div className="block">
      {head}
      <div className="panel panel-pad kv">
        {["prometheus", "loki", "grafana"].map((k) => {
          const x = o.services[k];
          if (!x)
            return (
              <KV key={k} k={OBS_LABEL[k]}>
                <span className="muted">{t("não encontrado")}</span>
              </KV>
            );
          const src = x.source === "config" ? t("configurado") : x.lab ? t("container {name} · projeto {lab}", { name: x.container, lab: x.lab }) : t("container {name}", { name: x.container });
          return (
            <KV key={k} k={OBS_LABEL[k]}>
              <span className="obs-val">
                <span className="state">
                  <span className={"dot " + (x.ok ? "on" : "err")} />
                  {x.ok ? (x.version ? "v" + x.version : t("respondendo")) : t("sem resposta")}
                </span>
                <span className="mono">{x.url}</span>
                <span className="muted small">{src}</span>
                {x.ok && k !== "loki" ? (
                  <button className="btn small" onClick={() => obsOpen(host, k)}>
                    {t("Abrir")}
                  </button>
                ) : null}
                {!x.ok && x.error ? (
                  <span className="small obs-err" title={x.error}>
                    {x.error.slice(0, 80)}
                  </span>
                ) : null}
              </span>
            </KV>
          );
        })}
      </div>
      {found ? null : (
        <p className="muted small">{t("Nada encontrado nos containers desta máquina. Gere um projeto com o modelo Prometheus + Grafana (ou veja examples/monitoramento) ou informe os endereços em Configurar.")}</p>
      )}
      {o.services.grafana?.ok ? <GrafanaList host={host} /> : null}
    </div>
  );
}

function GrafanaList({ host }: { host: string }) {
  const [ds, setDs] = useState<{ title: string; url: string; folder?: string }[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    api<{ title: string; url: string; folder?: string }[]>("/hosts/" + enc(host) + "/obs/grafana/dashboards")
      .then(setDs)
      .catch((e) => setErr((e as Error).message));
  }, [host]);
  return (
    <div className="grafana-list">
      {err ? (
        <span className="small obs-err">{err}</span>
      ) : !ds ? (
        <span className="muted small">{t("Carregando dashboards…")}</span>
      ) : !ds.length ? (
        <span className="muted small">{t("O Grafana ainda não tem dashboards.")}</span>
      ) : (
        <>
          <span className="muted small">{t("Dashboards do Grafana")}</span>
          <div className="grafana-chips">
            {ds.slice(0, 24).map((d, i) => (
              <button key={i} className="port-chip" title={d.folder || ""} onClick={() => obsOpen(host, "grafana", d.url)}>
                {d.title}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function obsDialog(host: string, cfg: Record<string, string>) {
  openDialog((close) => <ObsForm host={host} cfg={cfg} close={close} />);
}

function ObsForm({ host, cfg, close }: { host: string; cfg: Record<string, string>; close: () => void }) {
  const [v, setV] = useState({ prometheus: cfg.prometheus ?? "", loki: cfg.loki ?? "", grafana: cfg.grafana ?? "", grafanaToken: cfg.grafanaToken ?? "" });
  const [err, setErr] = useState("");
  const field = (id: keyof typeof v, label: string, ph: string, type = "text") => (
    <div className="field">
      <label htmlFor={"ob-" + id}>{label}</label>
      <input id={"ob-" + id} type={type} placeholder={ph} autoComplete="off" spellCheck={false} value={v[id]} onChange={(e) => setV({ ...v, [id]: e.target.value })} />
    </div>
  );
  return (
    <Modal
      title={t("Observabilidade · {host}", { host })}
      onClose={close}
      onSubmit={async (e) => {
        e.preventDefault();
        try {
          await api("/hosts/" + enc(host) + "/obs", { method: "PUT", body: { prometheus: v.prometheus.trim(), loki: v.loki.trim(), grafana: v.grafana.trim(), grafanaToken: v.grafanaToken.trim() } });
          close();
          loadObs(host, true);
        } catch (x) {
          setErr((x as Error).message);
        }
      }}
      actions={
        <>
          <button type="button" className="btn btn-ghost" onClick={close}>
            {t("Cancelar")}
          </button>
          <button type="submit" className="btn primary">
            {t("Salvar")}
          </button>
        </>
      }
    >
      <p className="hint" style={{ marginTop: 0 }}>
        {t("Deixe em branco para usar o que o Gantry encontrar nos containers. Os endereços são vistos da máquina do host: num servidor, http://127.0.0.1:9090 é o Prometheus do servidor.")}
      </p>
      {field("prometheus", "Prometheus", "http://127.0.0.1:9090")}
      {field("loki", "Loki", "http://127.0.0.1:3100")}
      {field("grafana", "Grafana", "http://127.0.0.1:3000")}
      {field("grafanaToken", t("Token do Grafana (opcional)"), "glsa_…", "password")}
      {err ? <p className="flash flash-err">{err}</p> : null}
    </Modal>
  );
}

// PromQuery charts any PromQL over the tab's period.
export function PromQuery({ host, lab, range }: { host: string; lab: string; range: number }) {
  const def = 'sum by (name) (rate(container_cpu_usage_seconds_total{container_label_com_docker_compose_project="' + lab + '"}[1m])) * 100';
  const [q, setQ] = useState(() => gantry.store.get("promql." + lab, def));
  const [res, setRes] = useState<{ series: { label: string; slot: number; step: number; points: { t: number; v: number }[] }[]; until: number; msg: string } | null>(null);
  const [msg, setMsg] = useState("");
  const run = async () => {
    const qq = q.trim();
    if (!qq) return;
    gantry.store.set("promql." + lab, qq);
    setMsg(t("Consultando…"));
    try {
      const r = await api<{ step: number; series: { labels: Record<string, string>; points: [number, number][] }[] }>("/hosts/" + enc(host) + "/obs/prom?q=" + enc(qq) + "&range=" + range);
      const series = r.series.slice(0, 8).map((x, i) => {
        const lbl =
          Object.entries(x.labels)
            .filter(([k]) => k !== "__name__")
            .map(([k, v]) => k + "=" + v)
            .join(", ") ||
          x.labels.__name__ ||
          t("valor");
        return { label: lbl, slot: i + 1, step: r.step, points: x.points.map(([t, v]) => ({ t, v })) };
      });
      setRes({ series, until: Math.floor(Date.now() / 1000), msg: "" });
      setMsg(r.series.length ? (r.series.length > 8 ? t("Mostrando 8 de {n} séries.", { n: r.series.length }) : "") : t("A consulta não retornou dados neste período."));
    } catch (e) {
      setRes(null);
      setMsg((e as Error).message);
    }
  };
  useEffect(() => {
    run();
  }, [range]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <section className="panel panel-pad promql">
      <h2 className="chart-title">{t("Consulta no Prometheus")}</h2>
      <form
        className="row-form"
        style={{ margin: "8px 0 10px" }}
        onSubmit={(e) => {
          e.preventDefault();
          run();
        }}
      >
        <input className="input" id="promql" spellCheck={false} aria-label={t("Consulta PromQL")} value={q} onChange={(e) => setQ(e.target.value)} />
        <button className="btn" type="submit">
          {t("Executar")}
        </button>
      </form>
      {res ? (
        <>
          <Legend series={res.series} />
          <LineChart series={res.series as never} val={(p) => p.v} fmt={fmtNum} t0={res.until - range} t1={res.until} />
        </>
      ) : null}
      <p className="muted small" style={{ margin: "6px 0 0" }}>
        {msg}
      </p>
    </section>
  );
}

/* agent */

type Agent = {
  state: string;
  version?: string;
  arch?: string;
  protocol?: number;
  direct?: string;
  via?: string;
  engineVia?: string;
  engineOk?: boolean;
  metricsSince?: number;
  vms?: boolean;
  outdated?: boolean;
  bundled?: boolean;
};

export function AgentBlock({ hst }: { hst: Host }) {
  const a = hst.agent as Agent;
  const [direct, setDirect] = useState(true);
  const run = async (op: string, label: string, btn: HTMLButtonElement, body?: unknown) => {
    if (op === "remove" && !(await confirm(t("Remover o agente de \"{name}\"?", { name: hst.name }), t("O serviço é parado e o histórico de métricas guardado no servidor é apagado. Os containers e as VMs não são afetados."), t("Remover"), true))) return;
    btn.disabled = true;
    try {
      const r = await api<{ id: string }>("/hosts/" + enc(hst.name) + "/agent/" + op, { method: "POST", body: body || {} });
      gantry.openJob(r.id, label + " · " + hst.name, async () => {
        await gantry.refreshHost(hst.name);
      });
    } catch (e) {
      gantry.toast((e as Error).message, "err");
      btn.disabled = false;
    }
  };
  const btn = (op: string, label: string, cls = "", title?: string, body?: unknown) => (
    <button className={"btn " + cls} title={title} onClick={(e) => run(op, label, e.currentTarget, body)}>
      {label}
    </button>
  );
  const head = (
    <div className="block-head">
      <h2 className="block-title">{t("Agente")}</h2>
    </div>
  );
  if (a.state === "running") {
    const since = a.metricsSince ? new Date(a.metricsSince * 1000) : null;
    const viaTxt = (v?: string) => (v === "tls" ? t("direto (TCP + TLS mútuo)") : v === "ssh" ? t("pelo SSH") : "—");
    const conn = a.direct ? (
      a.via === "tls" ? (
        <span>
          <span className="dot on" /> {t("direta em")} <code>{a.direct}</code> {t("· engine {via}", { via: viaTxt(a.engineVia || "ssh") })}
        </span>
      ) : (
        <span>
          <span className="dot warn" /> {t("pareada em")} <code>{a.direct}</code>{t(", mas a porta não respondeu: usando SSH (libere a porta no firewall do servidor)")}
        </span>
      )
    ) : (
      t("pelo SSH (socket encaminhado)")
    );
    return (
      <div className="block">
        {head}
        {a.outdated ? (
          <div className="banner info">
            <span className="grow">{t("Este app tem um agente com recursos novos (métricas das VMs e conexão direta). O servidor está com a {version}.", { version: a.version })}</span>
            {btn("update", t("Atualizar agente"))}
          </div>
        ) : null}
        <div className="panel panel-pad kv">
          <KV k={t("Estado")}>
            <span className="state">
              <span className="dot on" />
              {t("rodando")}
            </span>
          </KV>
          <KV k={t("Versão")}>{a.version + (a.arch ? " · linux/" + a.arch : "") + (a.protocol ? t(" · protocolo {n}", { n: a.protocol }) : "")}</KV>
          <KV k={t("Conexão")}>{conn}</KV>
          <KV k="Engine">{a.engineOk ? t("encontrado") : t("não encontrado pelo agente")}</KV>
          <KV k={t("Métricas desde")}>{(since ? since.toLocaleString(LOCALE, { dateStyle: "short", timeStyle: "short" }) : "—") + (a.vms ? t(" · containers e VMs") : t(" · só containers"))}</KV>
          <KV k={t("Arquivos dos projetos")}>{t("acompanhados na hora (inotify)")}</KV>
        </div>
        <div className="actions" style={{ marginTop: 10 }}>
          {a.outdated ? null : btn("update", t("Reinstalar"), "", t("Envia o agente deste app de novo e reinicia o serviço"))}
          {(a.protocol ?? 0) >= 2
            ? a.direct
              ? btn("direct", t("Desligar conexão direta"), "", t("Volta a usar só o SSH; a porta deixa de ser aberta"), { direct: false })
              : btn("direct", t("Ligar conexão direta"), "", t("O agente abre a porta TCP 7781 com TLS mútuo; só este app (pareado pelo SSH) consegue conectar"), { direct: true })
            : null}
          {btn("remove", t("Remover agente"), "danger")}
        </div>
      </div>
    );
  }
  return (
    <div className="block">
      {head}
      <div className="panel panel-pad">
        <p style={{ margin: "0 0 8px" }}>
          {t("Opcional. Um serviço pequeno que roda com o seu usuário no servidor (systemd")} <code>--user</code>).
        </p>
        <ul className="muted small agent-gains">
          <li>{t("guarda 24 h de CPU e memória dos containers e das VMs, mesmo com o app fechado;")}</li>
          <li>{t("avisa na hora quando o compose muda no disco, sem ler o arquivo a cada 3 s;")}</li>
          <li>{t("com a conexão direta, a API do engine e do agente vai por TCP com TLS mútuo (porta 7781), sem passar pelo SSH. O SSH continua para comandos e como reserva.")}</li>
        </ul>
        <label className="check" style={{ margin: "4px 0 10px", display: "flex", gap: 8, alignItems: "center" }}>
          <input type="checkbox" checked={direct} onChange={(e) => setDirect(e.target.checked)} />
          {t("Conexão direta (TCP 7781, TLS mútuo, pareada pelo SSH)")}
        </label>
        <div className="actions">
          {a.bundled ? (
            <button className="btn primary" onClick={(e) => run("install", t("Instalar agente"), e.currentTarget, { direct })}>
              {t("Instalar agente")}
            </button>
          ) : (
            <span className="muted small">{t("Este build não inclui o agente. Compile o app com make para incluí-lo.")}</span>
          )}
        </div>
      </div>
    </div>
  );
}

/* other engines of a server */

type EngineFound = { runtime: string; socket: string; main?: boolean; host?: string };
const useEngines = create<Record<string, EngineFound[]>>(() => ({}));

export async function engineChoices(host: string): Promise<EngineFound[]> {
  const cur = useEngines.getState()[host];
  if (cur && cur.length) return cur;
  try {
    const l = await api<EngineFound[]>("/hosts/" + enc(host) + "/engines");
    useEngines.setState({ [host]: l });
    return l;
  } catch {
    return [];
  }
}

// EnginesList shows the other engines found on a server (Podman next to
// Docker, containerd through the agent) and adds them as hosts.
export function EnginesList({ hst }: { hst: Host }) {
  const list = useEngines((s) => s[hst.name]);
  useEffect(() => {
    if (!list) engineChoices(hst.name);
  }, [hst.name]); // eslint-disable-line react-hooks/exhaustive-deps
  const others = (list || []).filter((e) => !e.main);
  if (!others.length) return null;
  return (
    <div className="engines-list">
      <span className="muted small">{t("Outros engines neste servidor")}</span>
      {others.map((e) => (
        <div className="engine-row" key={e.socket}>
          <b>{RT_NAME[e.runtime] || e.runtime}</b>
          <span className="mono small muted">{e.socket}</span>
          {e.host ? (
            <a className="small" href={hostHref(e.host)}>
              {t("já aparece como {name}", { name: e.host })}
            </a>
          ) : (
            <button
              className="btn small"
              onClick={async (ev) => {
                ev.currentTarget.disabled = true;
                try {
                  const name = hst.name + " · " + e.runtime;
                  await api("/hosts", { method: "POST", body: { name, url: hst.url, socket: e.socket } });
                  useEngines.setState({ [hst.name]: undefined as never });
                  await gantry.loadHosts(true);
                  location.hash = hostHref(name);
                } catch (err) {
                  gantry.toast((err as Error).message, "err");
                }
              }}
            >
              {t("Adicionar como host")}
            </button>
          )}
        </div>
      ))}
    </div>
  );
}
