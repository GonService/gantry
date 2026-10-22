// The command palette (Ctrl K / ⌘K): go anywhere and do the usual actions
// (bring a project up, open the logs of a container or of a Deployment,
// switch cluster or namespace) by typing a few letters. The screens can add
// their own commands (registerCommands); the ones used most come first.
import { useEffect, useMemo, useRef, useState } from "react";
import { create } from "zustand";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { hostHref, labHref } from "@/lib/app";
import { useStore } from "@/lib/store";
import { t } from "@/lib/i18n";
import { labOp } from "@/views/lab/LabPage";
import { openMultiLogs, openPodLogs } from "@/views/kube/actions";
import { openDetail } from "@/views/kube/detail/store";
import { setKubeNS, useKube, loadView } from "@/views/kube/store";
import { kubeHref, type Cluster } from "@/views/kube/types";

export type Command = {
  id: string; // stable: the use count is kept by it
  title: string;
  group: string;
  hint?: string;
  words?: string; // more words it answers to (other language, synonyms)
  run: () => void | Promise<void>;
};

type Provider = () => Command[];
const providers = new Map<string, Provider>();

// registerCommands adds the commands of a screen (returns the undo).
export function registerCommands(key: string, p: Provider): () => void {
  providers.set(key, p);
  return () => {
    if (providers.get(key) === p) providers.delete(key);
  };
}

export const usePalette = create<{ open: boolean }>(() => ({ open: false }));
export const openPalette = () => usePalette.setState({ open: true });

const USED = "palette.used";
const fold = (s: string) =>
  s
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "");

