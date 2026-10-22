/* eslint-disable @typescript-eslint/no-explicit-any */
import { Fragment, type ReactNode } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { fmtBytes, kAge, kdDur, kdTime, kubeCPU, kubeMem, toYAML } from "@/lib/format";
import { t } from "@/lib/i18n";
import { confirm } from "@/components/dialog";
import { KMeter } from "@/components/tile";
import type { Cluster } from "../types";
import { B, Copy, DLink, KV, RowsTable, Sec, chips, condBad, conditions, images, type KVPair } from "./parts";
import { Container, Volume } from "./container";
import { loadDetail } from "./store";

type View = (c: Cluster, o: any, d: any, common: KVPair[]) => ReactNode[];

function Revisions({ c, o, d }: { c: Cluster; o: any; d: any }) {
  const revs = d.revisions ?? [];
  if (revs.length < 1) return null;
  const can = d.can ?? {};
  const md = o.metadata;
  const back = async (rv: any) => {
    if (!(await confirm(t("Voltar {name} para a revisão {rev}?", { name: md.name, rev: rv.number }), t("Os pods são recriados aos poucos com {images}. Isso vira uma revisão nova (kubectl rollout undo).", { images: rv.images.join(", ") }), t("Voltar|revisão"))))
      return;
    try {
      await api("/clusters/" + enc(c.id) + "/r/deployments/" + enc(md.namespace) + "/" + enc(md.name) + "/voltar", { method: "POST", body: { revision: rv.number } });
      gantry.toast(t("{name} voltando para a revisão {rev}", { name: md.name, rev: rv.number }), "ok");
      void loadDetail(true);
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  return (
    <Sec title={t("Histórico") + " · " + revs.length}>
      <table className="kd-table kd-revs">
        <tbody>
          {revs.map((rv: any) => (
            <tr key={rv.number}>
              <td>
                <b>#{rv.number}</b> {rv.current ? <B text={t("atual")} tone="ok" /> : null}
                <div className="muted small">{t("há {age}", { age: kAge(rv.created) }) + (rv.replicas ? " · " + rv.ready + "/" + rv.replicas + " pods" : "")}</div>
              </td>
              <td>
                {rv.images.map((i: string) => (
                  <div key={i} className="mono small kd-img">
                    {i}
                  </div>
                ))}
                {rv.changeCause ? <div className="muted small">{rv.changeCause}</div> : null}
              </td>
              <td className="row-actions">
                {!rv.current && c.mode !== "leitura" && can.patch !== false ? (
                  <button className="btn small" type="button" onClick={() => back(rv)}>
                    {t("Voltar para esta")}
                  </button>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Sec>
  );
}

function workload(o: any, d: any, common: KVPair[], kind: string): ReactNode[] {
  const sp = o.spec ?? {},
    st = o.status ?? {};
  const want = kind === "daemonsets" ? st.desiredNumberScheduled : (sp.replicas ?? 1);
  const ready = kind === "daemonsets" ? st.numberReady : st.readyReplicas;
  const upd = kind === "daemonsets" ? st.updatedNumberScheduled : st.updatedReplicas;
  const avail = kind === "daemonsets" ? st.numberAvailable : st.availableReplicas;
  const strat = sp.strategy ?? sp.updateStrategy ?? {};
  const ru = strat.rollingUpdate ?? {};
  return [
    <Sec title={t("Resumo")} key="r">
      <KV
        pairs={[
          [
            t("Réplicas"),
            <span>
              <B text={t("{ready} de {want} prontas", { ready: ready || 0, want: want || 0 })} tone={(ready || 0) >= (want || 0) ? "ok" : "warn"} />
              <span className="muted small">{" · " + t("{upd} atualizadas · {avail} disponíveis", { upd: upd || 0, avail: avail || 0 })}</span>
            </span>,
          ],
          [t("Estratégia"), strat.type ? strat.type + (ru.maxSurge != null ? " " + t("(a mais {surge}, fora {unavail})", { surge: ru.maxSurge, unavail: ru.maxUnavailable }) : "") : null],
          [t("Seletor"), chips(sp.selector?.matchLabels)],
          [t("Imagens"), images(sp.template)],
          ...common,
          [t("Condições"), conditions(st.conditions)],
        ]}
      />
    </Sec>,
    <Sec title={"Pods · " + (d.pods ?? []).length} key="p">
      <RowsTable kind="pods" rows={d.pods} usage={d.usage} />
    </Sec>,
  ];
}

export const VIEWS: Record<string, View> = {
  pods(c, o, d, common) {
    const sp = o.spec ?? {},
      st = o.status ?? {},
      md = o.metadata;
    const u = d.usage?.[md.namespace + "/" + md.name];
    const restarts = (st.containerStatuses ?? []).reduce((n: number, x: any) => n + (x.restartCount || 0), 0);
    const phase = md.deletionTimestamp ? t("Terminando") : st.phase;
    const cts = [...(sp.initContainers ?? []).map((ct: any) => [ct, true]), ...(sp.containers ?? []).map((ct: any) => [ct, false])];
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [
              t("Estado"),
              <span>
                <B text={phase || "?"} tone={phase === "Running" || phase === "Succeeded" ? "ok" : phase === "Failed" ? "err" : "warn"} />
                {st.reason ? " " + st.reason : ""}
                {st.message ? <div className="small muted">{st.message}</div> : null}
              </span>,
            ],
            ...common,
            [t("Nó"), sp.nodeName ? <DLink kind="nodes" ns="" name={sp.nodeName} /> : t("ainda sem nó")],
            [t("IP do pod"), st.podIP ? <span className="mono">{st.podIP}</span> : null],
            [t("IP do nó"), st.hostIP ? <span className="mono">{st.hostIP}</span> : null],
            [t("Uso"), u ? t("{cpu}m de CPU", { cpu: u.cpu }) + " · " + fmtBytes(u.mem) : null],
            [t("Reinícios"), restarts ? String(restarts) : null],
            ["QoS", st.qosClass],
            [t("Service account"), sp.serviceAccountName ? <DLink kind="serviceaccounts" ns={md.namespace} name={sp.serviceAccountName} /> : null],
            [t("Reinício"), sp.restartPolicy],
            [t("Prioridade"), sp.priorityClassName],
            [t("Seletor de nó"), chips(sp.nodeSelector)],
            ["Tolerations", (sp.tolerations ?? []).length ? sp.tolerations.map((x: any) => (x.key || "*") + (x.value ? "=" + x.value : "") + (x.effect ? ":" + x.effect : "")).join(", ") : null],
            [t("Condições"), conditions(st.conditions)],
          ]}
        />
      </Sec>,
      <Sec title={"Containers · " + cts.length} key="c">
        {cts.map(([ct, init]) => (
          <Container key={(init ? "i:" : "") + ct.name} c={c} o={o} d={d} ct={ct} init={init} />
        ))}
      </Sec>,
      (sp.volumes ?? []).length ? (
        <Sec title={"Volumes · " + sp.volumes.length} key="v">
          <dl className="kd-kv">
            {sp.volumes.map((v: any) => (
              <Volume key={v.name} v={v} ns={md.namespace} />
            ))}
          </dl>
        </Sec>
      ) : null,
    ];
  },
  deployments: (c, o, d, common) => [...workload(o, d, common, "deployments"), <Revisions key="rev" c={c} o={o} d={d} />],
  statefulsets: (_c, o, d, common) => workload(o, d, common, "statefulsets"),
  daemonsets: (_c, o, d, common) => workload(o, d, common, "daemonsets"),
  replicasets: (_c, o, d, common) => workload(o, d, common, "replicasets"),
  jobs(_c, o, d, common) {
    const sp = o.spec ?? {},
      st = o.status ?? {};
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [t("Conclusões"), t("{done} de {total}", { done: st.succeeded || 0, total: sp.completions ?? 1 })],
            [t("Falhas"), t("{n} (tolera {limit})", { n: st.failed || 0, limit: sp.backoffLimit ?? 6 })],
            [t("Paralelismo"), sp.parallelism != null ? String(sp.parallelism) : null],
            [t("Ativos"), st.active ? String(st.active) : null],
            ...common,
            [t("Início"), kdTime(st.startTime)],
            [t("Fim"), kdTime(st.completionTime)],
            [t("Duração"), st.startTime ? kdDur(st.startTime, st.completionTime) + (st.completionTime ? "" : " " + t("(rodando)")) : null],
            [t("Imagens"), images(sp.template)],
            [t("Condições"), conditions(st.conditions)],
          ]}
        />
      </Sec>,
      <Sec title={"Pods · " + (d.pods ?? []).length} key="p">
        <RowsTable kind="pods" rows={d.pods} usage={d.usage} />
      </Sec>,
    ];
  },
  cronjobs(_c, o, d, common) {
    const sp = o.spec ?? {},
      st = o.status ?? {};
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [
              t("Agenda"),
              <span>
                <span className="mono">{sp.schedule}</span>
                {sp.timeZone ? " (" + sp.timeZone + ")" : ""}
              </span>,
            ],
            [t("Suspenso"), sp.suspend ? <B text={t("sim")} tone="warn" /> : t("não")],
            [t("Último agendamento"), kdTime(st.lastScheduleTime)],
            [t("Último sucesso"), kdTime(st.lastSuccessfulTime)],
            [t("Ativos"), (st.active ?? []).length ? String(st.active.length) : null],
            [t("Concorrência"), sp.concurrencyPolicy],
            [t("Histórico"), t("guarda {ok} com sucesso e {failed} com falha", { ok: sp.successfulJobsHistoryLimit ?? 3, failed: sp.failedJobsHistoryLimit ?? 1 })],
            ...common,
            [t("Imagens"), images(sp.jobTemplate?.spec?.template)],
          ]}
        />
      </Sec>,
      <Sec title={"Jobs · " + (d.jobs ?? []).length} key="j">
        <RowsTable kind="jobs" rows={d.jobs} />
      </Sec>,
    ];
  },
  services(_c, o, d, common) {
    const sp = o.spec ?? {},
      st = o.status ?? {};
    const lb = (st.loadBalancer?.ingress ?? []).map((x: any) => x.ip || x.hostname).join(", ");
    const dns = o.metadata.name + "." + o.metadata.namespace + ".svc";
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [t("Tipo"), sp.type],
            ["Cluster IP", sp.clusterIP ? <span className="mono">{sp.clusterIP}</span> : null],
            [t("IPs externos"), (sp.externalIPs ?? []).join(", ") || lb || null],
            [
              t("Nome no cluster"),
              <span className="mono">
                {dns}
                <Copy text={dns} />
              </span>,
            ],
            [t("Seletor"), chips(sp.selector)],
            [t("Afinidade"), sp.sessionAffinity !== "None" ? sp.sessionAffinity : null],
            ...common,
          ]}
        />
      </Sec>,
      (sp.ports ?? []).length ? (
        <Sec title={t("Portas")} key="ports">
          <table className="kd-table">
            <tbody>
              {sp.ports.map((p: any, i: number) => (
                <tr key={i}>
                  <td>{p.name || ""}</td>
                  <td className="mono">{p.port + "/" + (p.protocol || "TCP")}</td>
                  <td className="mono">{"→ " + (p.targetPort ?? p.port)}</td>
                  <td className="mono muted">{p.nodePort ? t("nó") + " :" + p.nodePort : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Sec>
      ) : null,
      sp.selector ? (
        <Sec title={"Pods · " + (d.pods ?? []).length} key="p">
          <RowsTable kind="pods" rows={d.pods} usage={d.usage} />
        </Sec>
      ) : null,
    ];
  },
  ingresses(_c, o, _d, common) {
    const sp = o.spec ?? {},
      ns = o.metadata.namespace;
    const rules: ReactNode[] = [];
    for (const r of sp.rules ?? [])
      for (const p of r.http?.paths ?? []) {
        const b = p.backend?.service ?? {};
        rules.push(
          <tr key={rules.length}>
            <td className="mono">{(r.host || "*") + (p.path || "/")}</td>
            <td>
              → {b.name ? <DLink kind="services" ns={ns} name={b.name} /> : "?"}:{b.port?.number || b.port?.name || ""}
            </td>
          </tr>,
        );
      }
    const addr = (o.status?.loadBalancer?.ingress ?? []).map((x: any) => x.ip || x.hostname).join(", ");
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [t("Classe"), sp.ingressClassName],
            [t("Endereço"), addr],
            ["TLS", (sp.tls ?? []).flatMap((t: any) => t.hosts ?? []).join(", ")],
            ...common,
          ]}
        />
      </Sec>,
      rules.length ? (
        <Sec title={t("Regras")} key="rules">
          <table className="kd-table">
            <tbody>{rules}</tbody>
          </table>
        </Sec>
      ) : null,
    ];
  },
  configmaps(_c, o, _d, common) {
    const data = o.data ?? {};
    const keys = Object.keys(data).sort();
    return [
      <Sec title={t("Resumo")} key="r">
        <KV pairs={[...common, [t("Chaves binárias"), Object.keys(o.binaryData ?? {}).join(", ")]]} />
      </Sec>,
      <Sec title={t("Dados") + " · " + keys.length} key="d">
        {keys.length ? (
          keys.map((k) => (
            <details key={k} className="kd-fold" open={keys.length <= 3}>
              <summary>
                <span className="mono">{k}</span>
                <span className="muted small">{" · " + t("{n} caracteres", { n: data[k].length })}</span>
              </summary>
              <pre className="kd-pre">{data[k]}</pre>
            </details>
          ))
        ) : (
          <p className="muted small">{t("Vazio.")}</p>
        )}
      </Sec>,
    ];
  },
  secrets(_c, o, d, common) {
    const keys = Object.keys(d.secretKeys ?? {}).sort();
    return [
      <Sec title={t("Resumo")} key="r">
        <KV pairs={[[t("Tipo"), <span className="mono">{o.type}</span>], ...common]} />
      </Sec>,
      <Sec title={t("Chaves") + " · " + keys.length} key="k">
        {keys.length ? (
          <dl className="kd-kv">
            {keys.map((k) => (
              <Fragment key={k}>
                <dt className="mono">{k}</dt>
                <dd className="muted">{d.secretKeys[k]} bytes</dd>
              </Fragment>
            ))}
          </dl>
        ) : (
          <p className="muted small">{t("Vazio.")}</p>
        )}
      </Sec>,
    ];
  },
  nodes(_c, o, d, common) {
    const st = o.status ?? {},
      ni = st.nodeInfo ?? {},
      sp = o.spec ?? {};
    const roles = Object.keys(o.metadata.labels ?? {})
      .filter((k) => k.startsWith("node-role.kubernetes.io/"))
      .map((k) => k.split("/")[1]);
    const ready = (st.conditions ?? []).find((x: any) => x.type === "Ready");
    const cap = st.capacity ?? {},
      al = st.allocatable ?? {};
    const cpu = kubeCPU(al.cpu),
      mem = kubeMem(al.memory);
    const u = d.node;
    const ok = ready && ready.status === "True";
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [
              t("Estado"),
              <span>
                <B text={ok ? t("Pronto") : t("Não pronto")} tone={ok ? "ok" : "err"} />
                {sp.unschedulable ? " " : ""}
                {sp.unschedulable ? <B text={t("isolado")} tone="warn" /> : null}
              </span>,
            ],
            [t("Papéis"), roles.join(", ")],
            [t("Endereços"), (st.addresses ?? []).map((a: any) => a.type + " " + a.address).join(" · ")],
            ["CPU", u ? <KMeter frac={u.cpu / cpu} text={t("{used} de {total} núcleos", { used: (u.cpu / 1000).toFixed(2), total: al.cpu })} /> : t("{n} núcleos", { n: al.cpu })],
            [t("Memória"), u ? <KMeter frac={u.mem / mem} text={t("{used} de {total}", { used: fmtBytes(u.mem), total: fmtBytes(mem) })} /> : fmtBytes(mem)],
            ["Pods", t("{used} de {total}", { used: (d.pods ?? []).length, total: al.pods || cap.pods || "?" })],
            [t("Sistema"), [ni.osImage, ni.architecture].filter(Boolean).join(" · ")],
            ["Kernel", ni.kernelVersion],
            ["Runtime", ni.containerRuntimeVersion],
            ["Kubelet", ni.kubeletVersion],
            ["Taints", (sp.taints ?? []).map((t: any) => t.key + (t.value ? "=" + t.value : "") + ":" + t.effect).join(", ")],
            ...common,
            [t("Condições"), conditions(st.conditions)],
          ]}
        />
      </Sec>,
      <Sec title={"Pods · " + (d.pods ?? []).length} key="p">
        <RowsTable kind="pods" rows={d.pods} usage={d.usage} />
      </Sec>,
    ];
  },
  pvcs(_c, o, _d, common) {
    const sp = o.spec ?? {},
      st = o.status ?? {};
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [t("Estado"), st.phase],
            [t("Capacidade"), st.capacity?.storage || sp.resources?.requests?.storage],
            [t("Storage class"), sp.storageClassName],
            [t("Acesso"), (sp.accessModes ?? []).join(", ")],
            ["Volume", sp.volumeName ? <DLink kind="pvs" ns="" name={sp.volumeName} /> : null],
            ...common,
          ]}
        />
      </Sec>,
    ];
  },
  pvs(_c, o, _d, common) {
    const sp = o.spec ?? {},
      st = o.status ?? {},
      cr = sp.claimRef;
    return [
      <Sec title={t("Resumo")} key="r">
        <KV
          pairs={[
            [t("Estado"), st.phase],
            [t("Capacidade"), sp.capacity?.storage],
            [t("Storage class"), sp.storageClassName],
            [t("Acesso"), (sp.accessModes ?? []).join(", ")],
            [t("Ao liberar"), sp.persistentVolumeReclaimPolicy],
            [
              t("Usado por"),
              cr ? (
                <span>
                  PVC <DLink kind="pvcs" ns={cr.namespace} name={cr.name} /> ({cr.namespace})
                </span>
              ) : null,
            ],
            [
              t("Origem"),
              Object.keys(sp)
                .filter((k) => !["capacity", "accessModes", "claimRef", "persistentVolumeReclaimPolicy", "storageClassName", "volumeMode", "nodeAffinity", "mountOptions"].includes(k))
                .join(", "),
            ],
            ...common,
          ]}
        />
      </Sec>,
    ];
  },
  namespaces: (_c, o, _d, common) => [
    <Sec title={t("Resumo")} key="r">
      <KV pairs={[[t("Estado"), o.status?.phase], ...common]} />
    </Sec>,
  ],
};

