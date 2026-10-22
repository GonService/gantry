/* eslint-disable @typescript-eslint/no-explicit-any */
import { Loading } from "@/components/ui";
import { t } from "@/lib/i18n";
import type { Cluster } from "@/views/kube/types";
import type { Groups } from "./folders";
import { ClustersTab, CredentialsTab, FoldersTab, GeneralTab, MachinesTab, type MachineInfo } from "./tabs";

const TABS: [string, string][] = [
  ["maquinas", t("Máquinas")],
  ["clusters", "Clusters"],
  ["pastas", t("Pastas")],
  ["credenciais", t("Credenciais")],
  ["geral", t("Geral")],
];

export type SettingsProps = { tab: string; groups: Groups | null; clusters: Cluster[]; machines: MachineInfo[]; firstHost: any };

export function SettingsPage({ tab, groups, clusters, machines, firstHost }: SettingsProps) {
  const cur = tab || "maquinas";
  return (
    <div>
      <div className="page-head">
        <div className="page-title">
          <h1>{t("Configuração")}</h1>
          <div className="sub">{t("Máquinas, pastas da barra lateral, credenciais e preferências")}</div>
        </div>
      </div>
      <div className="tabs" role="tablist">
        {TABS.map(([id, label]) => (
          <a key={id} className="tab" role="tab" href={"#/config/" + id} aria-selected={cur === id}>
            {label}
          </a>
        ))}
      </div>
      {!groups ? (
        <Loading />
      ) : cur === "maquinas" ? (
        <MachinesTab machines={machines} groups={groups} />
      ) : cur === "clusters" ? (
        <ClustersTab clusters={clusters} groups={groups} />
      ) : cur === "pastas" ? (
        <FoldersTab machines={machines} groups={groups} />
      ) : cur === "credenciais" ? (
        <CredentialsTab firstHost={firstHost} />
      ) : (
        <GeneralTab machines={machines} />
      )}
    </div>
  );
}
