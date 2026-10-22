import { fmtBytes, kAge } from "@/lib/format";
import { FilterInput, SortTh, filterItems, useFilter, usePage, useSort } from "@/components/list";
import { Loading } from "@/components/ui";
import { BulkBar, SelectAll, SelectCell, reportBulk, runBulk, someNames, useSelection } from "@/components/select";
import { confirm } from "@/components/dialog";
import { api, enc } from "@/lib/api";
import { useState } from "react";
import { loadView } from "./store";
import { LOCALE, t, tn } from "@/lib/i18n";
import { KubeActions, NameLink, kubeMenu } from "./actions";
import { openDetail } from "./detail/store";
import type { Cluster, HelmRow, KindInfo, ListResp } from "./types";

export function KubeList({ c, kind, K, data, ns }: { c: Cluster; kind: string; K?: KindInfo; data?: ListResp; ns: string }) {
  const label = K ? K.label : kind;
  const fk = "kube:" + c.id + ":" + kind;
  const [q] = useFilter(fk);
  const usage = data?.usage ?? {};
  const hasUsage = kind === "pods" && Object.keys(usage).length > 0;
  const showNS = !!K?.namespaced && !ns;
  // Column names are shown as they are; "Último" comes from the server (events).
  const NAME = t("Nome"),
    AGE = t("Idade"),
    LAST = t("Último"),
    USE = t("CPU / Memória");
  const cols = [NAME, showNS ? "Namespace" : null, ...(data?.columns ?? []), hasUsage ? USE : null, kind === "events" ? null : AGE].filter((x): x is string => x !== null);
  const def = kind === "events" ? { col: LAST, dir: 1 as const } : { col: NAME, dir: 1 as const };
  const [sort, sortBy] = useSort("kube-ordem." + kind, def, cols);
  const rows = data?.rows ?? [];
  const now = Date.now() / 1000;
  const sortVal = (r: (typeof rows)[number], col: string): string | number => {
    if (col === NAME) return r.name;
    if (col === "Namespace") return r.namespace ?? "";
    if (col === AGE || col === LAST) return now - (r.created || 0);
    if (col === USE) return usage[r.namespace + "/" + r.name]?.cpu ?? -1;
    const i = data!.columns.indexOf(col);
    return i >= 0 ? r.cells[i] || "" : "";
  };
  const coll = new Intl.Collator(LOCALE, { numeric: true, sensitivity: "base" });
  const filtered = filterItems(q, rows, (r) => r.name + " " + (r.namespace ?? "") + " " + r.cells.join(" ") + " " + (r.problem ?? ""));
  const sorted = [...filtered].sort((a, b) => {
    const x = sortVal(a, sort.col),
      y = sortVal(b, sort.col);
    const d = typeof x === "number" && typeof y === "number" ? x - y : coll.compare(String(x), String(y));
    return (d || coll.compare(a.name, b.name)) * sort.dir;
  });
  const pg = usePage(fk, sorted);
  // Several at once: delete (and restart, for workloads).
  const ro = c.mode === "leitura";
  const can = { delete: true, patch: true, ...(data?.can ?? {}) };
  const canDelete = !ro && can.delete !== false && kind !== "events" && kind !== "namespaces" && kind !== "nodes";
  const canRestart = !ro && can.patch !== false && (kind === "deployments" || kind === "statefulsets" || kind === "daemonsets");
  const selectable = canDelete || canRestart;
  const keyOf = (r: (typeof rows)[number]) => (r.namespace ?? "") + "/" + r.name;
  const sel = useSelection(
    rows.map(keyOf),
    pg.items.map(keyOf),
  );
  const [busy, setBusy] = useState("");
  const chosen = () => rows.filter((r) => sel.has(keyOf(r)));
  const bulk = async (what: "delete" | "restart") => {
    const list = chosen();
    if (!list.length) return;
    const names = someNames(list.map((r) => r.name));
    const ok =
      what === "delete"
        ? await confirm(
            tn(list.length, "Apagar {n} item?", "Apagar {n} itens?"),
            (kind === "pods" ? t("Os pods de um deployment são criados de novo no lugar.") + " " : "") + names,
            t("Apagar"),
            true,
          )
        : await confirm(tn(list.length, "Reiniciar {n} item?", "Reiniciar {n} itens?"), t("Os pods são recriados aos poucos (rollout restart).") + " " + names, t("Reiniciar"));
    if (!ok) return;
    const verb = what === "delete" ? t("Apagando") : t("Reiniciando");
    const fails = await runBulk(
      list,
      (r) => r.name,
      async (r) => {
        const path = "/clusters/" + enc(c.id) + "/r/" + kind + "/" + enc(r.namespace || "-") + "/" + enc(r.name);
        await api(what === "delete" ? path : path + "/reiniciar", what === "delete" ? { method: "DELETE" } : { method: "POST", body: {} });
      },
      setBusy,
      verb,
    );
    setBusy("");
    sel.clear();
    reportBulk(list.length, fails, what === "delete" ? tn(list.length, "{n} item apagado", "{n} itens apagados") : tn(list.length, "{n} item reiniciando", "{n} itens reiniciando"));
    void loadView(c.id, kind, true);
  };
  return (
    <div>
      <div className="block-head">
        <h2 className="block-title">{label + (data ? " · " + data.rows.length : "")}</h2>
        <span className="grow" />
        {data && data.rows.length > 8 ? <FilterInput listKey={fk} placeholder={t("Filtrar {what}…", { what: label.toLowerCase() })} /> : null}
      </div>
      {!data ? (
        <Loading />
      ) : !data.rows.length ? (
        <p className="muted">{ns ? t("Nenhum item em {ns}.", { ns }) : t("Nenhum item.")}</p>
      ) : (
        <>
          {selectable ? (
            <BulkBar s={sel} busy={busy}>
              {canRestart ? (
                <button type="button" className="btn small" onClick={() => void bulk("restart")}>
                  {t("Reiniciar")}
                </button>
              ) : null}
              {canDelete ? (
                <button type="button" className="btn small danger" onClick={() => void bulk("delete")}>
                  {t("Apagar")}
                </button>
              ) : null}
            </BulkBar>
          ) : null}
          <div className="panel table-wrap">
            <table className="grid kube-grid">
              <thead>
                <tr>
                  {selectable ? <SelectAll s={sel} keys={pg.items.map(keyOf)} /> : null}
                  {cols.map((col) => (
                    <SortTh key={col} col={col} sort={sort} onSort={sortBy} />
                  ))}
                  <th className="act">
                    <span className="sr">{t("Ações")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {pg.items.map((r) => {
                  const u = kind === "pods" ? usage[r.namespace + "/" + r.name] : undefined;
                  return (
                    <tr key={(r.namespace ?? "") + "/" + r.name} className={(r.problem ? "kube-problem" : "") + (sel.has(keyOf(r)) ? " selected" : "")} onContextMenu={(e) => kubeMenu(e, c, kind, r, { can: data.can })}>
                      {selectable ? <SelectCell s={sel} k={keyOf(r)} label={r.name} /> : null}
                      <td>
                        <NameLink c={c} kind={kind} r={r} />
                        {r.problem ? (
                          <div className="kube-why" title={r.problem}>
                            {r.problem}
                          </div>
                        ) : null}
                      </td>
                      {showNS ? <td className="small">{r.namespace}</td> : null}
                      {r.cells.map((x, i) => {
                        const s = x || "";
                        return (
                          <td key={i} className={"small" + (s.length < 32 ? " short" : s.length > 60 && !/\s/.test(s) ? " long" : "")} title={s.length > 60 ? s : undefined}>
                            {x}
                          </td>
                        );
                      })}
                      {hasUsage ? <td className="small nowrap">{u ? u.cpu + "m · " + fmtBytes(u.mem) : "—"}</td> : null}
                      {kind === "events" ? null : <td className="small">{kAge(r.created)}</td>}
                      <td className="act">
                        <KubeActions c={c} kind={kind} r={r} opts={{ can: data.can }} />
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {pg.bar}
        </>
      )}
    </div>
  );
}

export function HelmList({ c, list, ns }: { c: Cluster; list?: HelmRow[]; ns: string }) {
  return (
    <div>
      <div className="block-head">
        <h2 className="block-title">{t("Releases do Helm") + (list ? " · " + list.length : "")}</h2>
      </div>
      {!list ? (
        <Loading />
      ) : !list.length ? (
        <p className="muted">{ns ? t("Nenhuma release do Helm em {ns}.", { ns }) : t("Nenhuma release do Helm.")}</p>
      ) : (
        <div className="panel table-wrap">
          <table className="grid kube-grid">
            <thead>
              <tr>
                {["Release", "Namespace", "Chart", t("Versão do app"), t("Revisão"), t("Estado"), t("Atualizada")].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {list.map((x) => (
                <tr key={x.namespace + "/" + x.name} className={x.status === "deployed" ? "" : "kube-problem"}>
                  <td>
                    <a
                      href="#"
                      onClick={(e) => {
                        e.preventDefault();
                        openDetail(c, "helm", x.namespace, x.name);
                      }}
                    >
                      <b>{x.name}</b>
                    </a>
                  </td>
                  <td className="small">{x.namespace}</td>
                  <td className="small mono">{x.chart}</td>
                  <td className="small">{x.appVersion}</td>
                  <td className="small">{x.revision}</td>
                  <td>
                    <span className={"badge " + (x.status === "deployed" ? "ok" : "warn")}>{x.status}</span>
                  </td>
                  <td className="small">{kAge(x.updated)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
