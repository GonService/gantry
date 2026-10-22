/* eslint-disable @typescript-eslint/no-explicit-any */
import { Fragment, type ReactNode } from "react";
import { gantry } from "@/lib/gantry";
import { copyText, fmtBytes, kAge } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import { openDetail, useDetail } from "./store";
import type { Row, Usage } from "../types";

// Kinds of ownerReferences that have a screen here.
export const OWNER_KINDS: Record<string, string> = {
  ReplicaSet: "replicasets",
  Deployment: "deployments",
  StatefulSet: "statefulsets",
  DaemonSet: "daemonsets",
  Job: "jobs",
  CronJob: "cronjobs",
  Node: "nodes",
};

const present = (x: unknown) => x != null && x !== false && !(Array.isArray(x) && !x.filter((y) => y != null && y !== false).length);

export function Sec({ title, children }: { title: string; children: ReactNode }) {
  const kids = Array.isArray(children) ? children.flat(Infinity as 1).filter(present) : present(children) ? [children] : [];
  if (!kids.length) return null;
  return (
    <section className="kd-sec">
      <h3>{title}</h3>
      {kids.map((k, i) => (
        <Fragment key={i}>{k as ReactNode}</Fragment>
      ))}
    </section>
  );
}

export type KVPair = [string, ReactNode];

