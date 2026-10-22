import { create } from "zustand";
import { api } from "@/lib/api";

export type Tool = {
  id: string;
  name: string;
  area: string;
  why: string;
  need: "essencial" | "recomendado" | "opcional";
  have: boolean;
  found?: string;
  install?: string;
  note?: string;
  brew?: boolean;
};

export type Report = { os: string; arch: string; pm?: string; local: boolean; tools: Tool[]; missing: number };

type Entry = { data?: Report; err?: string; loading?: boolean; at?: number };

type ToolsState = {
  byKey: Record<string, Entry>;
  load: (key: string, url: string, fresh?: boolean) => Promise<void>;
};

const MAX_AGE = 60_000;

// The tools of each machine (key: the machine), shared by the tab, the
// warnings of the project pages and the welcome page.
export const useTools = create<ToolsState>((set, get) => ({
  byKey: {},
  load: async (key, url, fresh) => {
    const cur = get().byKey[key];
    if (cur?.loading) return;
    set((s) => ({ byKey: { ...s.byKey, [key]: { ...cur, loading: true } } }));
    let next: Entry;
    try {
      const data = await api<Report>(url + (fresh ? "?fresh=1" : ""));
      next = { data, at: Date.now() };
    } catch (e) {
      next = { ...cur, err: e instanceof Error ? e.message : String(e), at: Date.now() };
    }
    set((s) => ({ byKey: { ...s.byKey, [key]: next } }));
  },
}));

export function needsLoad(e: Entry | undefined): boolean {
  return !e || (!e.loading && Date.now() - (e.at ?? 0) > MAX_AGE);
}

export function toolsURL(local: boolean, host?: string): string {
  return local ? "/local/ferramentas" : "/hosts/" + encodeURIComponent(host ?? "") + "/ferramentas";
}