// score: every word typed must be in the command; the start of a word
// counts more, the title's start more still, and use lifts it.
export function score(c: Command, q: string[], used: Record<string, number>): number {
  const title = fold(c.title);
  const all = title + " " + fold(c.words ?? "") + " " + fold(c.hint ?? "") + " " + fold(c.group);
  let s = 0;
  for (const w of q) {
    const i = all.indexOf(w);
    if (i < 0) return -1;
    s += i === 0 || /[\s·/:(-]/.test(all[i - 1]) ? 3 : 1;
    if (title.indexOf(w) >= 0) s += 1;
  }
  if (q.length && title.startsWith(q[0])) s += 2;
  return s + Math.min(4, Math.log2((used[c.id] ?? 0) + 1));
}

const WORKLOADS: Record<string, string> = { deployments: "Deployment", statefulsets: "StatefulSet", daemonsets: "DaemonSet", jobs: "Job", replicasets: "ReplicaSet", cronjobs: "CronJob" };

// The commands of what the app knows now.
function baseCommands(): Command[] {
  const S = gantry.state();
  const out: Command[] = [];
  const nav = (id: string, title: string, href: string, words = "") => out.push({ id: "nav:" + id, title, group: t("Ir para"), words, run: () => void (location.hash = href) });
  nav("home", t("Início"), "#/", "home inicio");
  nav("templates", t("Modelos"), "#/modelos", "templates");
  nav("cfg", t("Configurações"), "#/config/geral", "settings config configuracoes geral general");
  nav("cfg-hosts", t("Configurações › Máquinas"), "#/config/maquinas", "settings config configuracoes machines hosts maquinas");
  nav("cfg-clusters", t("Configurações › Clusters"), "#/config/clusters", "settings config configuracoes kubernetes clusters");
  nav("cfg-folders", t("Configurações › Pastas"), "#/config/pastas", "settings config configuracoes folders pastas");
  nav("new-local", t("Criar projeto nesta máquina"), "#/local/novo", "new project novo criar");
  for (const h of S.hosts) {
    if (!h.ok) continue;
    nav("host:" + h.name, t("Máquina {name}", { name: h.name }), hostHref(h.name), "host machine maquina");
    for (const lab of (S.labs[h.name] ?? []) as { id: string; name: string; loose?: boolean; containers: { id: string; name: string; service?: string; state: string }[] }[]) {
      const where = h.name;
      out.push({ id: "lab:" + h.name + "/" + lab.id, title: t("Projeto {name}", { name: lab.name }), hint: where, group: t("Projetos"), words: "project lab abrir open", run: () => void (location.hash = labHref(h.name, lab.id)) });
      if (!lab.loose) {
        const op = (o: string, title: string, words: string, label: string) =>
          out.push({ id: "lab-" + o + ":" + h.name + "/" + lab.id, title, hint: where, group: t("Projetos"), words, run: () => labOp(h.name, lab as never, o, label) });
        op("up", t("Subir {name}", { name: lab.name }), "up start subir iniciar bring", t("Subir"));
        op("stop", t("Parar {name}", { name: lab.name }), "stop parar", t("Parar"));
        op("restart", t("Reiniciar {name}", { name: lab.name }), "restart reiniciar", t("Reiniciar"));
      }
      for (const c of lab.containers ?? []) {
        const label = c.service || c.name;
        out.push({ id: "ctr-logs:" + h.name + "/" + c.name, title: t("Logs de {name}", { name: label }), hint: lab.name + " · " + where, group: "Containers", words: "logs container " + c.name, run: () => gantry.openLogs(h.name, c as never) });
        if (c.state === "running")
          out.push({ id: "ctr-sh:" + h.name + "/" + c.name, title: t("Terminal de {name}", { name: label }), hint: lab.name + " · " + where, group: "Containers", words: "shell terminal sh bash exec container " + c.name, run: () => gantry.openTerminal(h.name, c as never) });
      }
    }
  }
  const route = S.route;
  for (const c of S.clusters ?? []) {
    out.push({ id: "cluster:" + c.id, title: t("Cluster {name}", { name: c.name }), hint: c.context, group: "Kubernetes", words: "kubernetes k8s contexto context trocar switch " + c.context, run: () => void (location.hash = kubeHref(c.id)) });
  }
  const cur = route.kube ? (S.clusters ?? []).find((c) => c.id === route.kube) : undefined;
  if (cur) {
    for (const k of S.kinds ?? []) out.push({ id: "kind:" + k.id, title: k.label, hint: cur.name, group: "Kubernetes", words: k.id, run: () => void (location.hash = kubeHref(cur.id, k.id)) });
    for (const ns of useKube.getState().data[cur.id]?.namespaces ?? [])
      out.push({
        id: "ns:" + cur.id + "/" + ns,
        title: t("Namespace {ns}", { ns }),
        hint: cur.name,
        group: "Kubernetes",
        words: "namespace ns trocar switch",
        run: () => {
          setKubeNS(cur.id, ns);
          void loadView(cur.id, route.kkind ?? "");
        },
      });
  }
  for (const p of providers.values()) out.push(...p());
  return out;
}

type Hit = { kind: string; label: string; namespace?: string; name: string };

// The objects of the cluster in sight that match (the cluster search), as
// commands: open, and logs for workloads and pods.
function objectCommands(c: Cluster, hits: Hit[]): Command[] {
  const out: Command[] = [];
  for (const h of hits) {
    const where = (h.namespace ? h.namespace + " · " : "") + c.name;
    out.push({
      id: "obj:" + c.id + "/" + h.kind + "/" + (h.namespace ?? "") + "/" + h.name,
      title: (WORKLOADS[h.kind] ?? (h.kind === "pods" ? "Pod" : h.label)) + " " + h.name,
      hint: where,
      group: t("Objetos do cluster"),
      words: "abrir open " + h.kind,
      run: () => {
        location.hash = kubeHref(c.id, h.kind);
        openDetail(c, h.kind, h.namespace ?? "", h.name);
      },
    });
    if (WORKLOADS[h.kind] || h.kind === "pods")
      out.push({
        id: "objlogs:" + c.id + "/" + h.kind + "/" + (h.namespace ?? "") + "/" + h.name,
        title: t("Logs de {kind} {name}", { kind: WORKLOADS[h.kind] ?? "Pod", name: h.name }),
        hint: where,
        group: t("Objetos do cluster"),
        words: "logs " + h.kind,
        run: () => (h.kind === "pods" ? openPodLogs(c, h.namespace ?? "", h.name, "", true) : openMultiLogs(c, h.namespace ?? "", h.kind, h.name)),
      });
  }
  return out;
}

// The verbs that do not name the object (logs api: api is the object).
const VERBS = new Set(["logs", "log", "abrir", "open", "ver"]);

export function Palette() {
  const open = usePalette((s) => s.open);
  const route = useStore((s) => s.route);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [hits, setHits] = useState<{ q: string; cmds: Command[] }>({ q: "", cmds: [] });
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        usePalette.setState((s) => ({ open: !s.open }));
      }
    };
    // Capture: the terminals of the dock would keep the keys.
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, []);
  const labs = useStore((s) => s.labs);
  const clusters = useStore((s) => s.clusters);
  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
      setTimeout(() => input.current?.focus(), 0);
      // The projects of every machine, for "subir loja" from anywhere.
      const S = gantry.state();
      for (const h of S.hosts) if (h.ok && !S.labs[h.name]) void gantry.loadLabs(h.name);
      if (!S.clusters) void gantry.loadClusters();
    }
  }, [open]);
  // The objects of the cluster in sight, searched as you type.
  const cluster = route.kube ? (gantry.state().clusters ?? []).find((c) => c.id === route.kube) : undefined;
  useEffect(() => {
    if (!open || !cluster) return;
    const words = fold(q).split(/\s+/).filter((w) => w && !VERBS.has(w));
    if (!words.length || words.join("").length < 2) {
      setHits({ q, cmds: [] });
      return;
    }
    const id = setTimeout(() => {
      api<{ hits: Hit[] }>("/clusters/" + enc(cluster.id) + "/busca?q=" + enc(words.join(" ")))
        .then((r) => setHits({ q, cmds: objectCommands(cluster, r.hits.slice(0, 30)) }))
        .catch(() => setHits({ q, cmds: [] }));
    }, 200);
    return () => clearTimeout(id);
  }, [q, open, cluster?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const used: Record<string, number> = open ? gantry.store.get(USED, {}) : {};
  const results = useMemo(() => {
    if (!open) return [];
    const words = fold(q).split(/\s+/).filter(Boolean);
    const all = [...baseCommands(), ...hits.cmds];
    if (!words.length) {
      const top = all.filter((c) => used[c.id]).sort((a, b) => (used[b.id] ?? 0) - (used[a.id] ?? 0));
      return top.length ? top.slice(0, 12) : all.filter((c) => c.id.startsWith("nav:")).slice(0, 12);
    }
    // "logs api": the object's commands match on the rest of the words.
    const scored: [Command, number][] = [];
    const seen = new Set<string>();
    for (const c of all) {
      if (seen.has(c.id)) continue;
      seen.add(c.id);
      let s = score(c, words, used);
      if (s < 0 && c.id.startsWith("obj")) s = score(c, words.filter((w) => !VERBS.has(w)), used) - 1;
      if (s >= 0) scored.push([c, s + (c.id.startsWith("objlogs") && words.some((w) => w === "logs" || w === "log") ? 3 : 0)]);
    }
    scored.sort((a, b) => b[1] - a[1] || a[0].title.localeCompare(b[0].title));
    return scored.slice(0, 50).map((x) => x[0]);
  }, [open, q, hits, labs, clusters]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => setSel(0), [q]);
  useEffect(() => {
    list.current?.querySelector(".pal-item.sel")?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  if (!open) return null;
  const close = () => usePalette.setState({ open: false });
  const run = (c: Command) => {
    const u = gantry.store.get<Record<string, number>>(USED, {});
    u[c.id] = (u[c.id] ?? 0) + 1;
    gantry.store.set(USED, u);
    close();
    void c.run();
  };
  return (
    <div className="pal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div className="pal" role="dialog" aria-label={t("Paleta de comandos")}>
        <input
          ref={input}
          className="pal-input"
          placeholder={t("Ir para ou fazer… (subir loja, logs api, terminal web, namespace prod)")}
          spellCheck={false}
          value={q}
          aria-controls="pal-list"
          aria-activedescendant={results[sel] ? "pal-" + sel : undefined}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") close();
            else if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((i) => Math.min(results.length - 1, i + 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((i) => Math.max(0, i - 1));
            } else if (e.key === "Enter" && results[sel]) {
              e.preventDefault();
              run(results[sel]);
            }
          }}
        />
        <div className="pal-list" id="pal-list" role="listbox" ref={list}>
          {results.length ? (
            results.map((c, i) => (
              <div key={c.id} id={"pal-" + i} role="option" aria-selected={i === sel} className={"pal-item" + (i === sel ? " sel" : "")} onMouseMove={() => setSel(i)} onClick={() => run(c)}>
                <span className="pal-title">{c.title}</span>
                {c.hint ? <span className="pal-hint">{c.hint}</span> : null}
                <span className="grow" />
                <span className="pal-group">{c.group}</span>
              </div>
            ))
          ) : (
            <p className="muted small pal-empty">{t("Nada com esse nome.")}</p>
          )}
        </div>
        <div className="pal-foot muted small">{t("↑↓ escolhe · Enter faz · Esc fecha")}</div>
      </div>
    </div>
  );
}
