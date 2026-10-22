import { create } from "zustand";
import { api, enc } from "@/lib/api";
import type { Cluster } from "../types";

/* eslint-disable @typescript-eslint/no-explicit-any */
export type DetailItem = { kind: string; ns: string; name: string };

type DetailState = {
  c: Cluster | null;
  stack: DetailItem[];
  data: any;
  err: string;
  seq: number;
};

export const useDetail = create<DetailState>(() => ({ c: null, stack: [], data: null, err: "", seq: 0 }));

export function openDetail(c: Cluster, kind: string, ns: string, name: string, push = false) {
  const s = useDetail.getState();
  const item = { kind, ns: ns || "", name };
  const stack = push && s.c?.id === c.id ? [...s.stack, item] : [item];
  useDetail.setState({ c, stack, data: null, err: "" });
  void loadDetail();
}

export function closeDetail() {
  useDetail.setState({ c: null, stack: [], data: null, err: "" });
}

export function backDetail() {
  const s = useDetail.getState();
  if (s.stack.length <= 1) return closeDetail();
  useDetail.setState({ stack: s.stack.slice(0, -1), data: null, err: "" });
  void loadDetail();
}

export async function loadDetail(quiet = false) {
  const s = useDetail.getState();
  const it = s.stack[s.stack.length - 1];
  if (!it || !s.c) return;
  const seq = s.seq + 1;
  useDetail.setState({ seq });
  const base = "/clusters/" + enc(s.c.id);
  try {
    const d =
      it.kind === "helm"
        ? await api(base + "/helm/" + enc(it.ns) + "/" + enc(it.name))
        : await api(base + "/r/" + it.kind + "/" + enc(it.ns || "-") + "/" + enc(it.name) + "/detalhe");
    if (useDetail.getState().seq !== seq) return;
    // Same as before: no redraw (the panel would blink every few seconds).
    if (quiet && !useDetail.getState().err && JSON.stringify(d) === JSON.stringify(useDetail.getState().data)) return;
    useDetail.setState({ data: d, err: "" });
  } catch (e) {
    if (useDetail.getState().seq !== seq) return;
    useDetail.setState(quiet ? { err: (e as Error).message } : { err: (e as Error).message, data: null });
  }
}