// KV skips empty values.
export function KV({ pairs }: { pairs: (KVPair | null)[] }) {
  const rows = pairs.filter((p): p is KVPair => !!p && p[1] != null && p[1] !== "" && !(Array.isArray(p[1]) && !p[1].length));
  if (!rows.length) return null;
  return (
    <dl className="kd-kv">
      {rows.map(([k, v]) => (
        <Fragment key={k}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

export function Chips({ m, cls }: { m?: Record<string, string> | null; cls?: string }) {
  const keys = Object.keys(m ?? {}).sort();
  if (!keys.length) return null;
  return (
    <div className="kd-chips">
      {keys.map((k) => (
        <span key={k} className={"kd-chip " + (cls ?? "")} title={k + "=" + m![k]}>
          {k}
          {m![k] !== "" ? <b>={m![k]}</b> : null}
        </span>
      ))}
    </div>
  );
}

export const chips = (m?: Record<string, string> | null) => (Object.keys(m ?? {}).length ? <Chips m={m} /> : null);

// For most conditions True is good; for the negative ones (Failed,
// DiskPressure, Degraded…) False is.
export function condBad(x: { type: string; status: string }): boolean {
  return /Pressure|Unavailable|Fail|Degraded|Stalled|Error|Disrupt|Unhealthy/i.test(x.type) ? x.status === "True" : x.status !== "True";
}

export function conditions(list?: any[] | null) {
  if (!list || !list.length) return null;
  return (
    <div className="kd-chips">
      {list.map((x, i) => (
        <span key={i} className={"kd-chip " + (condBad(x) ? "bad" : "good")} title={[x.reason, x.message].filter(Boolean).join(": ")}>
          {x.type}
        </span>
      ))}
    </div>
  );
}

export function B({ text, tone }: { text: ReactNode; tone?: string }) {
  return <span className={"badge " + (tone ?? "")}>{text}</span>;
}

export function DLink({ kind, ns, name, text }: { kind: string; ns: string; name: string; text?: ReactNode }) {
  return (
    <a
      href="#"
      onClick={(e) => {
        e.preventDefault();
        const c = useDetail.getState().c;
        if (c) openDetail(c, kind, ns, name, true);
      }}
    >
      {text ?? name}
    </a>
  );
}

export function Copy({ text }: { text: string }) {
  return (
    <button className="kd-copy" type="button" title={t("Copiar")} onClick={async () => gantry.toast((await copyText(text)) ? t("Copiado") : t("Não deu para copiar"), "ok")}>
      ⧉
    </button>
  );
}

export function Events({ evs }: { evs?: Row[] }) {
  if (!evs || !evs.length)
    return (
      <Sec title={t("Eventos")}>
        <p className="muted small">{t("Nenhum evento recente.")}</p>
      </Sec>
    );
  return (
    <Sec title={t("Eventos") + " · " + evs.length}>
      <div className="kd-events">
        {evs.slice(0, 30).map((e, i) => {
          const warn = e.cells[0] === "Warning";
          return (
            <div key={i} className={"kd-event" + (warn ? " warn" : "")}>
              <div className="kd-event-head">
                <B text={e.cells[1]} tone={warn ? "warn" : ""} />
                <span className="muted small">{t("há {age}", { age: kAge(e.created) }) + (e.cells[4] && e.cells[4] !== "1" ? " · " + e.cells[4] + "×" : "")}</span>
              </div>
              <div className="small">{e.cells[3]}</div>
            </div>
          );
        })}
      </div>
    </Sec>
  );
}

// A short table of pods (or jobs) that open their own details.
export function RowsTable({ kind, rows, usage }: { kind: string; rows?: Row[]; usage?: Record<string, Usage> }) {
  if (!rows || !rows.length) return <p className="muted small">{kind === "jobs" ? t("Nenhum job.") : t("Nenhum pod.")}</p>;
  return (
    <div className="table-wrap">
      <table className="grid kube-grid kd-mini">
        <tbody>
          {rows.map((r) => {
            const u = usage?.[r.namespace + "/" + r.name];
            return (
              <tr key={r.namespace + "/" + r.name} className={r.problem ? "kube-problem" : ""}>
                <td>
                  <DLink kind={kind} ns={r.namespace ?? ""} name={r.name} />
                  {r.problem ? <div className="kube-why">{r.problem}</div> : null}
                </td>
                {r.cells.slice(0, 3).map((x, i) => (
                  <td key={i} className="small nowrap">
                    {x}
                  </td>
                ))}
                {kind === "pods" ? <td className="small nowrap muted">{u ? u.cpu + "m · " + fmtBytes(u.mem) : ""}</td> : null}
                <td className="small nowrap muted">{kAge(r.created)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function resources(res: any, use?: Usage): KVPair[] {
  const rq = res?.requests ?? {},
    lm = res?.limits ?? {};
  const line = (k: string, label: string, fmt: () => string): KVPair | null => {
    if (rq[k] == null && lm[k] == null && !use) return null;
    const parts: string[] = [];
    if (use) parts.push(t("usa {v}", { v: fmt() }));
    if (rq[k] != null) parts.push(t("pede {v}", { v: rq[k] }));
    if (lm[k] != null) parts.push(t("limite {v}", { v: lm[k] }));
    return parts.length ? [label, parts.join(" · ")] : null;
  };
  return [line("cpu", "CPU", () => use!.cpu + "m"), line("memory", t("Memória"), () => fmtBytes(use!.mem))].filter((x): x is KVPair => !!x);
}

export function probe(p: any): string {
  if (!p) return "";
  let what = "";
  if (p.httpGet) what = "HTTP GET " + (p.httpGet.path || "/") + " :" + p.httpGet.port;
  else if (p.tcpSocket) what = "TCP :" + p.tcpSocket.port;
  else if (p.exec) what = "exec " + (p.exec.command || []).join(" ");
  else if (p.grpc) what = "gRPC :" + p.grpc.port;
  const tm: string[] = [];
  if (p.initialDelaySeconds) tm.push(t("espera {n}s", { n: p.initialDelaySeconds }));
  tm.push(t("a cada {n}s", { n: p.periodSeconds || 10 }));
  if (p.failureThreshold) tm.push(tn(p.failureThreshold, "{n} falha", "{n} falhas"));
  return what + " · " + tm.join(", ");
}

export function images(tpl: any) {
  const cts = tpl?.spec?.containers ?? [];
  if (!cts.length) return null;
  return (
    <div>
      {cts.map((x: any) => (
        <div key={x.name} className="mono small kd-img">
          {x.name + ": " + x.image}
        </div>
      ))}
    </div>
  );
}
