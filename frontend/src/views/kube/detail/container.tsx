/* eslint-disable @typescript-eslint/no-explicit-any */
import { kdTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { openPodLogs, openPodShell } from "../actions";
import type { Cluster } from "../types";
import { B, Copy, DLink, KV, probe, resources } from "./parts";

function Env({ ct, ns }: { ct: any; ns: string }) {
  const rows: React.ReactNode[] = [];
  for (const e of ct.env ?? []) {
    let v: React.ReactNode;
    const f = e.valueFrom;
    if (!f) v = <span className="mono">{e.value ?? ""}</span>;
    else if (f.secretKeyRef)
      v = (
        <span className="muted">
          Secret <DLink kind="secrets" ns={ns} name={f.secretKeyRef.name} /> → {f.secretKeyRef.key}
        </span>
      );
    else if (f.configMapKeyRef)
      v = (
        <span className="muted">
          ConfigMap <DLink kind="configmaps" ns={ns} name={f.configMapKeyRef.name} /> → {f.configMapKeyRef.key}
        </span>
      );
    else if (f.fieldRef) v = <span className="muted">{t("campo {path}", { path: f.fieldRef.fieldPath })}</span>;
    else if (f.resourceFieldRef) v = <span className="muted">{t("recurso {name}", { name: f.resourceFieldRef.resource })}</span>;
    else v = <span className="muted">{JSON.stringify(f)}</span>;
    rows.push(
      <tr key={"e" + e.name}>
        <td className="mono">{e.name}</td>
        <td>{v}</td>
      </tr>,
    );
  }
  for (const [i, f] of (ct.envFrom ?? []).entries()) {
    const ref = f.configMapRef ? ["ConfigMap", "configmaps", f.configMapRef.name] : f.secretRef ? ["Secret", "secrets", f.secretRef.name] : null;
    if (ref)
      rows.push(
        <tr key={"f" + i}>
          <td className="mono muted">{(f.prefix || "") + "*"}</td>
          <td className="muted">
            {t("todas as chaves do {kind}", { kind: ref[0] })} <DLink kind={ref[1]} ns={ns} name={ref[2]} />
          </td>
        </tr>,
      );
  }
  if (!rows.length) return null;
  return (
    <details className="kd-fold" open={rows.length <= 8}>
      <summary>{t("Variáveis")} · {rows.length}</summary>
      <table className="kd-table">
        <tbody>{rows}</tbody>
      </table>
    </details>
  );
}

function stateOf(st: any): [React.ReactNode, string] {
  if (!st) return [<B text={t("sem estado")} />, ""];
  if (st.running) return [<B text={t("Rodando")} tone="ok" />, t("desde {time}", { time: kdTime(st.running.startedAt) })];
  if (st.waiting) return [<B text={st.waiting.reason || t("Esperando")} tone="warn" />, st.waiting.message || ""];
  if (st.terminated) {
    const tm = st.terminated;
    return [<B text={(tm.reason || t("Terminou")) + " (" + tm.exitCode + ")"} tone={tm.exitCode === 0 ? "" : "err"} />, (tm.message || "") + (tm.finishedAt ? " · " + kdTime(tm.finishedAt) : "")];
  }
  return [<B text="?" />, ""];
}

export function Container({ c, o, d, ct, init }: { c: Cluster; o: any; d: any; ct: any; init: boolean }) {
  const statuses = (o.status ?? {})[init ? "initContainerStatuses" : "containerStatuses"] ?? [];
  const cs = statuses.find((x: any) => x.name === ct.name) ?? {};
  const [badge, since] = stateOf(cs.state);
  const md = o.metadata;
  const ro = c.mode === "leitura";
  const can = d.can ?? {};
  const running = cs.state?.running;
  const last = cs.lastState?.terminated;
  const ports = (ct.ports ?? []).map((p: any) => (p.name ? p.name + " " : "") + p.containerPort + "/" + (p.protocol || "TCP")).join(", ");
  const cmd = [...(ct.command ?? []), ...(ct.args ?? [])].join(" ");
  const mounts = (ct.volumeMounts ?? []).map((m: any, i: number) => (
    <div key={i} className="mono small">
      {m.mountPath}
      <span className="muted">{" ← " + m.name + (m.subPath ? "/" + m.subPath : "") + (m.readOnly ? " (" + t("só leitura") + ")" : "")}</span>
    </div>
  ));
  const use = d.containers?.[ct.name];
  return (
    <div className="kd-ct">
      <div className="kd-ct-head">
        <b>{ct.name}</b>
        {init ? <B text="init" /> : null}
        {badge}
        {cs.ready ? <B text={t("pronto")} tone="ok" /> : null}
        <span className="grow" />
        {can.logs !== false ? (
          <button className="btn small" type="button" onClick={() => openPodLogs(c, md.namespace, md.name, ct.name, true)}>
            Logs
          </button>
        ) : null}
        {can.logs !== false && (cs.restartCount || last) ? (
          <button className="btn small" type="button" title={t("O log da execução que terminou (útil em CrashLoopBackOff)")} onClick={() => openPodLogs(c, md.namespace, md.name, ct.name, true, true)}>
            {t("Log anterior")}
          </button>
        ) : null}
        {!ro && running && can.exec !== false ? (
          <button className="btn small" type="button" onClick={() => openPodShell(c, md.namespace, md.name, ct.name)}>
            Shell
          </button>
        ) : null}
      </div>
      <KV
        pairs={[
          [
            t("Imagem"),
            <span className="mono kd-img">
              {ct.image}
              <Copy text={ct.image} />
            </span>,
          ],
          [t("ID da imagem"), cs.imageID ? <span className="mono small muted kd-img">{cs.imageID}</span> : null],
          [t("Estado"), since],
          [t("Reinícios"), cs.restartCount ? String(cs.restartCount) : null],
          [t("Último término"), last ? (last.reason || "") + " " + t("(código {code})", { code: last.exitCode }) + (last.finishedAt ? " · " + kdTime(last.finishedAt) : "") : null],
          [t("Portas"), ports],
          [t("Comando"), cmd ? <span className="mono">{cmd}</span> : null],
          [t("Diretório"), ct.workingDir ? <span className="mono">{ct.workingDir}</span> : null],
          ...resources(ct.resources, use),
          ["Liveness", probe(ct.livenessProbe)],
          ["Readiness", probe(ct.readinessProbe)],
          ["Startup", probe(ct.startupProbe)],
          [t("Montagens"), mounts.length ? mounts : null],
        ]}
      />
      <Env ct={ct} ns={md.namespace} />
    </div>
  );
}

export function Volume({ v, ns }: { v: any; ns: string }) {
  const keys = Object.keys(v).filter((k) => k !== "name");
  const type = keys[0] || "?";
  const s = v[type] ?? {};
  let what: React.ReactNode;
  if (type === "configMap")
    what = (
      <span>
        ConfigMap <DLink kind="configmaps" ns={ns} name={s.name} />
      </span>
    );
  else if (type === "secret")
    what = (
      <span>
        Secret <DLink kind="secrets" ns={ns} name={s.secretName} />
      </span>
    );
  else if (type === "persistentVolumeClaim")
    what = (
      <span>
        PVC <DLink kind="pvcs" ns={ns} name={s.claimName} />
        {s.readOnly ? " (" + t("só leitura") + ")" : ""}
      </span>
    );
  else if (type === "emptyDir") what = t("diretório temporário") + (s.medium ? " " + t("em {medium}", { medium: s.medium }) : "") + (s.sizeLimit ? ", " + t("até {size}", { size: s.sizeLimit }) : "");
  else if (type === "hostPath")
    what = (
      <span>
        {t("pasta do nó")} <span className="mono">{s.path}</span>
      </span>
    );
  else if (type === "projected") what = t("projetado:") + " " + (s.sources ?? []).map((x: any) => Object.keys(x)[0]).join(", ");
  else what = <span className="mono small">{JSON.stringify(s)}</span>;
  return (
    <>
      <dt className="mono">{v.name}</dt>
      <dd>
        <span className="muted small">{type} · </span>
        {what}
      </dd>
    </>
  );
}
