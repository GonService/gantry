import { Fragment, useRef } from "react";
import { openMenu } from "@/components/contextmenu";
import { api, enc } from "@/lib/api";
import { closeTabsWhere } from "@/shell/dock";
import { gantry } from "@/lib/gantry";
import { t, tn } from "@/lib/i18n";
import { confirm, formDialog, pickOption } from "@/components/dialog";
import { loadView, useKube } from "./store";
import { drainNode, forwardDialog, secretDialog, yamlDialog } from "./dialogs";
import { ALL_PERMS, type Cluster, type Perms, type Row } from "./types";
import { closeDetail, openDetail, useDetail } from "./detail/store";

export function openPodLogs(c: Cluster, ns: string, pod: string, ct: string, many: boolean, previous = false) {
  gantry.openLogs("k:" + c.id, {
    id: ns + "/" + pod + "/" + ct + (previous ? "/anterior" : ""),
    name: pod + (ct && many ? " · " + ct : "") + (previous ? " " + t("(anterior)") : ""),
    logPrevious: !!previous,
    logBase: "/clusters/" + enc(c.id) + "/pods/" + enc(ns) + "/" + enc(pod),
    logQuery: ct ? ["container=" + enc(ct)] : [],
    pod: { cluster: c.id, ns, pod, container: ct },
  });
}

// debugDialog: an ephemeral container in a running pod (or one already
// there), opened in the terminal.
async function debugDialog(c: Cluster, ns: string, pod: string, containers: string[]) {
  let info: { containers: { name: string; image: string; running: boolean; target?: string }[]; images: string[] };
  try {
    info = await api("/clusters/" + enc(c.id) + "/debug/" + enc(ns) + "/" + enc(pod));
  } catch (e) {
    gantry.toast((e as Error).message, "err");
    return;
  }
  const running = info.containers.filter((x) => x.running);
  formDialog({
    title: t("Debug em {pod}", { pod }),
    intro: t("Entra um container novo no pod, com as ferramentas da imagem, vendo os processos do container escolhido. O pod não reinicia. Containers de debug não podem ser removidos: ficam no pod (parados) até ele sair."),
    submit: t("Abrir"),
    fields: [
      ...(running.length
        ? [{ id: "attach", label: t("Usar"), type: "select" as const, value: "", options: [["", t("um container de debug novo")] as [string, string], ...running.map((x) => [x.name, t("{name} (já rodando, {image})", { name: x.name, image: x.image })] as [string, string])] }]
        : []),
      { id: "image", label: t("Imagem"), type: "select", value: info.images[0], options: [...info.images.map((x) => [x, x] as [string, string]), ["outra", t("outra…")]] },
      { id: "other", label: t("Outra imagem"), optional: true, placeholder: "registry/imagem:tag", hint: t("Só se escolheu outra") },
      { id: "target", label: t("Ver os processos de"), type: "select", value: containers[0] ?? "", options: [...containers.map((x) => [x, x] as [string, string]), ["", t("nenhum (só a rede do pod)")]] },
    ],
    onSubmit: (v) => {
      const image = v.image === "outra" ? String(v.other || "").trim() : String(v.image);
      if (!v.attach && !image) throw new Error(t("Informe a imagem."));
      const q = new URLSearchParams();
      if (v.attach) q.set("attach", String(v.attach));
      else {
        q.set("image", image);
        if (v.target) q.set("target", String(v.target));
        gantry.toast(t("Criando o container de debug; a primeira vez baixa a imagem"), "ok");
      }
      gantry.openTerminal("k:" + c.id, {
        id: ns + "-" + pod + "-debug-" + Date.now(),
        name: pod + " · debug",
        termHeaders: {},
        termURL: "/clusters/" + enc(c.id) + "/debug/" + enc(ns) + "/" + enc(pod) + "?" + q.toString(),
      });
    },
  });
}

