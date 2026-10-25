// The dock at the bottom: logs, terminals, background jobs and command
// outputs, as tabs. Terminals use xterm.js (vendor/xterm.js, a global).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { create } from "zustand";
import { api, apiURL, enc, CFG } from "@/lib/api";
import { b64ToBytes, copyText, fmtBytes, readText } from "@/lib/format";
import { openMenu } from "@/components/contextmenu";
import { hostByName, loadHosts, loadLocal, useStore } from "@/lib/store";
import { localStore, toast } from "./toasts";
import { pickPath } from "./pick";
import { loadObs, obsOK } from "@/views/host/obs";
import { XIcon } from "@/components/icons";
import { t, LOCALE } from "@/lib/i18n";

/* eslint-disable @typescript-eslint/no-explicit-any */
declare const Terminal: any;
declare const FitAddon: any;

type Tab = { id: string; key: string; kind: string; title: string; node: (active: boolean) => React.ReactNode };
type DockState = { tabs: Tab[]; active: string | null; collapsed: boolean };

export const useDock = create<DockState>(() => ({ tabs: [], active: null, collapsed: false }));
let seq = 0;

function dockAdd(t: Omit<Tab, "id">): string {
  const s = useDock.getState();
  const existing = s.tabs.find((x) => x.key === t.key);
  if (existing) {
    useDock.setState({ active: existing.id, collapsed: false });
    return existing.id;
  }
  const id = "t" + ++seq;
  useDock.setState({ tabs: [...s.tabs, { ...t, id }], active: id, collapsed: false });
  return id;
}
export const dockHas = (key: string) => useDock.getState().tabs.some((t) => t.key === key);

// closeHostTabs closes the logs and terminals of hosts that were
// disconnected (they would only show the connection dropping).
export function closeHostTabs(hosts: string[]) {
  for (const t of [...useDock.getState().tabs]) if (hosts.some((h) => t.key.startsWith("logs:" + h + ":") || t.key.startsWith("term:" + h + ":"))) dockClose(t.id);
}

// A tab may ask before closing (an editor with unsaved changes).
const closeGuards = new Map<string, () => boolean | Promise<boolean>>();
export function setCloseGuard(key: string, fn: (() => boolean | Promise<boolean>) | null) {
  if (fn) closeGuards.set(key, fn);
  else closeGuards.delete(key);
}

// openPane: a tab of the panel with any content (the YAML editor of a
// cluster); a key already open is brought to the front.
export function openPane(key: string, kind: string, title: string, node: (active: boolean) => React.ReactNode): string {
  return dockAdd({ key, kind, title, node });
}

// closeTabsWhere closes the tabs whose key matches (those of a cluster
// that was disconnected); an editor with changes still asks.
export function closeTabsWhere(match: (key: string) => boolean) {
  for (const tb of [...useDock.getState().tabs]) if (match(tb.key)) void dockClose(tb.id);
}

export function closePane(key: string, force = false) {
  const tb = useDock.getState().tabs.find((x) => x.key === key);
  if (tb) void dockClose(tb.id, force);
}

async function dockClose(id: string, force = false) {
  const tb = useDock.getState().tabs.find((t) => t.id === id);
  const guard = tb && closeGuards.get(tb.key);
  if (!force && guard && !(await guard())) return;
  if (tb) closeGuards.delete(tb.key);
  const s = useDock.getState();
  const i = s.tabs.findIndex((t) => t.id === id);
  if (i < 0) return;
  const tabs = s.tabs.filter((t) => t.id !== id);
  let active = s.active;
  if (active === id) active = (tabs[i] || tabs[i - 1])?.id ?? null;
  useDock.setState({ tabs, active });
}

const termTheme = {
  background: "#0B0D0F",
  foreground: "#C3CAD2",
  cursor: "#4FD1C1",
  cursorAccent: "#0B0D0F",
  selectionBackground: "#2F5D59",
  black: "#1D2127",
  brightBlack: "#6B737C",
  red: "#FF8A80",
  green: "#3DDC97",
  yellow: "#F2C77A",
  blue: "#7AB8FF",
  magenta: "#D7A6FF",
  cyan: "#4FD1C1",
  white: "#E6E8EA",
};

