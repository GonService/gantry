/* eslint-disable @typescript-eslint/no-explicit-any */
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { copyText, kAge, toYAML } from "@/lib/format";
import { t } from "@/lib/i18n";
import { confirm } from "@/components/dialog";
import { loadView } from "../store";
import type { Cluster } from "../types";
import { B, DLink, KV, Sec } from "./parts";
import { loadDetail } from "./store";

// The kinds of a manifest that have a screen here (to open the object).
const KIND_IDS: Record<string, string> = {
  Pod: "pods",
  Deployment: "deployments",
  StatefulSet: "statefulsets",
  DaemonSet: "daemonsets",
  ReplicaSet: "replicasets",
  Job: "jobs",
  CronJob: "cronjobs",
  HorizontalPodAutoscaler: "hpas",
  Service: "services",
  Ingress: "ingresses",
  NetworkPolicy: "networkpolicies",
  ConfigMap: "configmaps",
  Secret: "secrets",
  ServiceAccount: "serviceaccounts",
  PersistentVolumeClaim: "pvcs",
  PersistentVolume: "pvs",
  StorageClass: "storageclasses",
  Namespace: "namespaces",
};
const CLUSTER_KINDS = new Set(["PersistentVolume", "StorageClass", "Namespace", "ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition"]);

type Doc = { key: string; kind: string; name: string; ns: string; source: string; text: string };

// splitManifest: one entry per object of the manifest Helm applied.
export function splitManifest(m: string, relNS: string): Doc[] {
  const out: Doc[] = [];
  for (const raw of m.split(/^---[ \t]*$/m)) {
    const text = raw.replace(/^\s*\n/, "").replace(/\s+$/, "");
    const body = text.replace(/^#.*\n?/gm, "").trim();
    if (!body) continue;
    const kind = /^kind:\s*["']?([^"'\s]+)/m.exec(text)?.[1] ?? "?";
    const md = /^metadata:[ \t]*\n((?:[ \t]+.*\n?|[ \t]*\n)*)/m.exec(text + "\n")?.[1] ?? "";
    const ind = /^([ \t]+)/.exec(md)?.[1] ?? "  ";
    const field = (f: string) => new RegExp("^" + ind + f + ":\\s*[\"']?([^\"'\\s#]+)", "m").exec(md)?.[1] ?? "";
    const name = field("name");
    const ns = CLUSTER_KINDS.has(kind) ? "" : field("namespace") || relNS;
    const source = /^# Source:\s*(.+)$/m.exec(text)?.[1] ?? "";
    out.push({ key: kind + "/" + (ns ? ns + "/" : "") + name, kind, name, ns, source, text });
  }
  return out;
}

// coalesce: the values Helm renders with (the chart's defaults with the
// given values on top; a null removes a default).
export function coalesce(base: any, over: any): any {
  if (over === undefined) return base;
  if (!isMap(base) || !isMap(over)) return over;
  const out: Record<string, any> = { ...base };
  for (const [k, v] of Object.entries(over)) {
    if (v === null) delete out[k];
    else out[k] = coalesce(base[k], v);
  }
  return out;
}
const isMap = (x: any) => x !== null && typeof x === "object" && !Array.isArray(x);

type Line = { op: " " | "+" | "-"; text: string };

// diffLines: a line diff (longest common subsequence); null when the texts
// are too big for it.
export function diffLines(a: string, b: string): Line[] | null {
  const x = a ? a.split("\n") : [];
  const y = b ? b.split("\n") : [];
  // The same start and end need no table.
  let pre = 0;
  while (pre < x.length && pre < y.length && x[pre] === y[pre]) pre++;
  let suf = 0;
  while (suf < x.length - pre && suf < y.length - pre && x[x.length - 1 - suf] === y[y.length - 1 - suf]) suf++;
  const xs = x.slice(pre, x.length - suf);
  const ys = y.slice(pre, y.length - suf);
  const n = xs.length;
  const m = ys.length;
  if (n * m > 4_000_000) return null;
  const w = m + 1;
  const L = new Uint32Array((n + 1) * w);
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i * w + j] = xs[i] === ys[j] ? L[(i + 1) * w + j + 1] + 1 : Math.max(L[(i + 1) * w + j], L[i * w + j + 1]);
  const out: Line[] = x.slice(0, pre).map((text) => ({ op: " ", text }));
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && xs[i] === ys[j]) {
      out.push({ op: " ", text: xs[i] });
      i++;
      j++;
    } else if (i < n && (j >= m || L[(i + 1) * w + j] >= L[i * w + j + 1])) out.push({ op: "-", text: xs[i++] });
    else out.push({ op: "+", text: ys[j++] });
  }
  for (const text of x.slice(x.length - suf)) out.push({ op: " ", text });
  return out;
}