// openMultiLogs: the logs of all the pods of a workload or service at once.
export function openMultiLogs(c: Cluster, ns: string, kind: string, name: string) {
  gantry.openLogs("k:" + c.id, {
    id: "varios/" + ns + "/" + kind + "/" + name,
    name: name + " · " + t("todos os pods"),
    logBase: "/clusters/" + enc(c.id) + "/varios/" + enc(ns) + "/" + kind + "/" + enc(name),
    logQuery: [],
    multi: true,
  });
}

export function openPodShell(c: Cluster, ns: string, pod: string, ct: string) {
  gantry.openTerminal("k:" + c.id, {
    id: ns + "-" + pod + (ct ? "-" + ct : ""),
    name: pod + (ct ? " · " + ct : ""),
    termHeaders: {},
    termURL: "/clusters/" + enc(c.id) + "/shell/" + enc(ns) + "/" + enc(pod) + (ct ? "?container=" + enc(ct) : ""),
  });
}

type Opts = { can?: Perms; flat?: boolean; after?: () => void; deleted?: () => void };

// KubeActions: the buttons of an object (logs, shell, scale, restart…),
// following the cluster's mode and what the account may do. In tables the
// first one is in sight and the rest in ⋯; flat shows them all.
export function KubeActions({ c, kind, r, opts = {} }: { c: Cluster; kind: string; r: Row; opts?: Opts }) {
  const menu = useRef<HTMLDetailsElement>(null);
  const out = kubeActionList(c, kind, r, opts).map((a) => ({
    key: a.key,
    node: (
      <button
        className={"btn small" + (a.cls ? " " + a.cls : "")}
        type="button"
        title={a.title}
        onClick={() => {
          if (menu.current) menu.current.open = false;
          a.onClick();
        }}
      >
        {a.label}
      </button>
    ),
  }));
  if (out.length <= 2 || opts.flat) return <>{out.map((x) => <Fragment key={x.key}>{x.node}</Fragment>)}</>;
  return (
    <>
      {out[0].node}
      <details className="more-menu mini" ref={menu}>
        <summary className="btn small" aria-label={t("Mais ações")}>
          ⋯
        </summary>
        <div className="more-list">
          {out.slice(1).map((x) => (
            <Fragment key={x.key}>{x.node}</Fragment>
          ))}
        </div>
      </details>
    </>
  );
}

type KAction = { key: string; label: string; onClick: () => void; cls?: string; title?: string };

// kubeMenu: the right-click menu of an object, with the same actions as
// its buttons and the details panel.
export function kubeMenu(e: React.MouseEvent, c: Cluster, kind: string, r: Row, opts: Opts = {}) {
  const acts = kubeActionList(c, kind, r, opts);
  openMenu(
    e,
    [
      kind === "events" ? null : { label: t("Abrir detalhes"), onClick: () => openDetail(c, kind, r.namespace ?? "", r.name) },
      "-",
      ...acts.filter((a) => a.key !== "delete").map((a) => ({ label: a.label, onClick: a.onClick, hint: a.title })),
      "-",
      { label: t("Copiar nome"), onClick: () => void navigator.clipboard?.writeText(r.name).then(() => gantry.toast(t("Copiado"), "ok")) },
      "-",
      ...acts.filter((a) => a.key === "delete").map((a) => ({ label: a.label, onClick: a.onClick, danger: true })),
    ],
    (r.namespace ? r.namespace + " / " : "") + r.name,
  );
}