// useXterm makes a terminal in the box, fitted to it while visible.
function useXterm(opts: Record<string, unknown>, active: boolean) {
  const box = useRef<HTMLDivElement>(null);
  const ref = useRef<{ term: any; fit: any } | null>(null);
  if (!ref.current) {
    const term = new Terminal({
      fontFamily: "'JetBrains Mono', ui-monospace, 'SF Mono', Menlo, Consolas, monospace",
      fontSize: 13,
      lineHeight: 1.2,
      theme: termTheme,
      scrollback: 5000,
      allowProposedApi: false,
      ...opts,
    });
    const fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    ref.current = { term, fit };
  }
  const activeRef = useRef(active);
  activeRef.current = active;
  useLayoutEffect(() => {
    const { term, fit } = ref.current!;
    term.open(box.current);
    const doFit = () => {
      try {
        fit.fit();
      } catch {
        /* hidden */
      }
    };
    doFit();
    // Copy and paste: Ctrl+Shift+C / Ctrl+Shift+V (Cmd+C / Cmd+V on the Mac),
    // Ctrl+C with text selected copies it (without a selection it is still
    // the terminal's Ctrl+C), and the right-click menu.
    const canType = !term.options.disableStdin;
    const copySel = async () => {
      const sel = term.getSelection();
      if (!sel) return;
      const ok = await copyText(sel);
      toast(ok ? t("Copiado") : t("Não consegui copiar para a área de transferência"), ok ? "ok" : "err");
      term.focus();
    };
    const paste = async () => {
      const txt = await readText();
      if (txt == null) toast(t("Não consegui ler a área de transferência"), "err");
      else if (txt) term.paste(txt);
      term.focus();
    };
    term.attachCustomKeyEventHandler((e: KeyboardEvent) => {
      if (e.type !== "keydown" || !(e.ctrlKey || e.metaKey) || e.altKey) return true;
      const k = e.key.toLowerCase();
      if (k === "c" && (e.shiftKey || e.metaKey || term.hasSelection())) {
        e.preventDefault();
        void copySel();
        return false;
      }
      if (k === "v" && (e.shiftKey || e.metaKey) && canType) {
        e.preventDefault();
        void paste();
        return false;
      }
      return true;
    });
    const menu = (e: MouseEvent) =>
      openMenu(e, [
        { label: t("Copiar"), disabled: !term.hasSelection(), hint: navigator.platform.startsWith("Mac") ? "⌘C" : "Ctrl+Shift+C", onClick: copySel },
        canType && { label: t("Colar"), hint: navigator.platform.startsWith("Mac") ? "⌘V" : "Ctrl+Shift+V", onClick: paste },
        "-",
        {
          label: t("Selecionar tudo"),
          onClick: () => {
            term.selectAll();
            term.focus();
          },
        },
        {
          label: t("Limpar a tela"),
          onClick: () => {
            term.clear();
            term.focus();
          },
        },
      ]);
    box.current!.addEventListener("contextmenu", menu);
    // The cell size changes when the monospace font finishes loading: fit again.
    document.fonts?.ready.then(() => activeRef.current && doFit()).catch(() => {});
    const ro = new ResizeObserver(() => {
      if (activeRef.current) doFit();
    });
    ro.observe(box.current!);
    const el = box.current!;
    return () => {
      el.removeEventListener("contextmenu", menu);
      ro.disconnect();
      term.dispose();
    };
  }, []);
  useEffect(() => {
    if (active)
      requestAnimationFrame(() => {
        try {
          ref.current!.fit.fit();
        } catch {
          /* nothing */
        }
      });
  }, [active]);
  return { box, term: ref.current.term, fit: ref.current.fit };
}

/* logs */

export type LogTarget = {
  id: string;
  name: string;
  service?: string;
  runtime?: string;
  logBase?: string;
  logQuery?: string[];
  logPrevious?: boolean;
  // A pod's log: the logs Gantry saved of the workload's pods that are gone.
  pod?: { cluster: string; ns: string; pod: string; container: string };
  // Extra buttons of the pane, and named events of the stream (a capture
  // tells its file).
  actions?: { label: string; title?: string; run: () => void }[];
  onEvent?: Record<string, (data: string) => void>;
  [k: string]: any;
};

type SavedPod = { pod: string; ns: string; workload: string; node?: string; saved: number; containers: { name: string; previous?: boolean; bytes: number }[] };

