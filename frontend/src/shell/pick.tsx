// pickPath opens the system dialog to choose a folder (kind "pasta"), where
// to save a file ("salvar", with name) or a file to open ("abrir"). It
// resolves to the path, or null when the user cancels. Without a native
// dialog (a Linux without zenity) it asks for the path in a form.
import { api } from "@/lib/api";
import { formDialog } from "@/components/dialog";
import { t } from "@/lib/i18n";

export type PickOpts = { kind: "pasta" | "salvar" | "abrir"; title: string; dir?: string; name?: string };

export async function pickPath({ kind, title, dir, name }: PickOpts): Promise<string | null> {
  try {
    const r = await api<{ canceled?: boolean; path: string }>("/escolher", { method: "POST", body: { kind, title, dir: dir || "", name: name || "" } });
    return r.canceled ? null : r.path;
  } catch (e) {
    return new Promise((resolve) => {
      let done = false;
      formDialog({
        title,
        intro: (e as Error).message,
        submit: t("Usar"),
        fields: [{ id: "p", label: kind === "pasta" ? t("Pasta") : t("Arquivo"), value: kind === "salvar" && dir ? dir.replace(/\/$/, "") + "/" + (name || "") : dir || "" }],
        onSubmit: async (v) => {
          if (!v.p) throw new Error(t("Informe o caminho."));
          done = true;
          resolve(String(v.p));
        },
        onClose: () => {
          if (!done) resolve(null);
        },
      });
    });
  }
}
