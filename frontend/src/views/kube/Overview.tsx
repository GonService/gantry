import { fmtBytes, kubeMem } from "@/lib/format";
import { KMeter, Tile } from "@/components/tile";
import { Loading } from "@/components/ui";
import { t } from "@/lib/i18n";
import { kubeHref, type Cluster, type Overview as OV, type Row } from "./types";

export function Overview({ c, ov }: { c: Cluster; ov?: OV }) {
  if (!ov) return <Loading />;
  const nodes = ov.nodes ?? [],
    pods = ov.pods ?? [],
    deps = ov.deployments ?? [];
  const readyNodes = nodes.filter((n) => !n.problem).length;
  const running = pods.filter((p) => p.info?.status === "Running").length;
  const podProbs = pods.filter((p) => p.problem);
  const depOK = deps.filter((d) => !d.problem).length;
  let cpuUsed = 0,
    cpuCap = 0,
    memUsed = 0,
    memCap = 0;
  for (const n of nodes) {
    const u = ov.nodeUsage?.[n.name];
    cpuCap += (parseFloat(n.cells[4]) || 0) * 1000;
    memCap += kubeMem(n.cells[5]);
    if (u) {
      cpuUsed += u.cpu;
      memUsed += u.mem;
    }
  }
  const hasUsage = ov.nodeUsage && Object.keys(ov.nodeUsage).length > 0;
  const href = (k: string) => kubeHref(c.id, k);
  const att = (what: string, list: Row[] | undefined, kind: string) =>
    (list ?? []).filter((d) => d.problem).map((d) => ({ what, name: d.name, ns: d.namespace, msg: d.problem!, href: href(kind) }));
  const attention = [
    ...podProbs.map((p) => ({ what: "Pod", name: p.name, ns: p.namespace, msg: p.problem!, href: href("pods") })),
    ...att("Deployment", deps, "deployments"),
    ...att("StatefulSet", ov.statefulsets, "statefulsets"),
    ...att("DaemonSet", ov.daemonsets, "daemonsets"),
    ...att("Job", ov.jobs, "jobs"),
    ...nodes.filter((n) => n.problem).map((n) => ({ what: t("Nó"), name: n.name, ns: undefined, msg: n.problem!, href: href("nodes") })),
  ];
  const evs = ov.events ?? [];
  return (
    <div>
      <div className="tiles">
        <Tile label={t("Nós")} value={readyNodes + "/" + nodes.length} meta={t("prontos")} frac={nodes.length ? readyNodes / nodes.length : null} href={href("nodes")} />
        <Tile
          label="Pods"
          value={running + "/" + pods.length}
          meta={podProbs.length ? t("{n} com problema", { n: podProbs.length }) : t("rodando")}
          frac={pods.length ? running / pods.length : null}
          href={href("pods")}
        />
        <Tile label="Deployments" value={depOK + "/" + deps.length} meta={t("prontos")} frac={deps.length ? depOK / deps.length : null} href={href("deployments")} />
        {hasUsage ? (
          <Tile label="CPU" value={t("{used} de {total}", { used: (cpuUsed / 1000).toFixed(2), total: (cpuCap / 1000).toFixed(0) })} meta={t("núcleos em uso")} frac={cpuCap ? cpuUsed / cpuCap : null} />
        ) : (
          <Tile label="CPU" value="—" meta={t("sem metrics-server")} />
        )}
        {hasUsage ? <Tile label={t("Memória")} value={fmtBytes(memUsed)} meta={t("de {total}", { total: fmtBytes(memCap) })} frac={memCap ? memUsed / memCap : null} /> : null}
      </div>
      <div className="block">
        <div className="block-head">
          <h2 className="block-title">{attention.length ? t("Precisa de atenção") + " · " + attention.length : t("Tudo certo")}</h2>
        </div>
        {attention.length ? (
          <div className="panel table-wrap">
            <table className="grid kube-grid">
              <tbody>
                {attention.slice(0, 40).map((a, i) => (
                  <tr className="kube-problem" key={i}>
                    <td className="small muted">{a.what}</td>
                    <td>
                      <a href={a.href}>
                        <b>{a.name}</b>
                      </a>
                      {a.ns ? <div className="muted small">{a.ns}</div> : null}
                    </td>
                    <td className="small">{a.msg}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="muted">{t("Nenhum pod, deployment ou nó com problema.")}</p>
        )}
      </div>
      <div className="block">
        <div className="block-head">
          <h2 className="block-title">{t("Nós")}</h2>
        </div>
        <div className="panel table-wrap">
          <table className="grid kube-grid">
            <thead>
              <tr>
                {[t("Nó"), t("Estado"), t("Papéis"), t("Versão"), "CPU", t("Memória")].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => {
                const u = ov.nodeUsage?.[n.name];
                const cpu = parseFloat(n.cells[4]) || 0,
                  mem = kubeMem(n.cells[5]);
                return (
                  <tr key={n.name} className={n.problem ? "kube-problem" : ""}>
                    <td>
                      <b>{n.name}</b>
                      <div className="muted small mono">{n.cells[3]}</div>
                    </td>
                    <td className="small">{n.cells[0]}</td>
                    <td className="small">{n.cells[1]}</td>
                    <td className="small mono">{n.cells[2]}</td>
                    <td className="small">{u ? <KMeter frac={u.cpu / (cpu * 1000)} text={(u.cpu / 1000).toFixed(2) + " / " + cpu} /> : t("{n} núcleos", { n: cpu })}</td>
                    <td className="small">{u ? <KMeter frac={u.mem / mem} text={fmtBytes(u.mem) + " / " + fmtBytes(mem)} /> : fmtBytes(mem)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
      <div className="block">
        <div className="block-head">
          <h2 className="block-title">{t("Alertas recentes")}</h2>
          <span className="grow" />
          <a className="btn small" href={href("events")}>
            {t("Todos os eventos")}
          </a>
        </div>
        {evs.length ? (
          <div className="panel table-wrap">
            <table className="grid kube-grid">
              <tbody>
                {evs.map((e, i) => (
                  <tr key={i}>
                    <td className="small nowrap">{e.cells[5]}</td>
                    <td>
                      <span className="badge warn">{e.cells[1]}</span>
                    </td>
                    <td className="small mono">{(e.namespace ? e.namespace + "/" : "") + e.cells[2]}</td>
                    <td className="small">{e.cells[3]}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="muted">{t("Nenhum alerta.")}</p>
        )}
      </div>
    </div>
  );
}