const agoShort = (ms: number) => {
  const m = Math.round((Date.now() - ms) / 60000);
  if (m < 1) return t("agora");
  if (m < 60) return t("há {n} min", { n: m });
  if (m < 48 * 60) return t("há {n} h", { n: Math.round(m / 60) });
  return t("há {n} dias", { n: Math.round(m / 1440) });
};

const rtq = (host: string, c: { runtime?: string }) => {
  const hst = hostByName(host);
  return hst && c.runtime && c.runtime !== hst.runtime ? "?rt=" + enc(c.runtime) : "";
};

function LogsPane({ host, c, url, active }: { host: string; c: LogTarget; url?: string; active: boolean }) {
  const { box, term } = useXterm({ disableStdin: true, convertEol: true, cursorBlink: false, cursorStyle: "underline" }, active);
  const [grep, setGrep] = useState("");
  const [since, setSince] = useState("");
  const [stamps, setStamps] = useState(false);
  const [prev, setPrev] = useState(c.logPrevious ? "1" : "");
  const [saved, setSaved] = useState<SavedPod[]>([]);
  const loadSaved = () => {
    const p = c.pod;
    if (!p) return;
    api<{ pods: SavedPod[] }>("/clusters/" + enc(p.cluster) + "/podlogs?ns=" + enc(p.ns) + "&pod=" + enc(p.pod))
      .then((r) => setSaved(r.pods || []))
      .catch(() => {});
  };
  useEffect(loadSaved, []); // eslint-disable-line react-hooks/exhaustive-deps
  // "s:<pod>|<0|1>": a saved log of a pod that is gone (1: its run before).
  const savedPick = prev.startsWith("s:") ? { pod: prev.slice(2).split("|")[0], previous: prev.endsWith("|1") } : null;
  const [source, setSource] = useState("live");
  const [loki, setLoki] = useState(false);
  const [run, setRun] = useState(0);
  // Logs of many pods: which ones (all, or one).
  const [podList, setPodList] = useState<string[]>([]);
  const [onePod, setOnePod] = useState("");
  useEffect(() => {
    if (!c.multi || !c.logBase) return;
    const load = () =>
      api<{ pods: { name: string }[] }>(c.logBase + "/pods")
        .then((r) => setPodList(r.pods.map((p) => p.name)))
        .catch(() => {});
    load();
    const id = setInterval(load, 10000);
    return () => clearInterval(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const grepRef = useRef(grep);
  grepRef.current = grep;
  const params = () => {
    const q: string[] = [];
    if (c.logPrevious != null && prev === "1") q.push("previous=1");
    if (grepRef.current.trim()) q.push("grep=" + enc(grepRef.current.trim()));
    if (since === "all") q.push("tail=all");
    else if (since) q.push("since=" + since);
    if (stamps) q.push("timestamps=1");
    if (c.multi && onePod) q.push("pods=" + enc(onePod));
    return q;
  };
  const ctrURL = (suffix: string) => {
    // logBase is a path without query; its own parameters come in logQuery.
    const base = c.logBase ? c.logBase + suffix : "/hosts/" + enc(host) + "/containers/" + c.id + suffix + rtq(host, c);
    const q = (c.logQuery || []).concat(params());
    return q.length ? base + (base.includes("?") ? "&" : "?") + q.join("&") : base;
  };
  useEffect(() => {
    if (url || c.logBase) return;
    // Containers may have older logs in Loki.
    loadObs(host).then(() => setLoki(obsOK(host, "loki")));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    term.reset();
    if (source !== "live") {
      let live = true;
      term.write("\x1b[2m— " + t("buscando no Loki…") + " —\x1b[0m\r\n");
      const lab = useStore.getState().route.lab || "";
      api<{ lines: { t: number; line: string }[]; query: string }>(
        "/hosts/" + enc(host) + "/obs/loki?range=" + source + "&container=" + enc(c.name) + "&service=" + enc(c.service || "") + "&lab=" + enc(lab),
      )
        .then((r) => {
          if (!live) return;
          term.reset();
          for (const l of r.lines) term.write("\x1b[2m" + new Date(l.t / 1e6).toLocaleString(LOCALE) + "\x1b[0m " + l.line.replace(/\r?\n$/, "") + "\r\n");
          term.write("\r\n\x1b[2m— " + t("{n} linhas do Loki", { n: r.lines.length }) + " · " + r.query + " —\x1b[0m\r\n");
        })
        .catch((e) => live && term.write("\x1b[31m" + (e as Error).message + "\x1b[0m\r\n"));
      return () => {
        live = false;
      };
    }
    let src = url || ctrURL("/logs");
    if (savedPick && c.pod) {
      const q = ["container=" + enc(c.pod.container)];
      if (savedPick.previous) q.push("previous=1");
      if (grepRef.current.trim()) q.push("grep=" + enc(grepRef.current.trim()));
      if (stamps) q.push("timestamps=1");
      src = "/clusters/" + enc(c.pod.cluster) + "/podlogs/" + enc(c.pod.ns) + "/" + enc(savedPick.pod) + "/log?" + q.join("&");
    }
    const es = new EventSource(apiURL(src, true));
    es.onmessage = (e) => term.write(b64ToBytes(e.data));
    for (const [ev, fn] of Object.entries(c.onEvent ?? {})) es.addEventListener(ev, (e) => fn((e as MessageEvent).data));
    es.addEventListener("end", (e) => {
      let msg = t("fim do log");
      try {
        msg = JSON.parse((e as MessageEvent).data).message;
      } catch {
        /* nothing */
      }
      term.write("\r\n\x1b[2m— " + msg + " —\x1b[0m\r\n");
      es.close();
      // The pod went (or never restarted): what was saved may be new.
      if (/Pods anteriores|Pods that have left/.test(msg)) loadSaved();
    });
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) term.write("\r\n\x1b[2m— " + t("conexão encerrada") + " —\x1b[0m\r\n");
    };
    return () => es.close();
  }, [since, stamps, prev, source, run, onePod]); // eslint-disable-line react-hooks/exhaustive-deps
  const live = source === "live";
  return (
    <>
      <div className="pane-tools">
        {loki ? (
          <select className="select small" aria-label={t("Origem dos logs")} value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="live">{t("Ao vivo")}</option>
            <option value="3600">Loki · 1 h</option>
            <option value="21600">Loki · 6 h</option>
            <option value="86400">Loki · 24 h</option>
          </select>
        ) : null}
        {!url && live ? (
          <>
            {c.logPrevious != null ? (
              <select
                className="select small"
                aria-label={t("Execução")}
                title={t("O log da execução atual, da anterior (a que terminou) ou de um pod que já saiu do cluster (guardado pelo Gantry)")}
                value={prev}
                onFocus={loadSaved}
                onChange={(e) => setPrev(e.target.value)}
              >
                <option value="">{t("Execução atual")}</option>
                <option value="1">{t("Execução anterior")}</option>
                {c.pod ? (
                  <optgroup label={t("Pods que já saíram (guardados pelo Gantry)")}>
                    {saved.length ? null : <option disabled>{t("nenhum guardado ainda")}</option>}
                    {saved.flatMap((sp) =>
                      sp.containers
                        .filter((x) => !c.pod!.container || x.name === c.pod!.container)
                        .map((x) => (
                          <option key={sp.pod + (x.previous ? "|1" : "|0")} value={"s:" + sp.pod + (x.previous ? "|1" : "|0")}>
                            {sp.pod + (x.previous ? " · " + t("execução anterior") : "") + " · " + agoShort(sp.saved)}
                          </option>
                        )),
                    )}
                  </optgroup>
                ) : null}
              </select>
            ) : null}
            {c.multi ? (
              <select className="select small" aria-label={t("Pods")} value={onePod} onChange={(e) => setOnePod(e.target.value)}>
                <option value="">{t("Todos os pods ({n})", { n: podList.length })}</option>
                {podList.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            ) : null}
            <input
              className="input small"
              placeholder={t("Filtrar linhas…")}
              spellCheck={false}
              style={{ width: 170 }}
              value={grep}
              onChange={(e) => setGrep(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") setRun((n) => n + 1);
              }}
            />
            {savedPick ? null : (
              <select className="select small" aria-label={t("Período")} value={since} onChange={(e) => setSince(e.target.value)}>
                <option value="">{t("Últimas 300")}</option>
                <option value="900">15 min</option>
                <option value="3600">1 h</option>
                <option value="21600">6 h</option>
                <option value="86400">24 h</option>
                <option value="all">{t("Tudo")}</option>
              </select>
            )}
            <label className="check small" title={t("Mostrar a hora de cada linha")}>
              <input type="checkbox" checked={stamps} onChange={(e) => setStamps(e.target.checked)} />
              {t("Hora")}
            </label>
            {savedPick || c.multi ? null : (
              <button
                className="btn small"
                title={t("Salvar o log (com os filtros) num arquivo")}
                onClick={async () => {
                  const dest = await pickPath({ kind: "salvar", title: t("Salvar o log"), name: (c.service || c.name || "container") + ".log" });
                  if (!dest) return;
                  try {
                    const r = await api<{ path: string; bytes: number }>(ctrURL("/logs/salvar"), { method: "POST", body: { dest } });
                    toast(t("Log salvo em {path} ({size})", { path: r.path, size: fmtBytes(r.bytes) }), "ok");
                  } catch (e) {
                    toast((e as Error).message, "err");
                  }
                }}
              >
                {t("Salvar…")}
              </button>
            )}
          </>
        ) : null}
        {(c.actions ?? []).map((a) => (
          <button key={a.label} className="btn small" title={a.title} onClick={a.run}>
            {a.label}
          </button>
        ))}
        <button className="btn small" onClick={() => term.clear()}>
          {t("Limpar")}
        </button>
      </div>
      <div className="term" ref={box} />
    </>
  );
}

export function openLogs(host: string, c: LogTarget, url?: string) {
  dockAdd({ key: "logs:" + host + ":" + c.id, kind: "logs", title: c.service || c.name, node: (active) => <LogsPane host={host} c={c} url={url} active={active} /> });
}

/* terminal */

const rawFetch = (path: string, opts: RequestInit = {}) => fetch(apiURL(path), { ...opts, headers: { ...((opts.headers as Record<string, string>) || {}), ...(CFG.token ? { Authorization: "Bearer " + CFG.token } : {}) } });

function TermPane({ host, c, active }: { host: string; c: LogTarget; active: boolean }) {
  const { box, term } = useXterm({ cursorBlink: true }, active);
  useEffect(() => {
    let es: EventSource | null = null;
    let sid: string | null = null;
    let closed = false;
    let queue = "";
    let sending: Promise<unknown> = Promise.resolve();
    let rtimer = 0;
    const flush = () => {
      if (!queue || !sid) return;
      const data = queue;
      queue = "";
      sending = sending.then(() => rawFetch("/term/" + sid + "/input", { method: "POST", body: data })).catch(() => {});
    };
    const sendResize = () => {
      clearTimeout(rtimer);
      rtimer = window.setTimeout(() => {
        if (!sid || closed) return;
        rawFetch("/term/" + sid + "/resize", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ cols: term.cols, rows: term.rows }) }).catch(() => {});
      }, 120);
    };
    term.write("\x1b[2m" + t("Conectando em {name}…", { name: c.service || c.name }) + "\x1b[0m\r\n");
    (async () => {
      try {
        const r = await api<{ id: string }>(c.termURL || "/hosts/" + enc(host) + "/containers/" + c.id + "/term" + rtq(host, c), { method: "POST" });
        sid = r.id;
      } catch (e) {
        term.write("\x1b[31m" + (e as Error).message + "\x1b[0m\r\n");
        return;
      }
      if (closed) {
        rawFetch("/term/" + sid, { method: "DELETE" }).catch(() => {});
        return;
      }
      es = new EventSource(apiURL("/term/" + sid + "/stream", true));
      es.onopen = () => sendResize();
      es.onmessage = (e) => term.write(b64ToBytes(e.data));
      es.addEventListener("exit", () => {
        term.write("\r\n\x1b[2m— " + t("sessão encerrada") + " —\x1b[0m\r\n");
        es?.close();
        closed = true;
      });
      es.onerror = () => {
        if (!closed) {
          es?.close();
          closed = true;
          term.write("\r\n\x1b[2m— " + t("conexão perdida") + " —\x1b[0m\r\n");
        }
      };
    })();
    const d1 = term.onData((d: string) => {
      if (closed) return;
      queue += d;
      setTimeout(flush, 8);
    });
    const d2 = term.onResize(sendResize);
    return () => {
      closed = true;
      es?.close();
      if (sid) rawFetch("/term/" + sid, { method: "DELETE" }).catch(() => {});
      d1.dispose();
      d2.dispose();
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (active) term.focus();
  }, [active]); // eslint-disable-line react-hooks/exhaustive-deps
  return <div className="term" ref={box} />;
}

