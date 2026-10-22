/* eslint-disable @typescript-eslint/no-explicit-any */
import { Fragment, useEffect, useRef } from "react";
import { XIcon } from "@/components/icons";
import { kdTime } from "@/lib/format";
import { fmtAgo } from "@/lib/app";
import { t } from "@/lib/i18n";
import { KubeActions } from "../actions";
import type { Cluster, KindInfo } from "../types";
import { DLink, Events, KV, OWNER_KINDS, Sec, chips, type KVPair } from "./parts";
import { VIEWS, customView } from "./views";
import { History } from "./extra";
import { HelmRelease } from "./helm";
import { backDetail, closeDetail, loadDetail, useDetail } from "./store";

const ONE: Record<string, string> = {
  pods: "Pod",
  deployments: "Deployment",
  statefulsets: "StatefulSet",
  daemonsets: "DaemonSet",
  replicasets: "ReplicaSet",
  jobs: "Job",
  cronjobs: "CronJob",
  hpas: "Autoscaler",
  services: "Service",
  ingresses: "Ingress",
  endpoints: "Endpoints",
  networkpolicies: "Network Policy",
  configmaps: "ConfigMap",
  secrets: "Secret",
  serviceaccounts: "Service Account",
  helm: t("Release do Helm"),
  pvcs: "PVC",
  pvs: "PV",
  storageclasses: "Storage Class",
  nodes: t("Nó"),
  namespaces: "Namespace",
};

// The bits of a table row that KubeActions uses.
function rowInfo(kind: string, o: any) {
  const sp = o.spec ?? {};
  if (kind === "pods") return { containers: (sp.containers ?? []).map((x: any) => x.name), ports: (sp.containers ?? []).flatMap((x: any) => (x.ports ?? []).map((p: any) => p.containerPort)) };
  if (kind === "deployments" || kind === "statefulsets" || kind === "replicasets") return { replicas: sp.replicas };
  if (kind === "cronjobs") return { suspended: !!sp.suspend };
  if (kind === "nodes") return { unschedulable: !!sp.unschedulable };
  if (kind === "services") return { ports: (sp.ports ?? []).map((p: any) => ({ port: p.port, name: p.name })) };
  return {};
}

function Sections({ c, kind, o, d }: { c: Cluster; kind: string; o: any; d: any }) {
  const md = o.metadata ?? {};
  const owners = (md.ownerReferences ?? []).map((r: any) =>
    OWNER_KINDS[r.kind] ? (
      <span key={r.name}>
        {r.kind + " "}
        <DLink kind={OWNER_KINDS[r.kind]} ns={md.namespace} name={r.name} />
      </span>
    ) : (
      r.kind + " " + r.name
    ),
  );
  const common: KVPair[] = [
    [t("Criado"), kdTime(md.creationTimestamp)],
    [t("Controlado por"), owners.length ? owners : null],
  ];
  const fn = VIEWS[kind] ?? (kind.startsWith("crd.") ? customView : null);
  const out = fn
    ? fn(c, o, d, common)
    : [
        <Sec title={t("Resumo")} key="r">
          <KV pairs={common} />
        </Sec>,
      ];
  if (d.history) out.splice(1, 0, <History key="hist" kind={kind} d={d} />);
  if (d.diagnosis?.length) out.unshift(<Diagnosis key="diag" c={c} kind={kind} list={d.diagnosis} ns={md.namespace} />);
  const ann = md.annotations ?? {};
  return (
    <>
      {out.map((x, i) => (
        <Fragment key={i}>{x}</Fragment>
      ))}
      <Sec title="Labels">{chips(md.labels)}</Sec>
      {Object.keys(ann).length ? (
        <section className="kd-sec">
          <details>
            <summary>
              <h3>Annotations · {Object.keys(ann).length}</h3>
            </summary>
            <dl className="kd-kv kd-ann">
              {Object.keys(ann)
                .sort()
                .map((k) => (
                  <Fragment key={k}>
                    <dt className="mono">{k}</dt>
                    <dd className="mono">{ann[k]}</dd>
                  </Fragment>
                ))}
            </dl>
          </details>
        </section>
      ) : null}
      <Events evs={d.events} />
    </>
  );
}