// kubeActionList: what can be done with an object, following the cluster's
// mode and what the account may do.
function kubeActionList(c: Cluster, kind: string, r: Row, opts: Opts = {}): KAction[] {
  const ro = c.mode === "leitura";
  const can = { ...ALL_PERMS, ...(opts.can ?? {}) };
  const nsPath = enc(r.namespace || "-") + "/" + enc(r.name);
  const after = () => {
    void loadView(c.id, kind, true);
    opts.after?.();
  };
  const act = async (acao: string, body: unknown, msg: string) => {
    try {
      await api("/clusters/" + enc(c.id) + "/r/" + kind + "/" + nsPath + "/" + acao, { method: "POST", body: body ?? {} });
      gantry.toast(msg, "ok");
      after();
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  const out: KAction[] = [];
  const btn = (key: string, label: string, onClick: () => void, extra: { cls?: string; title?: string } = {}) => out.push({ key, label, onClick, ...extra });

  if (kind === "pods") {
    const containers: string[] = r.info?.containers ?? [];
    if (can.logs)
      btn("logs", "Logs", async () => {
        const ct = await pickOption(containers, t("Logs de qual container?"));
        if (ct == null) return;
        // A pod in a restart loop: what matters is the log of the run that died.
        openPodLogs(c, r.namespace ?? "", r.name, ct, containers.length > 1, /CrashLoopBackOff/.test(r.info?.status ?? ""));
      });
    if (!ro && can.exec)
      btn("shell", "Shell", async () => {
        const ct = await pickOption(containers, t("Shell em qual container?"));
        if (ct == null) return;
        openPodShell(c, r.namespace ?? "", r.name, ct);
      });
    if (!ro && can.debug)
      btn("debug", "Debug", () => void debugDialog(c, r.namespace ?? "", r.name, containers), {
        title: t("Um container com ferramentas (curl, dig, tcpdump…) dentro do pod, vendo os processos dele, sem reiniciar"),
      });
  }
  if (["deployments", "statefulsets", "daemonsets", "replicasets", "jobs", "services"].includes(kind) && can.logs)
    btn("logs", t("Logs dos pods"), () => openMultiLogs(c, r.namespace ?? "", kind, r.name), { title: t("Os logs de todos os pods juntos, cada um com a sua cor") });
  if ((kind === "deployments" || kind === "statefulsets" || kind === "replicasets") && !ro && can.scale) {
    btn("scale", t("Escalar"), () =>
      formDialog({
        title: t("Réplicas de {name}", { name: r.name }),
        submit: t("Aplicar"),
        fields: [{ id: "n", label: t("Réplicas"), value: String(r.info?.replicas ?? "") }],
        onSubmit: async (v) => {
          const n = parseInt(String(v.n), 10);
          if (!(n >= 0)) throw new Error(t("Informe um número."));
          await api("/clusters/" + enc(c.id) + "/r/" + kind + "/" + nsPath + "/escalar", { method: "POST", body: { replicas: n } });
          gantry.toast(r.name + ": " + tn(n, "{n} réplica", "{n} réplicas"), "ok");
          after();
        },
      }),
    );
  }
  if ((kind === "deployments" || kind === "statefulsets" || kind === "daemonsets") && !ro && can.patch) {
    btn("restart", t("Reiniciar"), async () => {
      if (await confirm(t("Reiniciar {name}?", { name: r.name }), t("Os pods são recriados aos poucos (rollout restart)."), t("Reiniciar"))) void act("reiniciar", {}, t("{name} reiniciando", { name: r.name }));
    });
  }
  if (kind === "cronjobs" && !ro) {
    if (can.createJobs) btn("run", t("Executar agora"), () => void act("executar", {}, t("Job criado a partir de {name}", { name: r.name })));
    const susp = !!r.info?.suspended;
    if (can.patch) btn("suspend", susp ? t("Retomar") : t("Suspender"), () => void act("suspender", { on: !susp }, susp ? t("{name} retomado", { name: r.name }) : t("{name} suspenso", { name: r.name })));
  }
  if (kind === "nodes" && !ro && can.patch && can.evict) {
    btn("drain", t("Esvaziar"), () => void drainNode(c, r.name, after), { title: t("Isola o nó e tira os pods dele (kubectl drain)") });
  }
  if (kind === "nodes" && !ro && can.patch) {
    const iso = !!r.info?.unschedulable;
    btn("cordon", iso ? t("Liberar") : t("Isolar"), () => void act("isolar", { on: !iso }, iso ? t("{name} liberado", { name: r.name }) : t("{name} isolado", { name: r.name })), {
      title: iso ? t("Volta a receber pods") : t("Não recebe pods novos (cordon)"),
    });
  }
  if (kind === "services") {
    const ports: { port: number; name?: string }[] = r.info?.ports ?? [];
    if (ports.length && can.proxy)
      btn(
        "open",
        t("Abrir"),
        async () => {
          const p = ports.length === 1 ? ports[0].port : await pickOption(ports.map((x) => String(x.port) + (x.name ? " (" + x.name + ")" : "")), t("Qual porta?")).then((x) => (x ? parseInt(x, 10) : 0));
          if (!p) return;
          try {
            await api("/clusters/" + enc(c.id) + "/abrir", { method: "POST", body: { ns: r.namespace, kind: "services", name: r.name, port: p } });
          } catch (e) {
            gantry.toast((e as Error).message, "err");
          }
        },
        { cls: "primary", title: t("Abre no navegador pelo proxy do cluster (HTTP)") },
      );
  }
  if ((kind === "pods" || kind === "services") && !ro && can.portforward) {
    const ports: number[] = ((r.info?.ports ?? []) as (number | { port: number })[]).map((x) => (typeof x === "object" ? x.port : x)).filter(Boolean);
    btn("pf", t("Encaminhar porta"), () => forwardDialog(c, kind, r.namespace ?? "", r.name, ports), {
      title: kind === "pods" ? t("Uma porta desta máquina que leva a uma porta do pod (como kubectl port-forward)") : t("Uma porta desta máquina que leva a uma porta do Service (como kubectl port-forward)"),
    });
  }
  if (kind === "secrets" && !ro && can.secrets) btn("values", t("Ver valores"), () => secretDialog(c, r));
  if (kind !== "events") btn("yaml", ro || !can.update ? "YAML" : t("Editar"), () => yamlDialog(c, kind, r, !can.update, opts.after));
  if (!ro && can.delete && kind !== "events" && kind !== "namespaces" && kind !== "nodes") {
    btn(
      "delete",
      t("Apagar"),
      async () => {
        const what = kind === "pods" ? t("O pod é apagado; se ele é de um deployment, outro é criado no lugar.") : t("O objeto é apagado do cluster.");
        if (!(await confirm(t("Apagar {name}?", { name: r.name }), what, t("Apagar"), true))) return;
        try {
          await api("/clusters/" + enc(c.id) + "/r/" + kind + "/" + nsPath, { method: "DELETE" });
          gantry.toast(t("{name} apagado", { name: r.name }), "ok");
          void loadView(c.id, kind, true);
          opts.deleted?.();
        } catch (e) {
          gantry.toast((e as Error).message, "err");
        }
      },
      { cls: "danger" },
    );
  }
  return out;
}

// The name in a table: opens the details panel (events open the YAML).
export function NameLink({ c, kind, r }: { c: Cluster; kind: string; r: Row }) {
  return (
    <a
      href="#"
      onClick={(e) => {
        e.preventDefault();
        if (kind === "events") yamlDialog(c, kind, r);
        else openDetail(c, kind, r.namespace ?? "", r.name);
      }}
    >
      <b>{r.name}</b>
    </a>
  );
}

// disconnectCluster closes what is open with the cluster: its logs, shells
// and YAML tabs (an editor with changes still asks), the details panel, its
// page, and on the server the watches, the history and the port-forwards.
// It connects again when opened.
export async function disconnectCluster(c: Cluster) {
  const id = c.id;
  closeTabsWhere((k) => k.startsWith("logs:k:" + id + ":") || k.startsWith("term:k:" + id + ":") || k.startsWith("yaml:" + id + ":") || k.startsWith("apply:" + id + ":"));
  if (useDetail.getState().c?.id === id) closeDetail();
  if (location.hash.startsWith("#/k/" + encodeURIComponent(id))) location.hash = "#/local";
  try {
    await api("/clusters/" + enc(id) + "/desconectar", { method: "POST" });
    useKube.setState((s) => {
      const data = { ...s.data };
      const forwards = { ...s.forwards };
      delete data[id];
      delete forwards[id];
      return { data, forwards };
    });
    gantry.toast(t("{name} desconectado", { name: c.name }), "ok");
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}
