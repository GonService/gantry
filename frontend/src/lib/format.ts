// Formatting shared by the views.
import { t, LOCALE } from "./i18n";

export function fmtBytes(n: number | null | undefined): string {
  if (n == null) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return (n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)) + " " + u[i];
}

export function b64ToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// Age of a unix time in seconds: 12s, 5m, 3h, 2d.
export function kAge(sec: number | null | undefined): string {
  if (!sec) return "";
  const d = Date.now() / 1000 - sec;
  if (d < 60) return Math.floor(d) + "s";
  if (d < 3600) return Math.floor(d / 60) + "m";
  if (d < 172800) return Math.floor(d / 3600) + "h";
  return Math.floor(d / 86400) + "d";
}

// Memory quantity of Kubernetes in bytes ("512Mi", "2Gi", "1000k").
export function kubeMem(s: string | null | undefined): number {
  const m = /^([0-9.]+)\s*(Ki|Mi|Gi|Ti|k|M|G|T)?$/.exec(s || "");
  if (!m) return 0;
  const mult: Record<string, number> = { Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, k: 1e3, M: 1e6, G: 1e9, T: 1e12 };
  return parseFloat(m[1]) * (m[2] ? mult[m[2]] : 1);
}

// CPU quantity in millicores ("2", "1500m", "250000n").
export function kubeCPU(s: string | null | undefined): number {
  const m = /^([0-9.]+)(m|n|u)?$/.exec(s || "");
  if (!m) return 0;
  const v = parseFloat(m[1]);
  return m[2] === "m" ? v : m[2] === "n" ? v / 1e6 : m[2] === "u" ? v / 1e3 : v * 1000;
}

export function kdTime(iso: string | null | undefined): string {
  if (!iso) return "";
  const ms = Date.parse(iso);
  if (!ms) return iso;
  return new Date(ms).toLocaleString(LOCALE) + " (" + t("há {age}", { age: kAge(ms / 1000) }) + ")";
}

export function kdDur(a: string, b?: string | null): string {
  const t0 = Date.parse(a),
    t1 = b ? Date.parse(b) : Date.now();
  if (!t0 || !t1) return "";
  let s = Math.max(0, Math.round((t1 - t0) / 1000));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m " + (s % 60) + "s";
  s = Math.floor(s / 60);
  return Math.floor(s / 60) + "h " + (s % 60) + "m";
}

// A small YAML printer for showing objects (not for editing).
export function toYAML(v: unknown, ind = ""): string {
  const scalar = (x: unknown): string => {
    if (x === null || x === undefined) return "null";
    if (typeof x === "string")
      return x === "" || /^[\s]|[\s]$|^[-?:,[\]{}#&*!|>'"%@`]|: | #|^(true|false|null|yes|no|on|off|~|[-+]?[0-9.]+(e[0-9]+)?)$/i.test(x) ? JSON.stringify(x) : x;
    return String(x);
  };
  const isObj = (x: unknown): x is Record<string, unknown> => !!x && typeof x === "object" && !Array.isArray(x);
  if (Array.isArray(v)) {
    if (!v.length) return "[]";
    return v
      .map((x) => {
        if (x && typeof x === "object" && (Array.isArray(x) ? x.length : Object.keys(x).length)) {
          const body = toYAML(x, ind + "  ");
          return ind + "- " + body.slice(ind.length + 2);
        }
        return ind + "- " + (x && typeof x === "object" ? (Array.isArray(x) ? "[]" : "{}") : scalar(x));
      })
      .join("\n");
  }
  if (isObj(v)) {
    const keys = Object.keys(v);
    if (!keys.length) return "{}";
    return keys
      .map((k) => {
        const x = v[k];
        if (x && typeof x === "object" && (Array.isArray(x) ? x.length : Object.keys(x).length)) return ind + k + ":\n" + toYAML(x, ind + "  ");
        if (typeof x === "string" && x.includes("\n"))
          return ind + k + ": |\n" + x.replace(/\n$/, "").split("\n").map((l) => ind + "  " + l).join("\n");
        return ind + k + ": " + (x && typeof x === "object" ? (Array.isArray(x) ? "[]" : "{}") : scalar(x));
      })
      .join("\n");
  }
  return ind + scalar(v);
}

// The clipboard: the desktop app's (the Wails runtime: the WebKitGTK of
// Linux does not give navigator.clipboard to the app's page), else the
// browser's, else the old execCommand of a hidden textarea.
type ClipRT = { ClipboardSetText?: (s: string) => Promise<boolean>; ClipboardGetText?: () => Promise<string> };
const clipRT = () => (window as unknown as { runtime?: ClipRT }).runtime;

export async function copyText(text: string): Promise<boolean> {
  try {
    const rt = clipRT();
    if (rt?.ClipboardSetText && (await rt.ClipboardSetText(text))) return true;
  } catch {
    /* next */
  }
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    /* next */
  }
  const keep = document.activeElement as HTMLElement | null;
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  ta.remove();
  keep?.focus();
  return ok;
}

// readText: the text in the clipboard, or null when it cannot be read.
export async function readText(): Promise<string | null> {
  try {
    const rt = clipRT();
    if (rt?.ClipboardGetText) return await rt.ClipboardGetText();
  } catch {
    /* next */
  }
  try {
    return await navigator.clipboard.readText();
  } catch {
    return null;
  }
}