// Diff shows the changed lines with a few lines around them.
export function Diff({ a, b }: { a: string; b: string }) {
  const lines = useMemo(() => diffLines(a, b), [a, b]);
  if (!lines) return <p className="muted small">{t("Grande demais para comparar linha a linha.")}</p>;
  if (!lines.some((l) => l.op !== " ")) return <p className="muted small">{t("Sem mudanças.")}</p>;
  const keep = lines.map(() => false);
  lines.forEach((l, i) => {
    if (l.op !== " ") for (let k = Math.max(0, i - 3); k <= Math.min(lines.length - 1, i + 3); k++) keep[k] = true;
  });
  const out: ReactNode[] = [];
  let gap = false;
  lines.forEach((l, i) => {
    if (!keep[i]) {
      if (!gap) out.push(<div key={"g" + i} className="kd-diff-gap">⋯</div>);
      gap = true;
      return;
    }
    gap = false;
    out.push(
      <div key={i} className={"kd-diff-line" + (l.op === "+" ? " add" : l.op === "-" ? " del" : "")}>
        {l.op + " " + l.text}
      </div>,
    );
  });
  return <pre className="kd-pre kd-yaml kd-diff">{out}</pre>;
}

function CopyBtn({ text, label }: { text: string; label?: string }) {
  return (
    <button className="btn small" type="button" onClick={async () => gantry.toast((await copyText(text)) ? t("Copiado") : t("Não deu para copiar"), "ok")}>
      {label ?? t("Copiar")}
    </button>
  );
}

