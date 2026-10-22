import { useEffect, useMemo, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { t } from "@/lib/i18n";
import { ErrorBox, Loading } from "@/components/ui";
import { gantry } from "@/lib/gantry";
import { openDetail } from "./detail/store";
import type { Cluster } from "./types";

// Search over the whole cluster, and the account's permissions.

type Hit = { kind: string; label: string; namespace?: string; name: string; images?: string[]; why?: string };
type SearchResult = { hits: Hit[]; skipped?: string[]; more?: boolean };

export function SearchView({ c }: { c: Cluster }) {
  const key = "kube-busca." + c.id;
  const [q, setQ] = useState<string>(() => new URLSearchParams(location.hash.split("?")[1] ?? "").get("q") ?? gantry.store.get(key, ""));
  // A search typed in the menu while this screen is open.
  useEffect(() => {
    const on = () => {
      const v = new URLSearchParams(location.hash.split("?")[1] ?? "").get("q");
      if (v != null) setQ(v);
    };
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const [res, setRes] = useState<SearchResult | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const run = (text: string, fresh = false) => {
    gantry.store.set(key, text);
    if (!text.trim()) {
      setRes(null);
      return;
    }
    const n = ++seq.current;
    setBusy(true);
    api<SearchResult>("/clusters/" + enc(c.id) + "/busca?q=" + enc(text) + (fresh ? "&novo=1" : ""))
      .then((r) => n === seq.current && (setRes(r), setErr("")))
      .catch((e) => n === seq.current && setErr((e as Error).message))
      .finally(() => n === seq.current && setBusy(false));
  };
  useEffect(() => {
    const id = setTimeout(() => run(q), 250);
    return () => clearTimeout(id);
  }, [q]); // eslint-disable-line react-hooks/exhaustive-deps
  const groups = useMemo(() => {
    const m = new Map<string, Hit[]>();
    for (const h of res?.hits ?? []) m.set(h.label, [...(m.get(h.label) ?? []), h]);
    return [...m.entries()];
  }, [res]);
  return (
    <div>
      <div className="block-head">
        <h2 className="block-title">{t("Busca no cluster")}</h2>
        <span className="grow" />
        <button className="btn small" type="button" onClick={() => run(q, true)} title={t("Ler o cluster de novo (a busca guarda o que leu por 30 s)")}>
          {t("Atualizar")}
        </button>
      </div>
      <input
        className="filter kube-search-input"
        autoFocus
        spellCheck={false}
        placeholder={t("nome, label:app=web, image:nginx, ns:loja, kind:deploy")}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <p className="muted small">{t("Junte termos: web image:nginx ns:loja acha os objetos com web no nome, da imagem nginx, no namespace loja.")}</p>
      {err ? <ErrorBox>{err}</ErrorBox> : null}
      {busy && !res ? <Loading /> : null}
      {res ? (
        <>
          <p className="muted small">
            {t("{n} resultados", { n: res.hits.length }) + (res.more ? " " + t("(há mais; refine a busca)") : "")}
            {res.skipped?.length ? " · " + t("sem permissão para ler: {kinds}", { kinds: res.skipped.join(", ") }) : ""}
          </p>
          {groups.map(([label, hits]) => (
            <div key={label} className="panel kube-hits">
              <div className="kube-hits-head">
                <b>{label}</b> <span className="muted small">{hits.length}</span>
              </div>
              {hits.map((h) => (
                <a
                  key={h.kind + (h.namespace ?? "") + h.name}
                  href="#"
                  className="kube-hit"
                  onClick={(e) => {
                    e.preventDefault();
                    openDetail(c, h.kind, h.namespace ?? "", h.name);
                  }}
                >
                  <span className="mono">{h.name}</span>
                  {h.namespace ? <span className="muted small">{h.namespace}</span> : null}
                  {h.why ? <span className="muted small mono">{h.why}</span> : null}
                </a>
              ))}
            </div>
          ))}
        </>
      ) : null}
    </div>
  );
}

type PermRow = { kind: string; label: string; verbs: Record<string, boolean>; some?: Record<string, boolean> };
type PermTable = { namespace: string; user?: string; groups?: string[]; incomplete: boolean; rows: PermRow[]; rules: { verbs: string[]; apiGroups: string[]; resources: string[]; resourceNames?: string[] }[] };

const VERB_LABEL: Record<string, string> = { exec: "shell", logs: "logs", portforward: "port-fwd", debug: "debug", scale: "scale" };

export function PermsView({ c, ns, namespaces }: { c: Cluster; ns: string; namespaces: string[] }) {
  const [sel, setSel] = useState(ns || "default");
  const [d, setD] = useState<{ table: PermTable; verbs: string[] } | null>(null);
  const [err, setErr] = useState("");
  const [filter, setFilter] = useState(() => new URLSearchParams(location.hash.split("?")[1] ?? "").get("tipo") ?? "");
  useEffect(() => {
    setD(null);
    api<{ table: PermTable; verbs: string[] }>("/clusters/" + enc(c.id) + "/permissoes?ns=" + enc(sel))
      .then((x) => (setD(x), setErr("")))
      .catch((e) => setErr((e as Error).message));
  }, [sel, c.id]);
  const rows = (d?.table.rows ?? []).filter((r) => !filter || (r.kind + " " + r.label).toLowerCase().includes(filter.toLowerCase()));
  return (
    <div>
      <div className="block-head">
        <h2 className="block-title">{t("Minhas permissões")}</h2>
        <span className="grow" />
        <select className="select" value={sel} onChange={(e) => setSel(e.target.value)} aria-label="Namespace">
          {[...new Set([sel, ...namespaces])].map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      </div>
      {err ? <ErrorBox>{err}</ErrorBox> : !d ? <Loading /> : null}
      {d ? (
        <>
          <p className="muted small">
            {d.table.user ? t("Conta: {user}", { user: d.table.user }) + (d.table.groups?.length ? " · " + t("grupos: {g}", { g: d.table.groups.join(", ") }) : "") : t("O cluster não diz quem é a conta (Kubernetes anterior a 1.28).")}
          </p>
          {d.table.incomplete ? <div className="banner warn">{t("O cluster avisou que a lista pode estar incompleta (autorizadores além do RBAC, como o IAM do GKE). Uma ação pode ser permitida mesmo sem aparecer aqui.")}</div> : null}
          <input className="filter" placeholder={t("Filtrar tipos…")} value={filter} onChange={(e) => setFilter(e.target.value)} />
          <div className="panel table-wrap">
            <table className="grid kube-grid perm-grid">
              <thead>
                <tr>
                  <th>{t("Tipo")}</th>
                  {d.verbs.map((v) => (
                    <th key={v} className="num">
                      {VERB_LABEL[v] ?? v}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.kind}>
                    <td>{r.label}</td>
                    {d.verbs.map((v) => (
                      <td key={v} className={"num " + (r.verbs[v] ? "perm-yes" : r.some?.[v] ? "perm-some" : "perm-no")} title={r.some?.[v] ? t("só para alguns objetos (resourceNames)") : undefined}>
                        {r.verbs[v] ? "✓" : r.some?.[v] ? "◐" : "·"}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <details className="np-adv">
            <summary>{t("As regras como o cluster dá ({n})", { n: d.table.rules.length })}</summary>
            <pre className="kd-pre">{d.table.rules.map((r) => `${r.verbs.join(",")}  ${r.apiGroups.map((g) => g || "core").join(",")}  ${r.resources.join(",")}${r.resourceNames?.length ? "  [" + r.resourceNames.join(",") + "]" : ""}`).join("\n")}</pre>
          </details>
        </>
      ) : null}
    </div>
  );
}

// forbidden: a 403 of the cluster, to offer the permissions screen.
export const forbidden = (msg: string) => /forbidden|\b403\b|cannot (list|get|create|patch|delete|watch)/i.test(msg);
