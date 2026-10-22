import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";

export type Folder = { id: string; name: string; parent?: string };
export type Groups = { folders: Folder[]; items: Record<string, string> };

export const folderById = (g: Groups, id: string) => g.folders.find((f) => f.id === id);

export function isUnder(g: Groups, id: string, ancestor: string): boolean {
  for (let f = folderById(g, id), n = 0; f && n < 50; f = folderById(g, f.parent ?? ""), n++) if (f.parent === ancestor) return true;
  return false;
}

// itemFolder is the folder of a machine or cluster, or "" (top level).
export function itemFolder(g: Groups, key: string): string {
  const id = g.items[key];
  return id && folderById(g, id) ? id : "";
}

// The folders as options of a <select>, indented by depth.
export function folderOptions(g: Groups, exclude?: string, top = t("— fora de pastas —")): [string, string][] {
  const out: [string, string][] = [["", top]];
  const walk = (parent: string, depth: number) => {
    for (const f of g.folders.filter((x) => (x.parent || "") === parent)) {
      if (exclude && (f.id === exclude || isUnder(g, f.id, exclude))) continue;
      out.push([f.id, "  ".repeat(depth) + f.name]);
      walk(f.id, depth + 1);
    }
  };
  walk("", 0);
  return out;
}

export async function moveToFolder(key: string, folder: string) {
  try {
    await api("/grupos-itens", { method: "PUT", body: { item: key, folder } });
    await gantry.loadGroups();
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

export function FolderSelect({ g, value, onChange, label, exclude, top }: { g: Groups; value: string; onChange: (v: string) => void; label: string; exclude?: string; top?: string }) {
  return (
    <select className="select small" aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}>
      {folderOptions(g, exclude, top).map(([v, l]) => (
        <option key={v} value={v}>
          {l}
        </option>
      ))}
    </select>
  );
}
