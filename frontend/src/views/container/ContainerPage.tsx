// The page of a container: state and actions, summary, limits, usage,
// ports, volumes, networks, environment, labels and the raw inspect.
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { ctrHref, enc, hostHref, labHref, rtQuery, useApp, type Host } from "@/lib/app";
import { copyText, fmtBytes } from "@/lib/format";
import { confirm } from "@/components/dialog";
import { Loading } from "@/components/ui";
import { ensureWeb, openWeb, useWeb, webURL } from "@/views/shared/web";
import { UsagePanel } from "@/views/lab/metrics";
import { ctrCommitDialog, ctrFilesDialog, ctrResourcesDialog, eventsDialog } from "./tools";
import { joinCmd } from "./form";
import { LOCALE, t } from "@/lib/i18n";

/* eslint-disable @typescript-eslint/no-explicit-any */
type Data = { inspect: any; spec: any };

export function ContainerPage({ host, rt, id }: { host: string; rt: string; id: string }) {
  const S = useApp();
  const [d, setD] = useState<Data | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const cur = useRef("");
  const key = host + "|" + rt + "|" + id;
  const load = async () => {
    const my = key;
    try {
      const r = await api<Data>("/hosts/" + enc(host) + "/containers/" + enc(id) + rtQuery(rt));
      if (cur.current !== my) return;
      setD((old) => (old && JSON.stringify(old) === JSON.stringify(r) ? old : r));
      setErr("");
    } catch (e) {
      if (cur.current !== my) return;
      setErr((e as Error).message);
      setD(null);
    }
  };
  useEffect(() => {
    cur.current = key;
    setD(null);
    setErr("");
    load();
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  // The projects of the host are read again after engine events: read the
  // container again too.
  const labs = S.labs[host];
  useEffect(() => {
    if (d) load();
  }, [labs]); // eslint-disable-line react-hooks/exhaustive-deps
  const hst = S.hosts.find((x) => x.name === host);
  if (!hst) return null;
  if (err)
    return (
      <div className="empty">
        <h1>{t("Container não encontrado")}</h1>
        <p>{err}</p>
        <a className="btn" href={hostHref(host)}>
          {t("Ver o host")}
        </a>
      </div>
    );
  if (!d) return <Loading />;
  return <View hst={hst} rt={rt} d={d} busy={busy} setBusy={setBusy} reload={load} />;
}

const KV = ({ k, children }: { k: string; children: React.ReactNode }) => (
  <div>
    <span className="k">{k}</span>
    <span className="v">{children}</span>
  </div>
);

function Table({ head, rows }: { head: string[]; rows: React.ReactNode[][] }) {
  return (
    <div className="table-wrap">
      <table className="grid compact">
        <thead>
          <tr>
            {head.map((h, i) => (
              <th key={i}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {r.map((v, j) => (
                <td key={j} className={j ? "mono" : "mono strong"}>
                  {v}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Section({ title, folded, children }: { title: string; folded?: boolean; children: React.ReactNode }) {
  if (folded)
    return (
      <details className="panel ctr-sec">
        <summary>{title}</summary>
        {children}
      </details>
    );
  return (
    <div className="panel ctr-sec">
      <h2>{title}</h2>
      {children}
    </div>
  );
}

const Empty = ({ children }: { children: React.ReactNode }) => (
  <p className="muted" style={{ margin: 0 }}>
    {children}
  </p>
);

function View({ hst, rt, d, busy, setBusy, reload }: { hst: Host; rt: string; d: Data; busy: string; setBusy: (s: string) => void; reload: () => Promise<void> }) {
  const ins = d.inspect,
    sp = d.spec;
  const st = ins.State || {};
  const name = (ins.Name || "").replace(/^\//, "") || ins.Id.slice(0, 12);
  const labels: Record<string, string> = (ins.Config && ins.Config.Labels) || {};
  const lab = labels["com.docker.compose.project"];
  const svc = labels["com.docker.compose.service"];
  const c = { id: ins.Id, name, service: svc, runtime: rt };
  const running = !!st.Running && !st.Paused;
  const state = st.Paused ? "paused" : st.Status || (st.Running ? "running" : "exited");
  const dot = state === "running" ? "on" : state === "paused" || state === "restarting" ? "warn" : state === "dead" ? "err" : "";
  const base = "/hosts/" + enc(hst.name) + "/containers/" + enc(ins.Id);
  const web = useWeb((s) => s[hst.name]);
  useEffect(() => {
    if (!rt) ensureWeb(hst.name);
  }, [hst.name, rt]);

  const act = (action: string, label: string, cls = "") => (
    <button
      className={"btn " + cls}
      disabled={!!busy}
      onClick={async () => {
        setBusy(action);
        try {
          await api(base + "/" + action + rtQuery(rt), { method: "POST" });
        } catch (e) {
          gantry.toast((e as Error).message, "err");
        }
        setBusy("");
        await reload();
        gantry.loadLabs(hst.name);
      }}
    >
      {busy === action ? (
        <>
          <span className="spin" />
          {label}…
        </>
      ) : (
        label
      )}
    </button>
  );

  const cfg = ins.Config || {};
  const hc = ins.HostConfig || {};
  const when = (s: string) => (s && !s.startsWith("0001") ? new Date(s).toLocaleString(LOCALE) : "—");
  const restart = sp.restart ? sp.restart + (sp.restartMax ? t(" (até {n})", { n: sp.restartMax }) : "") : t("nunca");

  // Ports, as published right now.
  const ports: [string, string][] = [];
  const live = (ins.NetworkSettings && ins.NetworkSettings.Ports) || {};
  for (const [k, binds] of Object.entries(live) as [string, any[] | null][]) {
    if (!binds || !binds.length) {
      ports.push([k, "—"]);
      continue;
    }
    const seen = new Set<string>();
    for (const b of binds) {
      const hp = (b.HostIp && b.HostIp !== "0.0.0.0" && b.HostIp !== "::" ? b.HostIp + ":" : "") + b.HostPort;
      if (!seen.has(hp)) {
        seen.add(hp);
        ports.push([k, hp]);
      }
    }
  }
  const apps = rt ? [] : Object.values(web?.apps || {}).filter((x) => x.owner === name);
  const mounts = (ins.Mounts || []).map((m: any) => [m.Type, m.Type === "volume" ? m.Name : m.Source, m.Destination, m.RW === false ? t("leitura") : t("leitura e escrita")]);
  const env = (sp.env || []).map((e: string) => {
    const i = e.indexOf("=");
    return i < 0 ? [e, ""] : [e.slice(0, i), e.slice(i + 1)];
  });
  const lbl = Object.entries(labels).sort();
  const json = JSON.stringify(ins, null, 2);

  return (
    <div id="ctr-view">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={hostHref(hst.name)}>{hst.name}</a>
            <span aria-hidden="true">/</span>
            <a href={labHref(hst.name, lab || "_avulsos")}>{lab || t("Avulsos")}</a>
            <span aria-hidden="true">/</span>
            <span>Container</span>
          </nav>
          <h1>
            <span className={"dot " + dot} /> {svc || name}
          </h1>
          <div className="badges">
            <span className={"badge" + (running ? " accent" : "")}>{state}</span>
            {svc ? <span className="badge mono">{name}</span> : null}
            {rt ? <span className={"rt-chip rt-" + rt}>{rt}</span> : null}
          </div>
        </div>
        <div className="actions">
          {running ? (
            <button className="btn" onClick={() => gantry.openTerminal(hst.name, c)}>
              Shell
            </button>
          ) : null}
          <button className="btn" onClick={() => gantry.openLogs(hst.name, c)}>
            Logs
          </button>
          {st.Paused ? act("unpause", t("Retomar"), "primary") : running ? act("stop", t("Parar")) : act("start", t("Iniciar"), "primary")}
          {running ? act("restart", t("Reiniciar")) : null}
          {running ? act("pause", t("Pausar")) : null}
          {running ? act("kill", t("Matar"), "danger") : null}
        </div>
      </div>
      <div className="ctr-toolbar">
        <button className="btn small" onClick={() => ctrFilesDialog(hst.name, c, rt)}>
          {t("Arquivos")}
        </button>
        <button className="btn small" title={t("CPU, memória e reinício, sem recriar")} onClick={() => ctrResourcesDialog(hst.name, hst.runtime, ins, rt, reload)}>
          {t("Recursos…")}
        </button>
        <button className="btn small" onClick={() => eventsDialog(hst.name, { container: ins.Id, rt, title: t("Eventos de {name}", { name: svc || name }) })}>
          {t("Eventos")}
        </button>
        <button className="btn small" title={t("Gravar o estado atual numa imagem")} onClick={() => ctrCommitDialog(hst.name, ins, rt)}>
          {t("Criar imagem…")}
        </button>
        <a className="btn small" href={ctrHref(hst.name, rt, ins.Id, "editar")}>
          {t("Editar e recriar")}
        </a>
        <a className="btn small" href={ctrHref(hst.name, rt, ins.Id, "duplicar")}>
          {t("Duplicar")}
        </a>
        <button
          className="btn small danger"
          onClick={async () => {
            const msg = running ? t("O container está rodando e será parado antes. Volumes nomeados são mantidos.") : t("Volumes nomeados são mantidos.");
            if (!(await confirm(t("Remover o container \"{name}\"?", { name }), msg, t("Remover"), true))) return;
            try {
              await api(base + rtQuery(rt), { method: "DELETE" });
              gantry.toast(t("Container {name} removido", { name }), "ok");
              gantry.loadLabs(hst.name);
              location.hash = labHref(hst.name, lab || "_avulsos");
            } catch (e) {
              gantry.toast((e as Error).message, "err");
            }
          }}
        >
          {t("Remover")}
        </button>
        <span className="grow" />
        <button className="btn small btn-ghost" onClick={() => reload()}>
          {t("Atualizar")}
        </button>
      </div>
      <div className="ctr-grid">
        <div className="panel panel-pad">
          <h2>{t("Resumo")}</h2>
          <div className="kv">
            <KV k="ID">{ins.Id.slice(0, 12)}</KV>
            <KV k={t("Imagem")}>{sp.image || cfg.Image || "—"}</KV>
            <KV k={t("Criado")}>{when(ins.Created)}</KV>
            <KV k={running ? t("Iniciado") : t("Parou")}>{running ? when(st.StartedAt) : when(st.FinishedAt) + (st.ExitCode != null && !running ? t(" · código {code}", { code: st.ExitCode }) : "")}</KV>
            <KV k={t("Comando")}>{joinCmd([...(sp.entrypoint || []), ...(sp.cmd || [])]) || "—"}</KV>
            {sp.workingDir ? <KV k={t("Diretório")}>{sp.workingDir}</KV> : null}
            {sp.user ? <KV k={t("Usuário")}>{sp.user}</KV> : null}
            <KV k={t("Reiniciar")}>{restart}</KV>
            <KV k={t("Rede")}>{sp.network || t("padrão")}</KV>
            {st.Health ? <KV k={t("Saúde")}>{st.Health.Status}</KV> : null}
          </div>
        </div>
        <div className="panel panel-pad">
          <h2>{t("Recursos e segurança")}</h2>
          <div className="kv">
            <KV k={t("Memória")}>{sp.memory ? fmtBytes(sp.memory) : t("sem limite")}</KV>
            <KV k="CPUs">{sp.cpus ? String(sp.cpus) : t("sem limite")}</KV>
            {sp.shmSize ? <KV k="/dev/shm">{fmtBytes(sp.shmSize)}</KV> : null}
            <KV k={t("Privilegiado")}>{sp.privileged ? t("sim") : t("não")}</KV>
            {sp.readOnly ? <KV k={t("Sistema de arquivos")}>{t("somente leitura")}</KV> : null}
            {sp.init ? <KV k="Init">{t("sim")}</KV> : null}
            {(sp.capAdd || []).length ? <KV k={t("Capacidades extras")}>{sp.capAdd.join(", ")}</KV> : null}
            {(sp.capDrop || []).length ? <KV k={t("Capacidades removidas")}>{sp.capDrop.join(", ")}</KV> : null}
            {(sp.devices || []).length ? <KV k={t("Dispositivos")}>{sp.devices.join(", ")}</KV> : null}
            {hc.LogConfig && hc.LogConfig.Type ? <KV k="Logs">{hc.LogConfig.Type}</KV> : null}
          </div>
        </div>
      </div>
      {!rt ? <UsagePanel host={hst.name} id={ins.Id} memLimit={sp.memory} running={running} /> : null}
      <Section title={t("Portas")}>
        {apps.length ? (
          <div className="apps-bar">
            {apps.map((x) => (
              <button key={x.port} type="button" className="btn small" title={x.title || undefined} onClick={() => openWeb(hst.name, x.port, x.path)}>
                {t("Abrir :{port}{path} ↗", { port: x.port, path: x.path || "" })}
              </button>
            ))}
            <span className="muted small">{t("Na rede: {urls}", { urls: apps.map((x) => webURL(hst.name, x)).join(", ") })}</span>
          </div>
        ) : null}
        {ports.length ? <Table head={[t("No container"), t("No host")]} rows={ports} /> : <Empty>{t("Nenhuma porta exposta.")}</Empty>}
      </Section>
      <Section title={t("Volumes")}>{mounts.length ? <Table head={[t("Tipo"), t("Origem"), t("No container"), t("Acesso")]} rows={mounts} /> : <Empty>{t("Nenhum volume montado.")}</Empty>}</Section>
      <Networks hst={hst} ins={ins} base={base} rt={rt} reload={reload} />
      <Section title={t("Variáveis de ambiente")}>{env.length ? <Table head={[t("Nome"), t("Valor")]} rows={env} /> : <Empty>{t("Nenhuma.")}</Empty>}</Section>
      <Section title="Labels" folded>
        {lbl.length ? <Table head={[t("Nome"), t("Valor")]} rows={lbl} /> : <Empty>{t("Nenhum.")}</Empty>}
      </Section>
      <details className="panel ctr-raw">
        <summary>Inspect (JSON)</summary>
        <div className="raw-tools">
          <button className="btn small" onClick={async () => gantry.toast((await copyText(json)) ? t("Copiado") : t("Não consegui copiar"), "ok")}>
            {t("Copiar")}
          </button>
        </div>
        <pre className="code">{json}</pre>
      </details>
    </div>
  );
}

function Networks({ hst, ins, base, rt, reload }: { hst: Host; ins: any; base: string; rt: string; reload: () => Promise<void> }) {
  const mode: string = (ins.HostConfig && ins.HostConfig.NetworkMode) || "";
  const nets: Record<string, any> = (ins.NetworkSettings && ins.NetworkSettings.Networks) || {};
  const shared = mode === "host" || mode === "none" || mode.startsWith("container:");
  const [all, setAll] = useState<string[] | null>(null);
  const [sel, setSel] = useState("");
  const [ip, setIp] = useState("");
  useEffect(() => {
    if (shared) return;
    api<{ name: string }[]>("/hosts/" + enc(hst.name) + "/res/networks" + rtQuery(rt))
      .then((l) => setAll(l.map((n) => n.name)))
      .catch(() => setAll([]));
  }, [hst.name, rt, shared]);
  const free = (all || []).filter((n) => !nets[n] && n !== "host" && n !== "none");
  const op = async (kind: string, body: { network: string; ipv4?: string }) => {
    try {
      await api(base + "/net/" + kind + rtQuery(rt), { method: "POST", body });
      gantry.toast(kind === "connect" ? t("Conectado a {net}", { net: body.network }) : t("Desconectado de {net}", { net: body.network }), "ok");
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
    await reload();
    gantry.loadLabs(hst.name);
  };
  const rows = Object.entries(nets);
  return (
    <Section title={t("Redes")}>
      {shared ? (
        <p className="hint" style={{ margin: "0 0 8px" }}>
          {t("Usa a rede {mode}; não dá para conectar a outras.", { mode })}
        </p>
      ) : null}
      {rows.length ? (
        <div className="table-wrap">
          <table className="grid compact">
            <thead>
              <tr>
                {[t("Rede"), "IP", "Gateway", "MAC", ""].map((h, i) => (
                  <th key={i}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map(([n, v]) => (
                <tr key={n}>
                  <td className="mono strong">{n}</td>
                  <td className="mono">{v.IPAddress || "—"}</td>
                  <td className="mono">{v.Gateway || "—"}</td>
                  <td className="mono">{v.MacAddress || "—"}</td>
                  <td className="act">
                    {shared ? null : (
                      <button className="btn small" onClick={() => op("disconnect", { network: n })}>
                        {t("Desconectar")}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty>{t("Nenhuma rede.")}</Empty>
      )}
      {!shared ? (
        <div className="inline-form">
          <span className="muted">{t("Conectar a")}</span>
          <select aria-label={t("Rede")} value={sel || free[0] || ""} onChange={(e) => setSel(e.target.value)}>
            {all === null ? <option value="">{t("Carregando…")}</option> : free.length ? free.map((n) => <option key={n} value={n}>{n}</option>) : <option value="">{t("nenhuma outra rede")}</option>}
          </select>
          <input placeholder={t("IP fixo (opcional)")} aria-label={t("IP fixo")} spellCheck={false} value={ip} onChange={(e) => setIp(e.target.value)} />
          <button
            className="btn small"
            onClick={() => {
              const n = sel || free[0];
              if (n) op("connect", { network: n, ipv4: ip.trim() });
            }}
          >
            {t("Conectar")}
          </button>
        </div>
      ) : null}
    </Section>
  );
}
