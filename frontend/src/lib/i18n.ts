/// <reference types="vite/client" />
// The interface in Portuguese or in English. The Portuguese text is the
// key: t("Salvar") is "Save" in English (src/i18n/en/*.json), and a text
// missing there stays in Portuguese. Values go in braces:
//   t("Log salvo em {path} ({size})", { path, size })
// The same Portuguese with two meanings gets a context after "|", which
// Portuguese does not show: t("Voltar|snapshot") is "Voltar" or "Revert".
// The language is decided before the first module runs (config.js and
// localStorage), so texts made at load time come right too.

export type Lang = "pt" | "en";

const files = import.meta.glob("../i18n/en/*.json", { eager: true, import: "default" }) as Record<string, Record<string, string>>;
const EN: Record<string, string> = Object.assign({}, ...Object.values(files));

// The choice in Settings ("" follows the system).
export function langSetting(): "" | Lang {
  let v = "";
  try {
    v = localStorage.getItem("gantry.lang") ?? "";
  } catch {
    /* no storage */
  }
  if (v !== "pt" && v !== "en") v = (window as unknown as { GANTRY?: { lang?: string } }).GANTRY?.lang ?? "";
  return v === "pt" || v === "en" ? v : "";
}

function systemLang(): Lang {
  const l = (navigator.languages?.[0] || navigator.language || "pt").toLowerCase();
  return l.startsWith("pt") ? "pt" : "en";
}

export const LANG: Lang = langSetting() || systemLang();
document.documentElement.lang = LANG === "en" ? "en" : "pt-BR";

// For dates and numbers.
export const LOCALE = LANG === "en" ? "en-US" : "pt-BR";

export function t(pt: string, vars?: Record<string, string | number | null | undefined>): string {
  let s = LANG === "en" ? EN[pt] : undefined;
  if (s === undefined) {
    const bar = pt.indexOf("|");
    s = bar > 0 ? pt.slice(0, bar) : pt;
  }
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k] ?? "") : m));
  return s;
}

// Plural: tn(n, "{n} pod", "{n} pods").
export function tn(n: number, one: string, many: string, vars?: Record<string, string | number | null | undefined>): string {
  return t(n === 1 ? one : many, { n, ...vars });
}

// Saves the choice and opens the app again in that language.
export function setLang(v: "" | Lang) {
  try {
    if (v) localStorage.setItem("gantry.lang", v);
    else localStorage.removeItem("gantry.lang");
  } catch {
    /* no storage */
  }
  location.reload();
}