// The details of an object in a side panel (as Lens).
export function DetailPanel({ kinds, routeCluster }: { kinds: (id: string) => KindInfo | undefined; routeCluster: string }) {
  const { c, stack, data, err } = useDetail();
  const body = useRef<HTMLDivElement>(null);
  const it = stack[stack.length - 1];

  // Esc closes (not while a dialog is open); every 10 s it reads again.
  useEffect(() => {
    if (!it) return;
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !document.querySelector("dialog[open]")) closeDetail();
    };
    document.addEventListener("keydown", esc);
    const timer = setInterval(() => {
      if (document.hidden || document.querySelector("dialog[open]") || body.current?.closest(".kd-panel")?.querySelector("details.more-menu[open]")) return;
      void loadDetail(true);
    }, 10000);
    return () => {
      document.removeEventListener("keydown", esc);
      clearInterval(timer);
    };
  }, [it]);
  // Another cluster on screen: the panel goes.
  useEffect(() => {
    if (c && c.id !== routeCluster) closeDetail();
  }, [c, routeCluster]);

  if (!it || !c) return null;
  const label = ONE[it.kind] ?? kinds(it.kind)?.label ?? it.kind;
  const prev = stack.length > 1 ? stack[stack.length - 2] : null;
  let content: React.ReactNode;
  if (!data) content = err ? <div className="error-box">{err}</div> : <div className="loading"><span className="spin" />{t("Carregando…")}</div>;
  else if (it.kind === "helm")
    content = <HelmRelease key={it.ns + "/" + it.name} c={c} d={data} />;
  else {
    const o = data.object;
    const row = { name: it.name, namespace: it.ns, created: 0, cells: [], info: rowInfo(it.kind, o) };
    content = (
      <>
        {err ? <div className="flash flash-err">{err}</div> : null}
        <div className="kd-actions">
          <KubeActions c={c} kind={it.kind} r={row} opts={{ flat: true, can: data.can, after: () => void loadDetail(true), deleted: () => backDetail() }} />
        </div>
        <Sections c={c} kind={it.kind} o={o} d={data} />
      </>
    );
  }
  return (
    <div className="kd-wrap">
      <div className="kd-backdrop" onClick={closeDetail} />
      <aside className="kd-panel" role="dialog" aria-label={label + " " + it.name}>
        <div className="kd-head">
          <div className="kd-title">
            {prev ? (
              <button className="btn small" type="button" title={t("Voltar para {name}", { name: prev.name })} onClick={backDetail}>
                ←
              </button>
            ) : null}
            <div className="kd-title-text">
              <div className="muted small">{label + (it.ns ? " · " + it.ns : "")}</div>
              <h2>{it.name}</h2>
            </div>
          </div>
          <button className="icon-btn" type="button" aria-label={t("Fechar")} title={t("Fechar (Esc)")} onClick={closeDetail}>
            <XIcon />
          </button>
        </div>
        <div className="kd-body" ref={body}>
          {content}
        </div>
      </aside>
    </div>
  );
}

type Finding = { pod?: string; container?: string; severity: string; title: string; detail?: string; next?: string; lines?: string[]; at?: number };

// Diagnosis: why the pod does not come up, with what to do.
function Diagnosis({ kind, list, ns }: { c: Cluster; kind: string; list: Finding[]; ns: string }) {
  return (
    <section className="kd-sec kd-diag">
      <h3>{t("Diagnóstico") + (kind !== "pods" && list[0]?.pod ? " · " + t("pod {pod}", { pod: list[0].pod }) : "")}</h3>
      {kind !== "pods" && list[0]?.pod ? (
        <p className="muted small" style={{ marginTop: 0 }}>
          {t("O pod mais novo com problema:")} <DLink kind="pods" ns={ns} name={list[0].pod} />
        </p>
      ) : null}
      {list.map((f, i) => (
        <div key={i} className={"kd-finding " + f.severity}>
          <b>{f.title}</b>
          {f.at ? <span className="muted small">{" · " + t("última há {ago}", { ago: fmtAgo(f.at) })}</span> : null}
          {f.detail ? <div className="mono small kd-finding-detail">{f.detail}</div> : null}
          {f.next ? <div className="small">{"→ " + f.next}</div> : null}
          {f.lines?.length ? (
            <details open={i === 0}>
              <summary className="small muted">{t("últimas {n} linhas do log da execução que caiu", { n: f.lines.length })}</summary>
              <pre className="kd-pre">{f.lines.join("\n")}</pre>
            </details>
          ) : null}
        </div>
      ))}
    </section>
  );
}
