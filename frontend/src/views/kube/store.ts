import { create } from "zustand";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t, tn } from "@/lib/i18n";
import type { Forward, HelmRow, KindInfo, ListResp, Overview } from "./types";

type ClusterData = {
  ov?: Overview;
  helm?: HelmRow[];
  lists: Record<string, ListResp>;
  namespaces?: string[];
  err: string;
  loading: string;
  at: number;
};

type KubeState = {
  data: Record<string, ClusterData>;
  crds: Record<string, { kinds: KindInfo[]; at: number; loading?: boolean }>;
  forwards: Record<string, Forward[]>;
  ns: Record<string, string>;
};

export const useKube = create<KubeState>(() => ({ data: {}, crds: {}, forwards: {}, ns: {} }));

const empty = (): ClusterData => ({ lists: {}, err: "", loading: "", at: 0 });

function patch(id: string, f: (d: ClusterData) => Partial<ClusterData>) {
  useKube.setState((s) => {
    const cur = s.data[id] ?? empty();
    return { data: { ...s.data, [id]: { ...cur, ...f(cur) } } };
  });
}

export function kubeNS(id: string): string {
  const v = useKube.getState().ns[id];
  return v != null ? v : gantry.store.get("kube-ns." + id, "");
}

export function setKubeNS(id: string, ns: string) {
  useKube.setState((s) => ({ ns: { ...s.ns, [id]: ns } }));
  gantry.store.set("kube-ns." + id, ns);
}

// loadView reads the screen in sight: the overview (kind ""), the Helm
// releases or a list. quiet: a refresh that changes nothing does not
// touch the state (no redraw).
export async function loadView(id: string, kind: string, quiet = false) {
  const cur = useKube.getState().data[id] ?? empty();
  const k = kind || "overview";
  void loadCRDs(id);
  if (!quiet || !useKube.getState().forwards[id]) void loadForwards(id);
  if (cur.loading === k) return;
  patch(id, () => ({ loading: k }));
  const ns = kubeNS(id);
  const q = ns ? "?ns=" + enc(ns) : "";
  const base = "/clusters/" + enc(id);
  try {
    if (kind === "busca" || kind === "permissoes") {
      // Screens that read on their own.
      patch(id, () => ({ loading: "", err: "", at: Date.now() }));
      const d = useKube.getState().data[id];
      if (!d?.namespaces && !d?.ov) void loadNamespaces(id);
    } else if (!kind) {
      const ov = await api<Overview>(base + "/visao" + q);
      patch(id, (d) => (quiet && JSON.stringify(d.ov) === JSON.stringify(ov) && !d.err ? { loading: "", at: Date.now() } : { ov, err: "", loading: "", at: Date.now() }));
    } else if (kind === "helm") {
      const helm = await api<HelmRow[]>(base + "/helm" + q);
      patch(id, (d) => (quiet && JSON.stringify(d.helm) === JSON.stringify(helm) && !d.err ? { loading: "", at: Date.now() } : { helm, err: "", loading: "", at: Date.now() }));
    } else {
      const list = await api<ListResp>(base + "/r/" + kind + q);
      patch(id, (d) =>
        quiet && JSON.stringify(d.lists[kind]) === JSON.stringify(list) && !d.err
          ? { loading: "", at: Date.now() }
          : { lists: { ...d.lists, [kind]: list }, err: "", loading: "", at: Date.now() },
      );
      const d = useKube.getState().data[id];
      if (!d?.namespaces && !d?.ov) void loadNamespaces(id);
    }
  } catch (e) {
    patch(id, () => ({ err: (e as Error).message, loading: "", at: Date.now() }));
  }
}

async function loadNamespaces(id: string) {
  try {
    const r = await api<ListResp>("/clusters/" + enc(id) + "/r/namespaces");
    patch(id, () => ({ namespaces: r.rows.map((x) => x.name) }));
  } catch {
    /* nothing */
  }
}

export async function loadCRDs(id: string) {
  const cur = useKube.getState().crds[id];
  if (cur && (cur.loading || Date.now() - cur.at < 120000)) return;
  useKube.setState((s) => ({ crds: { ...s.crds, [id]: { kinds: cur?.kinds ?? [], at: cur?.at ?? 0, loading: true } } }));
  let kinds = cur?.kinds ?? [];
  try {
    kinds = (await api<{ kinds: KindInfo[] }>("/clusters/" + enc(id) + "/crds")).kinds ?? [];
  } catch {
    /* keep */
  }
  useKube.setState((s) => ({ crds: { ...s.crds, [id]: { kinds, at: Date.now() } } }));
}

export async function loadForwards(id: string) {
  try {
    const list = await api<Forward[]>("/encaminhamentos?cluster=" + enc(id));
    useKube.setState((s) => (JSON.stringify(s.forwards[id]) === JSON.stringify(list) ? s : { forwards: { ...s.forwards, [id]: list } }));
  } catch {
    /* nothing */
  }
}

// The state of a cluster for the sidebar: [dot, text].
export function clusterState(id: string): [string, string] {
  const st = useKube.getState().data[id];
  if (!st || !st.ov) {
    if (st && st.err) return ["err", t("sem resposta")];
    // Opened on a list (the overview not read yet): connected all the same.
    if (st && Object.keys(st.lists).length) return ["on", t("conectado")];
    return ["idle", t("não aberto")];
  }
  const probs = (st.ov.pods ?? []).filter((p) => p.problem).length;
  return probs ? ["warn", tn(probs, "{n} pod com problema", "{n} pods com problema")] : ["on", st.ov.version];
}