// HelmRelease: a release with its history and, of the revision picked, the
// values, the notes and each object of the manifest; another revision can be
// compared with it.
export function HelmRelease({ c, d }: { c: Cluster; d: any }) {
  const rel = d.release;
  const ro = c.mode === "leitura";
  const latest = rel.history[0]?.revision ?? 0;
  const [rev, setRev] = useState(0); // 0: the latest
  const [cmp, setCmp] = useState(0); // the revision compared with (0: none)
  const [mode, setMode] = useState<"given" | "all">("given");
  const [cache, setCache] = useState<Record<number, any>>({});
  const [err, setErr] = useState("");
  const shown = rev || latest;
  const base = "/clusters/" + enc(c.id) + "/helm/" + enc(rel.namespace) + "/" + enc(rel.name);

  // The content of revisions other than the latest (the latest comes with d).
  useEffect(() => {
    if (ro) return;
    for (const r of [shown, cmp]) {
      if (!r || r === latest || cache[r]) continue;
      api<any>(base + "?rev=" + r)
        .then((x) => setCache((m) => ({ ...m, [r]: x.release })))
        .catch((e) => setErr((e as Error).message));
    }
  }, [shown, cmp, latest]); // eslint-disable-line react-hooks/exhaustive-deps
  const content = (r: number) => (r === latest ? rel : cache[r]);
  const cur = content(shown);
  const old = cmp ? content(cmp) : null;

  const back = async (rv: any) => {
    if (!(await confirm(t("Voltar {name} para a revisão {rev}?", { name: rel.name, rev: rv.revision }), t("Roda helm rollback {name} {rev} ({chart}). Isso vira uma revisão nova.", { name: rel.name, rev: rv.revision, chart: rv.chart }), t("Voltar|revisão")))) return;
    try {
      await api(base + "/voltar", { method: "POST", body: { revision: rv.revision } });
      gantry.toast(t("{name} voltou para a revisão {rev}", { name: rel.name, rev: rv.revision }), "ok");
      setRev(0);
      setCmp(0);
      void loadDetail(true);
      void loadView(c.id, "helm", true);
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  const top = rel.history[0] ?? {};
  const valuesOf = (x: any) => (mode === "all" ? coalesce(x?.defaults ?? {}, x?.values ?? {}) : (x?.values ?? {}));
  const yamlOf = (v: any) => (v && Object.keys(v).length ? toYAML(v) : "");
  const vText = cur ? yamlOf(valuesOf(cur)) : "";
  const docs = useMemo(() => (cur?.manifest ? splitManifest(cur.manifest, rel.namespace) : []), [cur?.manifest, rel.namespace]);
  const oldDocs = useMemo(() => (old?.manifest ? splitManifest(old.manifest, rel.namespace) : null), [old?.manifest, rel.namespace]);
  const prevOf = (r: number) => rel.history.find((h: any) => h.revision < r)?.revision ?? 0;

  return (
    <>
      <Sec title={t("Resumo")}>
        <KV
          pairs={[
            ["Chart", <span className="mono">{top.chart}</span>],
            [t("Versão do app"), top.appVersion],
            [t("Revisão"), String(top.revision)],
            [t("Estado"), <B text={top.status} tone={top.status === "deployed" ? "ok" : "warn"} />],
            [t("Atualizada"), top.updated ? t("há {age}", { age: kAge(top.updated) }) : null],
          ]}
        />
      </Sec>
      <Sec title={t("Histórico") + " · " + rel.history.length}>
        <table className="kd-table kd-revs">
          <tbody>
            {rel.history.map((rv: any) => (
              <tr key={rv.revision} className={rv.revision === shown ? "sel" : ""}>
                <td>
                  <b>#{rv.revision}</b> <B text={rv.status} tone={rv.status === "deployed" ? "ok" : rv.status === "superseded" ? "" : "warn"} />
                  <div className="muted small">{t("há {age}", { age: kAge(rv.updated) })}</div>
                </td>
                <td>
                  <div className="mono small">{rv.chart + (rv.appVersion ? " · app " + rv.appVersion : "")}</div>
                  {rv.description ? <div className="muted small">{rv.description}</div> : null}
                </td>
                <td className="row-actions">
                  {!ro && rv.revision !== shown ? (
                    <button className="btn small" type="button" title={t("Mostrar values e manifest desta revisão")} onClick={() => (setRev(rv.revision === latest ? 0 : rv.revision), setCmp(0))}>
                      {t("Ver")}
                    </button>
                  ) : null}
                  {!ro && rv.revision !== shown ? (
                    <button className="btn small" type="button" title={t("Comparar a revisão #{a} com esta", { a: shown })} onClick={() => setCmp(cmp === rv.revision ? 0 : rv.revision)} aria-pressed={cmp === rv.revision}>
                      {cmp === rv.revision ? t("Comparando") : t("Comparar")}
                    </button>
                  ) : null}
                  {!ro && rv.status !== "deployed" && d.helm ? (
                    <button className="btn small" type="button" onClick={() => back(rv)}>
                      {t("Voltar para esta")}
                    </button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!ro && !d.helm && rel.history.length > 1 ? <p className="muted small">{t("Para voltar uma revisão, instale o helm nesta máquina (no Mac: brew install helm).")}</p> : null}
      </Sec>
      <HelmDrift base={base} />
      {ro ? (
        <Sec title="Values">
          <p className="muted small">{t("Em modo só leitura os values e o manifest não aparecem (costumam ter senhas).")}</p>
        </Sec>
      ) : (
        <>
          <div className="kd-helm-bar">
            <b>{t("Revisão #{n}", { n: shown })}</b>
            {cmp ? <span className="muted small">{t("comparada com #{n} (− o que saiu, + o que entrou)", { n: cmp })}</span> : null}
            <span className="grow" />
            {!cmp && prevOf(shown) ? (
              <button className="btn small" type="button" onClick={() => setCmp(prevOf(shown))}>
                {t("Comparar com a anterior")}
              </button>
            ) : null}
            {cmp ? (
              <button className="btn small" type="button" onClick={() => setCmp(0)}>
                {t("Parar de comparar")}
              </button>
            ) : null}
          </div>
          {err ? <div className="flash flash-err">{err}</div> : null}
          {!cur || (cmp && !old) ? (
            <div className="loading">
              <span className="spin" />
              {t("Carregando…")}
            </div>
          ) : cur.contentError ? (
            <div className="error-box">{t("Não consegui ler o conteúdo da revisão #{n}: {err}", { n: shown, err: cur.contentError })}</div>
          ) : (
            <>
              <section className="kd-sec">
                <div className="kd-sec-head">
                  <h3>Values</h3>
                  <span className="grow" />
                  <div className="seg" role="group">
                    <button className="seg-btn" type="button" aria-pressed={mode === "given"} title={t("Os values passados no install/upgrade (helm get values)")} onClick={() => setMode("given")}>
                      {t("Informados")}
                    </button>
                    <button className="seg-btn" type="button" aria-pressed={mode === "all"} title={t("Os padrões do chart com os informados por cima: o que o chart usou (helm get values --all)")} onClick={() => setMode("all")}>
                      {t("Com os padrões do chart")}
                    </button>
                  </div>
                  {vText ? <CopyBtn text={vText} /> : null}
                </div>
                {old ? (
                  <Diff a={yamlOf(valuesOf(old))} b={vText} />
                ) : vText ? (
                  <pre className="kd-pre kd-yaml kd-tall">{vText}</pre>
                ) : (
                  <p className="muted small">{mode === "given" ? t("Instalada com os values padrão do chart.") : t("O chart não tem values.")}</p>
                )}
              </section>
              {cur.notes || old?.notes ? (
                <Sec title={t("Notas")}>{old ? <Diff a={old.notes ?? ""} b={cur.notes ?? ""} /> : <pre className="kd-pre">{cur.notes}</pre>}</Sec>
              ) : null}
              <Manifest docs={docs} old={oldDocs} full={cur.manifest ?? ""} />
            </>
          )}
        </>
      )}
    </>
  );
}

type DriftObj = { kind: string; kid?: string; namespace?: string; name: string; state: "same" | "changed" | "gone" | "unknown"; changes?: { path: string; want: string; have: string }[]; managers?: string[]; error?: string };

// HelmDrift: the objects as they run against what Helm applied (only the
// fields the manifest sets), and who changed them.
function HelmDrift({ base }: { base: string }) {
  const [objs, setObjs] = useState<DriftObj[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const load = () => {
    setBusy(true);
    api<{ objects: DriftObj[] }>(base + "/drift")
      .then((x) => (setObjs(x.objects), setErr("")))
      .catch((e) => setErr((e as Error).message))
      .finally(() => setBusy(false));
  };
  useEffect(load, [base]); // eslint-disable-line react-hooks/exhaustive-deps
  const bad = (objs ?? []).filter((o) => o.state !== "same");
  const word = { changed: t("mudou no cluster"), gone: t("sumiu do cluster"), unknown: t("não consegui ler"), same: "" };
  const tone = { changed: "warn", gone: "err", unknown: "", same: "ok" } as const;
  return (
    <section className="kd-sec kd-drift">
      <div className="kd-sec-head">
        <h3>{t("Drift")}</h3>
        {objs ? <B text={bad.length ? t("{n} objetos com drift", { n: bad.length }) : t("igual ao que o Helm aplicou")} tone={bad.length ? "warn" : "ok"} /> : null}
        <span className="grow" />
        <button className="btn small" type="button" disabled={busy} onClick={load} title={t("Comparar de novo os objetos com o manifest da última revisão")}>
          {busy ? t("Comparando…") : t("Comparar de novo")}
        </button>
      </div>
      {err ? <div className="flash flash-err">{err}</div> : !objs ? (
        <div className="loading">
          <span className="spin" />
          {t("Comparando com o cluster…")}
        </div>
      ) : (
        <>
          <p className="muted small">{t("Compara cada objeto da última revisão com o que está no cluster, só nos campos que o chart define (os padrões do Kubernetes e o status não contam).")}</p>
          {bad.length ? (
            <div className="kd-objs">
              {bad.map((o) => {
                const key = o.kind + "/" + (o.namespace ?? "") + "/" + o.name;
                const isOpen = open[key] ?? o.state === "changed";
                return (
                  <div key={key} className={"kd-obj " + (o.state === "gone" ? "gone" : "changed")}>
                    <div className="kd-obj-head">
                      <button className="kd-obj-toggle" type="button" aria-expanded={isOpen} onClick={() => setOpen((m) => ({ ...m, [key]: !isOpen }))}>
                        <span className="kd-caret">{o.changes?.length ? (isOpen ? "▾" : "▸") : " "}</span>
                        <span className="mono small kd-obj-kind">{o.kind}</span>
                        <span className="mono">{o.name}</span>
                      </button>
                      <B text={word[o.state]} tone={tone[o.state]} />
                      {o.changes?.length ? <span className="muted small">{t("{n} campos", { n: o.changes.length })}</span> : null}
                      <span className="grow" />
                      {o.kid && o.state !== "gone" ? (
                        <span className="small">
                          <DLink kind={o.kid} ns={o.namespace ?? ""} name={o.name} text={t("Abrir")} />
                        </span>
                      ) : null}
                    </div>
                    {o.error ? <p className="muted small">{o.error}</p> : null}
                    {isOpen && o.changes?.length ? (
                      <>
                        <table className="kd-table kd-drift-tab">
                          <thead>
                            <tr>
                              <th>{t("Campo")}</th>
                              <th>{t("Helm aplicou")}</th>
                              <th>{t("No cluster")}</th>
                            </tr>
                          </thead>
                          <tbody>
                            {o.changes.map((ch) => (
                              <tr key={ch.path}>
                                <td className="mono small">{ch.path}</td>
                                <td className="mono small kd-drift-want">{ch.want}</td>
                                <td className="mono small kd-drift-have">{ch.have}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                        {o.managers?.length ? <p className="muted small">{t("Também escreveram neste objeto: {who}", { who: o.managers.join(" · ") })}</p> : null}
                      </>
                    ) : null}
                  </div>
                );
              })}
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}

function Manifest({ docs, old, full }: { docs: Doc[]; old: Doc[] | null; full: string }) {
  const [open, setOpen] = useState<Record<string, boolean>>({});
  if (!docs.length && !old?.length) return null;
  const before = new Map((old ?? []).map((x) => [x.key, x]));
  const rows: { doc: Doc; prev?: Doc; state: "" | "new" | "changed" | "gone" }[] = docs.map((doc) => {
    const prev = before.get(doc.key);
    return { doc, prev, state: !old ? "" : !prev ? "new" : prev.text !== doc.text ? "changed" : "" };
  });
  if (old) {
    const now = new Set(docs.map((x) => x.key));
    for (const p of old) if (!now.has(p.key)) rows.push({ doc: p, prev: p, state: "gone" });
  }
  const changed = rows.filter((r) => r.state).length;
  const tone = { new: "ok", changed: "warn", gone: "err" } as const;
  const word = { new: t("novo"), changed: t("mudou"), gone: t("saiu") };
  return (
    <section className="kd-sec">
      <div className="kd-sec-head">
        <h3>{t("Manifest · {n} objetos", { n: docs.length })}</h3>
        {old ? <span className="muted small">{changed ? t("{n} com mudança", { n: changed }) : t("sem mudanças")}</span> : null}
        <span className="grow" />
        <button className="btn small" type="button" onClick={() => setOpen(Object.fromEntries(rows.map((r) => [r.doc.key, !rows.every((x) => open[x.doc.key])])))}>
          {rows.every((x) => open[x.doc.key]) ? t("Recolher tudo") : t("Abrir tudo")}
        </button>
        {full ? <CopyBtn text={full} label={t("Copiar tudo")} /> : null}
      </div>
      <div className="kd-objs">
        {rows.map(({ doc, prev, state }) => {
          const id = KIND_IDS[doc.kind];
          const isOpen = !!open[doc.key];
          return (
            <div key={doc.key + state} className={"kd-obj" + (state ? " " + state : "")}>
              <div className="kd-obj-head">
                <button className="kd-obj-toggle" type="button" aria-expanded={isOpen} onClick={() => setOpen((o) => ({ ...o, [doc.key]: !isOpen }))}>
                  <span className="kd-caret">{isOpen ? "▾" : "▸"}</span>
                  <span className="mono small kd-obj-kind">{doc.kind}</span>
                  <span className="mono">{doc.name}</span>
                </button>
                {state ? <B text={word[state]} tone={tone[state]} /> : null}
                {doc.source ? <span className="muted small mono kd-obj-src" title={doc.source}>{doc.source.replace(/^[^/]+\/templates\//, "")}</span> : null}
                <span className="grow" />
                {id && state !== "gone" ? (
                  <span className="small">
                    <DLink kind={id} ns={doc.ns} name={doc.name} text={t("Abrir")} />
                  </span>
                ) : null}
              </div>
              {isOpen ? state === "changed" && prev ? <Diff a={prev.text} b={doc.text} /> : <pre className="kd-pre kd-yaml kd-tall">{doc.text}</pre> : null}
            </div>
          );
        })}
      </div>
    </section>
  );
}