export function openTerminal(host: string, c: LogTarget) {
  dockAdd({ key: "term:" + host + ":" + c.id + ":" + Date.now(), kind: "shell", title: c.service || c.name, node: (active) => <TermPane host={host} c={c} active={active} /> });
}

/* jobs */

type JobEnd = { ok: boolean; error?: string };
const jobsOpen = new Set<string>();

function JobPane({ id, title, onDone, active }: { id: string; title: string; onDone?: (r: JobEnd) => void | Promise<void>; active: boolean }) {
  const { box, term, fit } = useXterm({ disableStdin: true, convertEol: false, cursorBlink: false }, active);
  const [prog, setProg] = useState("");
  const [done, setDone] = useState(false);
  const [canceling, setCanceling] = useState(false);
  useEffect(() => {
    jobsOpen.add(id);
    let ended = false;
    const es = new EventSource(apiURL("/jobs/" + id + "/stream", true));
    es.onmessage = (e) => term.write(b64ToBytes(e.data));
    es.addEventListener("progress", (e) => {
      setProg((e as MessageEvent).data || "");
      try {
        fit.fit();
      } catch {
        /* nothing */
      }
    });
    es.addEventListener("end", async (e) => {
      ended = true;
      es.close();
      jobsOpen.delete(id);
      setDone(true);
      setProg("");
      let r: JobEnd = { ok: false };
      try {
        r = JSON.parse((e as MessageEvent).data);
      } catch {
        /* nothing */
      }
      term.write(r.ok ? "\r\n\x1b[32m✓ " + t("concluído") + "\x1b[0m\r\n" : "\r\n\x1b[31m✗ " + (r.error === "cancelado" ? t("cancelado") : t("falhou: {error}", { error: r.error || "" })) + "\x1b[0m\r\n");
      toast(r.ok ? t("{title}: concluído", { title }) : r.error === "cancelado" ? t("{title}: cancelado", { title }) : t("{title}: falhou", { title }), r.ok ? "ok" : "err");
      useStore.setState({ localBusy: false });
      if (onDone) {
        await onDone(r);
        return;
      }
      await loadHosts(true);
      await loadLocal();
    });
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED && useStore.getState().localBusy) {
        useStore.setState({ localBusy: false });
        loadLocal();
      }
    };
    return () => {
      es.close();
      // The tab was closed with the operation still running: keep following
      // it, so the page leaves the busy state when it ends.
      if (!ended) {
        jobsOpen.delete(id);
        followJob(id, title, onDone);
      }
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const m = prog.match(/\((\d+)%\)/);
  return (
    <>
      <div className="job-prog" hidden={!prog}>
        <span className="job-prog-bar">
          <span className="job-prog-fill" style={{ width: (m ? m[1] : 0) + "%" }} />
        </span>
        <span className="job-prog-text">{prog}</span>
      </div>
      <div className="term" ref={box} />
      {done ? null : (
        <div className="pane-tools">
          <button
            className="btn small"
            disabled={canceling}
            onClick={async () => {
              setCanceling(true);
              try {
                await api("/jobs/" + id, { method: "DELETE" });
              } catch (e) {
                toast((e as Error).message, "err");
                setCanceling(false);
              }
            }}
          >
            {t("Cancelar")}
          </button>
        </div>
      )}
    </>
  );
}

// followJob waits, without a tab, for an operation to end on the server.
function followJob(id: string, title: string, onDone?: (r: JobEnd) => void | Promise<void>) {
  const tick = async () => {
    let j: { id: string; done: boolean; error?: string } | undefined;
    try {
      const jobs = await api<{ id: string; done: boolean; error?: string }[]>("/jobs");
      j = jobs.find((x) => x.id === id);
    } catch {
      setTimeout(tick, 3000);
      return;
    }
    if (j && !j.done) {
      setTimeout(tick, 2000);
      return;
    }
    const r: JobEnd = { ok: !j || !j.error, error: j?.error };
    toast(r.ok ? t("{title}: concluído", { title }) : r.error === "cancelado" ? t("{title}: cancelado", { title }) : t("{title}: falhou", { title }), r.ok ? "ok" : "err");
    useStore.setState({ localBusy: false });
    if (onDone) await onDone(r);
    else {
      await loadHosts(true);
      await loadLocal();
    }
  };
  setTimeout(tick, 1000);
}

// openJob shows a background operation in the dock: live output, a
// progress line for downloads and a Cancelar button. onDone gets {ok, error}.
export function openJob(id: string, title: string, onDone?: (r: JobEnd) => void | Promise<void>) {
  if (jobsOpen.has(id)) return;
  dockAdd({ key: "job:" + id, kind: t("tarefa"), title, node: (active) => <JobPane id={id} title={title} onDone={onDone} active={active} /> });
}

// reattachJobs reopens operations still running on the server (after a
// reload of the page, or from another window).
export async function reattachJobs(after: () => void) {
  let jobs: { id: string; key: string; label: string; done: boolean }[] = [];
  try {
    jobs = await api("/jobs");
  } catch {
    return;
  }
  for (const j of jobs) {
    if (j.done || jobsOpen.has(j.id)) continue;
    const m = /^lab:[^/]+\/(.+)$/.exec(j.key);
    openJob(j.id, j.label, after);
    if (m) {
      const busy = { ...useStore.getState().busy };
      for (const x of useStore.getState().hosts) busy[x.name + "/" + m[1]] = "job";
      useStore.setState({ busy });
    }
  }
}

export function showOutput(title: string, res: { command?: string; output?: string; error?: string; ok?: boolean }) {
  // No color codes (podman compose paints the warning of the external provider).
  // eslint-disable-next-line no-control-regex
  const text = ((res.command ? "$ " + res.command + "\n\n" : "") + (res.output || "") + (res.error && !res.output ? res.error : "") + (res.ok === false ? "\n[" + t("falhou") + "]" : "")).replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");
  dockAdd({ key: "out:" + Date.now(), kind: t("saída"), title, node: () => <pre className="out">{text.trim() || t("(sem saída)")}</pre> });
}

/* the dock */

export function Dock() {
  const { tabs, active, collapsed } = useDock();
  useEffect(() => {
    document.documentElement.style.setProperty("--dock-h", localStore.get("dockH", 300) + "px");
  }, []);
  useEffect(() => {
    document.body.classList.toggle("has-dock", tabs.length > 0);
    document.body.classList.toggle("dock-collapsed", collapsed);
  }, [tabs.length, collapsed]);
  return (
    <section id="dock" className={"dock" + (collapsed ? " collapsed" : "")} hidden={!tabs.length} aria-label={t("Terminais e logs")}>
      <div
        className="dock-resize"
        title={t("Arraste para redimensionar")}
        onPointerDown={(e) => {
          e.preventDefault();
          const grip = e.currentTarget;
          grip.setPointerCapture(e.pointerId);
          const move = (ev: PointerEvent) => {
            const hgt = Math.max(140, Math.min(window.innerHeight - 160, window.innerHeight - ev.clientY));
            document.documentElement.style.setProperty("--dock-h", hgt + "px");
          };
          const up = () => {
            grip.removeEventListener("pointermove", move);
            grip.removeEventListener("pointerup", up);
            localStore.set("dockH", parseInt(getComputedStyle(document.documentElement).getPropertyValue("--dock-h"), 10) || 300);
          };
          grip.addEventListener("pointermove", move);
          grip.addEventListener("pointerup", up);
        }}
      />
      <div className="dock-bar">
        <div className="dock-tabs" role="tablist">
          {tabs.map((tb) => (
            <div
              key={tb.id}
              className="dock-tab"
              role="tab"
              aria-selected={tb.id === active}
              tabIndex={0}
              onClick={() => useDock.setState({ active: tb.id })}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  useDock.setState({ active: tb.id });
                }
              }}
            >
              <span className="kind">{tb.kind}</span>
              {tb.title}
              <button
                className="x"
                aria-label={t("Fechar {title}", { title: tb.title })}
                onClick={(e) => {
                  e.stopPropagation();
                  dockClose(tb.id);
                }}
              >
                <XIcon size={12} />
              </button>
            </div>
          ))}
        </div>
        <button className="icon-btn" id="dock-toggle" type="button" title={t("Recolher")} aria-label={t("Recolher painel")} onClick={() => useDock.setState({ collapsed: !collapsed })}>
          ⌄
        </button>
      </div>
      <div className="dock-body">
        {tabs.map((tb) => (
          <div key={tb.id} className={"dock-pane" + (tb.id === active ? " active" : "")}>
            {tb.node(tb.id === active && !collapsed)}
          </div>
        ))}
      </div>
    </section>
  );
}