// A custom resource: its conditions, spec and status as YAML.
export const customView: View = (_c, o, _d, common) => {
  const st = o.status ?? {};
  const conds = Array.isArray(st.conditions) ? st.conditions : null;
  const rest = { ...st };
  if (conds) delete rest.conditions;
  const block = (title: string, v: any) =>
    v && Object.keys(v).length ? (
      <Sec title={title} key={title}>
        <pre className="kd-pre kd-yaml">{toYAML(v)}</pre>
      </Sec>
    ) : null;
  return [
    <Sec title={t("Resumo")} key="r">
      <KV pairs={[["API", <span className="mono">{o.apiVersion}</span>], ...common, [t("Condições"), conds ? conditions(conds) : null]]} />
    </Sec>,
    conds ? (
      <Sec title={t("Condições")} key="c">
        <div className="kd-events">
          {conds.map((x: any, i: number) => (
            <div key={i} className={"kd-event" + (condBad(x) ? " warn" : "")}>
              <div className="kd-event-head">
                <B text={x.type + ": " + x.status} tone={condBad(x) ? "warn" : "ok"} />
                {x.lastTransitionTime ? <span className="muted small">{t("há {age}", { age: kAge(Date.parse(x.lastTransitionTime) / 1000) })}</span> : null}
              </div>
              <div className="small">{[x.reason, x.message].filter(Boolean).join(": ")}</div>
            </div>
          ))}
        </div>
      </Sec>
    ) : null,
    block("Spec", o.spec),
    block("Status", rest),
  ];
};
