/* eslint-disable @typescript-eslint/no-explicit-any */
export type Cluster = {
  id: string;
  name: string;
  source: string;
  file: string;
  context: string;
  mode: "livre" | "leitura";
  lab?: string;
  plugin?: string;
  pluginHint?: string;
};

export type KindInfo = { id: string; label: string; group?: string; apiVersion?: string; namespaced: boolean; columns?: string[]; custom?: boolean };

export type Row = {
  name: string;
  namespace?: string;
  created: number;
  cells: string[];
  problem?: string;
  info?: Record<string, any>;
};

export type Usage = { cpu: number; mem: number };

export type Perms = Partial<Record<"update" | "delete" | "patch" | "scale" | "exec" | "logs" | "portforward" | "createJobs" | "proxy" | "secrets" | "evict" | "debug", boolean>>;

export type ListResp = { rows: Row[]; columns: string[]; can?: Perms; usage?: Record<string, Usage> };

export type Overview = {
  server: string;
  version: string;
  namespaces?: { name: string }[];
  nodes?: Row[];
  pods?: Row[];
  deployments?: Row[];
  statefulsets?: Row[];
  daemonsets?: Row[];
  jobs?: Row[];
  events?: Row[];
  nodeUsage?: Record<string, Usage>;
};

export type HelmRow = { name: string; namespace: string; chart: string; appVersion: string; revision: number; status: string; updated: number };

export type Forward = { id: string; cluster: string; ns: string; kind: string; name: string; port: number; local: number; conns: number; total: number; lastError?: string };

export const ALL_PERMS: Required<Perms> = {
  update: true,
  delete: true,
  patch: true,
  scale: true,
  exec: true,
  logs: true,
  portforward: true,
  createJobs: true,
  proxy: true,
  secrets: true,
  evict: true,
  debug: true,
};

export const kubeHref = (id: string, kind?: string) => "#/k/" + encodeURIComponent(id) + (kind ? "/" + kind : "");
