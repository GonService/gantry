// The local API of Gantry. In the window (Wails) the token comes in
// window.GANTRY; in the browser it came with the link of the terminal and
// lives in this origin's localStorage (never in a cookie).

import { LANG } from "./i18n";

type GantryCfg = { api: string; token: string; version: string; mode: string; lang?: string; streams?: string[] };

declare global {
  interface Window {
    GANTRY?: GantryCfg;
  }
}

export const CFG: GantryCfg = window.GANTRY ?? { api: "", token: "", version: "", mode: "web" };
if (!CFG.token) {
  try {
    CFG.token = localStorage.getItem("gantry.token") ?? "";
  } catch {
    /* no storage */
  }
}

export class APIError extends Error {
  status: number;
  data: unknown;
  constructor(message: string, status: number, data: unknown) {
    super(message);
    this.status = status;
    this.data = data;
  }
}

// Streams take turns among the server's extra addresses (desktop): the web
// view allows 6 connections per address, and open streams would leave the
// ordinary calls waiting.
let nextStream = 0;

export function apiURL(path: string, stream = false): string {
  const s = CFG.streams;
  const base = stream && s && s.length ? s[nextStream++ % s.length] : CFG.api;
  const u = base + "/api" + path;
  if (stream) return u + (u.includes("?") ? "&" : "?") + (CFG.token ? "t=" + encodeURIComponent(CFG.token) + "&" : "") + "lang=" + LANG;
  return u;
}

function headers(extra: Record<string, string> = {}): Record<string, string> {
  if (CFG.token) extra.Authorization = "Bearer " + CFG.token;
  extra["X-Gantry-Lang"] = LANG;
  return extra;
}

type Opts = { method?: string; body?: unknown; signal?: AbortSignal };

export async function api<T = unknown>(path: string, opts: Opts = {}): Promise<T> {
  const h = headers();
  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    if (typeof opts.body === "string") body = opts.body;
    else {
      body = JSON.stringify(opts.body);
      h["Content-Type"] = "application/json";
    }
  }
  const r = await fetch(apiURL(path), { method: opts.method ?? (body ? "POST" : "GET"), headers: h, body, signal: opts.signal });
  const text = await r.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (r.status === 401 && CFG.mode === "web") window.dispatchEvent(new Event("gantry:login-needed"));
  if (!r.ok) {
    const err = data && typeof data === "object" && "error" in data ? String((data as { error: unknown }).error) : "";
    const msg = err || text || r.statusText;
    throw new APIError(msg, r.status, data);
  }
  return data as T;
}

export const enc = encodeURIComponent;
