import { create } from "zustand";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { forgetMachine } from "@/views/machine/store";

export type Template = { id: string; name: string; category?: string; description?: string; yaml: string; builtin?: boolean; files?: Record<string, string> };

export type CheckVar = { name: string; default?: string; required?: boolean; message?: string };
export type Check = {
  error?: string;
  services?: { name: string; image: string; runtime?: string; ports: string[] }[];
  vms?: { name: string; image: string }[];
  networks?: string[];
  vars?: CheckVar[];
  uses?: Record<string, Record<string, string>>;
};

type TplState = { list: Template[] | null; err: string; loading: boolean };

export const useTemplates = create<TplState>(() => ({ list: null, err: "", loading: false }));

export async function loadTemplates() {
  if (useTemplates.getState().loading) return;
  useTemplates.setState({ loading: true });
  try {
    useTemplates.setState({ list: await api<Template[]>("/templates"), err: "", loading: false });
  } catch (e) {
    useTemplates.setState({ err: (e as Error).message, loading: false });
  }
}

export async function checkYAML(yaml: string): Promise<Check> {
  try {
    return await api<Check>("/templates/check", { method: "POST", body: { yaml } });
  } catch (e) {
    return { error: (e as Error).message, vars: [] };
  }
}

export const tplHref = (id: string, deploy = false) => "#/modelos/" + encodeURIComponent(id) + (deploy ? "/gerar" : "");

export const slug = (s: string) =>
  String(s || "")
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);

// tplTags guesses, from the text, what a template creates.
export function tplTags(yaml: string): string[] {
  const tags: string[] = [];
  if (/^services:/m.test(yaml)) tags.push("containers");
  if (/^x-vms:/m.test(yaml)) tags.push("vm");
  for (const r of ["docker", "podman", "nerdctl"]) if (new RegExp("runtime:\\s*" + r).test(yaml)) tags.push(r);
  return tags;
}

// envText writes the values as a .env (quoted when needed).
export function envText(vals: Record<string, string>, only?: (k: string) => boolean): string {
  return Object.entries(vals)
    .filter(([k, v]) => v !== "" && (!only || only(k)))
    .map(([k, v]) => k + "=" + (/[\s#"']/.test(v) ? JSON.stringify(v) : v))
    .join("\n");
}

export type Target = { name: string; ok: boolean; idle?: boolean; runtime?: string };

// createLab writes the project on the host (and brings it up as a job),
// then opens its page.
export async function createLab(host: string, body: { name: string; dir: string; compose: string; env: string; up: boolean; keychain: boolean; files: Record<string, string> }, mkey?: string) {
  const r = await api<{ id: string; job?: string; ok?: boolean; error?: string; output?: string; path?: string }>("/hosts/" + encodeURIComponent(host) + "/labs", {
    method: "POST",
    body: { ...body, job: body.up },
  });
  if (r.output) gantry.showOutput(host + ": " + body.name, r);
  if (r.job) {
    const key = host + "/" + r.id;
    gantry.markBusy(key, "up");
    gantry.openJob(r.job, t("Subir · {name}", { name: body.name }), async () => {
      gantry.markBusy(key, null);
      await gantry.loadLabs(host);
    });
  } else {
    gantry.toast(r.ok ? (body.up ? t("Projeto {name} no ar", { name: body.name }) : r.path ? t("Projeto {name} criado em {path}", { name: body.name, path: r.path }) : t("Projeto {name} criado", { name: body.name })) : r.error || t("falhou"), r.ok ? "ok" : "err");
  }
  const mach = mkey ?? gantry.machines().find((mm) => mm.hosts.some((x: { name: string }) => x.name === host))?.key;
  if (mach) forgetMachine(mach);
  await gantry.loadLabs(host);
  location.hash = gantry.labHref(host, r.id);
}

// randomSecret makes a password (for ?senhas=gerar, from the welcome page).
export function randomSecret(): string {
  const b = new Uint8Array(15);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b))
    .replace(/[+/=]/g, "")
    .slice(0, 20);
}
