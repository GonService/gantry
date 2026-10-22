import { useEffect, useState } from "react";
import { gantry } from "@/lib/gantry";
import { Chev } from "@/components/icons";
import { t } from "@/lib/i18n";
import { kubeHref, type KindInfo } from "./types";

export const KMENU: [string, [string, string][]][] = [
  ["Visão geral", [["", t("Visão geral")]]],
  [
    "Workloads",
    [
      ["pods", "Pods"],
      ["deployments", "Deployments"],
      ["statefulsets", "StatefulSets"],
      ["daemonsets", "DaemonSets"],
      ["replicasets", "ReplicaSets"],
      ["jobs", "Jobs"],
      ["cronjobs", "CronJobs"],
      ["hpas", "Autoscalers"],
    ],
  ],
  [
    "Rede",
    [
      ["services", "Services"],
      ["ingresses", "Ingresses"],
      ["endpoints", "Endpoints"],
      ["networkpolicies", "Network Policies"],
    ],
  ],
  [
    "Configuração",
    [
      ["configmaps", "ConfigMaps"],
      ["secrets", "Secrets"],
      ["serviceaccounts", "Service Accounts"],
    ],
  ],
  [
    "Armazenamento",
    [
      ["pvcs", "PVCs"],
      ["pvs", "PVs"],
      ["storageclasses", "Storage Classes"],
    ],
  ],
  ["Helm", [["helm", "Releases"]]],
  [
    "Cluster",
    [
      ["nodes", t("Nós")],
      ["namespaces", "Namespaces"],
      ["events", t("Eventos")],
      ["busca", t("Busca")],
      ["permissoes", t("Minhas permissões")],
    ],
  ],
];

// The group names above are keys (remembered as closed); these are shown.
const GROUP_TITLE: Record<string, string> = {
  Rede: t("Rede"),
  "Configuração": t("Configuração"),
  Armazenamento: t("Armazenamento"),
};

// A group that opens and closes; open is remembered by the caller.
function Group({
  title,
  n,
  open,
  onToggle,
  sub,
  titleAttr,
  children,
}: {
  title: string;
  n: number;
  open: boolean;
  onToggle: (now: boolean) => void;
  sub?: boolean;
  titleAttr?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={sub ? undefined : "kube-group"}>
      <button type="button" className={(sub ? "kube-sub-head" : "kube-group-head") + (open ? " open" : "")} aria-expanded={open} title={titleAttr} onClick={() => onToggle(!open)}>
        <Chev size={sub ? 12 : 14} />
        <span>{title}</span>
        <span className="kube-group-n">{n}</span>
      </button>
      <div className={sub ? "kube-sub-items" : "kube-group-items"} hidden={!open}>
        {children}
      </div>
    </div>
  );
}

// The cluster menu: groups that open and close (remembered); going to a
// screen opens its group, which can be closed again; custom resources by
// API group.
export function KubeMenu({ clusterId, kind, crds }: { clusterId: string; kind: string; crds: KindInfo[] }) {
  const [closed, setClosed] = useState<Record<string, boolean>>(() => gantry.store.get("kube-menu-fechados", {}));
  const [crdOpen, setCrdOpen] = useState<Record<string, boolean>>(() => gantry.store.get("kube-menu-crd-abertos", {}));
  const toggleGroup = (g: string, open: boolean) => {
    const next = { ...closed };
    if (open) delete next[g];
    else next[g] = true;
    setClosed(next);
    gantry.store.set("kube-menu-fechados", next);
  };
  const toggleCRD = (g: string, open: boolean) => {
    const next = { ...crdOpen };
    if (open) next[g] = true;
    else delete next[g];
    setCrdOpen(next);
    gantry.store.set("kube-menu-crd-abertos", next);
  };
  const link = (id: string, label: string, title?: string) => (
    <a key={id} className={"kube-link" + (kind === id ? " sel" : "")} href={kubeHref(clusterId, id)} title={title}>
      {label}
    </a>
  );
  const byGroup: Record<string, KindInfo[]> = {};
  for (const k of crds) (byGroup[k.group ?? ""] ??= []).push(k);
  // Arriving at a screen opens its group (once: closing it again sticks).
  const crdOfKind = crds.find((k) => k.id === kind);
  useEffect(() => {
    const g = KMENU.find(([, items]) => items.some(([id]) => id === kind))?.[0];
    if (g && closed[g]) toggleGroup(g, true);
    if (crdOfKind) {
      const want = { "*": true, [crdOfKind.group ?? ""]: true };
      if (Object.keys(want).some((k) => !crdOpen[k])) {
        const next = { ...crdOpen, ...want };
        setCrdOpen(next);
        gantry.store.set("kube-menu-crd-abertos", next);
      }
    }
  }, [kind, !!crdOfKind]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <nav className="kube-menu" aria-label={t("Recursos do cluster")}>
      <form
        className="kube-menu-search"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          const v = String(new FormData(e.currentTarget).get("q") ?? "").trim();
          gantry.store.set("kube-busca." + clusterId, v);
          location.hash = kubeHref(clusterId, "busca") + (v ? "?q=" + encodeURIComponent(v) : "");
        }}
      >
        <input
          name="q"
          type="search"
          className="filter"
          placeholder={t("Buscar no cluster…")}
          title={t("Nome, imagem (image:nginx), label (label:app=web), namespace (ns:loja), tipo (kind:deploy); Enter busca")}
          aria-label={t("Buscar no cluster")}
          spellCheck={false}
          autoComplete="off"
        />
      </form>
      {KMENU.map(([group, items]) => {
        if (group === "Visão geral")
          return (
            <a key="home" className={"kube-link kube-home" + (!kind ? " sel" : "")} href={kubeHref(clusterId, "")}>
              {t("Visão geral")}
            </a>
          );
        return (
          <Group key={group} title={GROUP_TITLE[group] ?? group} n={items.length} open={!closed[group]} onToggle={(o) => toggleGroup(group, o)}>
            {items.map(([id, label]) => link(id, label))}
          </Group>
        );
      })}
      {crds.length ? (
        <Group title={t("Customizados")} n={crds.length} open={!!crdOpen["*"]} onToggle={(o) => toggleCRD("*", o)}>
          {Object.keys(byGroup)
            .sort()
            .map((g) => (
              <Group key={g} sub title={g} titleAttr={g} n={byGroup[g].length} open={!!crdOpen[g]} onToggle={(o) => toggleCRD(g, o)}>
                {byGroup[g].map((k) => link(k.id, k.label, k.apiVersion))}
              </Group>
            ))}
        </Group>
      ) : null}
    </nav>
  );
}
