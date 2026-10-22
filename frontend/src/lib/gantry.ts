// What views use besides the API: notices, the dock (logs, terminals,
// jobs), dialogs of the shell and the state of the app.
import { api, apiURL, CFG, enc } from "./api";
import * as st from "./store";
import { labHref, RT_NAME } from "./app";
import { confirm, formDialog } from "@/components/dialog";
import { localStore, toast } from "@/shell/toasts";
import { closeHostTabs, openJob, openLogs, openTerminal, showOutput } from "@/shell/dock";
import { forgetMachine } from "@/views/machine/store";
import { pickPath } from "@/shell/pick";
import { openHostDialog } from "@/shell/hostDialog";
import { checkAppVersion } from "@/shell/version";
import { t } from "./i18n";

async function hideHost(hst: st.Host) {
  if (!(await confirm(t("Ocultar \"{name}\"?", { name: hst.name }), t("O host detectado some da lista. Para mostrar de novo, use a seção Hosts ocultos na tela {screen}.", { screen: st.LOCAL_NAME }), t("Ocultar")))) return;
  try {
    await api("/hosts/" + enc(hst.name) + "/hide", { method: "POST" });
    location.hash = "#/";
    await st.loadHosts();
  } catch (e) {
    toast((e as Error).message, "err");
  }
}

async function removeHost(name: string) {
  if (!(await confirm(t("Remover o host \"{name}\"?", { name }), t("Ele sai da lista do Gantry. Nada muda no servidor e os containers continuam rodando."), t("Remover"), true))) return;
  try {
    await api("/hosts/" + enc(name), { method: "DELETE" });
    location.hash = "#/";
    await st.loadHosts();
  } catch (e) {
    toast((e as Error).message, "err");
  }
}

// disconnectMachine closes the connections to a machine (SSH, the agent)
// and forgets its remembered passwords; it connects again when opened.
async function disconnectMachine(m: st.Machine) {
  const p = m.primary;
  if (!p) return;
  const names = m.hosts.map((x) => x.name);
  // Leave its pages and streams first, so nothing asks for it again.
  closeHostTabs(names);
  const r = st.useStore.getState().route;
  if (r.host && names.includes(r.host)) {
    location.hash = "#/local";
    st.followHash();
  }
  st.useStore.setState((s) => {
    const labs = { ...s.labs };
    for (const n of names) delete labs[n];
    return { labs, hosts: s.hosts.map((x) => (names.includes(x.name) ? { ...x, ok: false, connecting: false, idle: true } : x)) };
  });
  st.syncEvents();
  forgetMachine(m.key);
  try {
    await api("/hosts/" + enc(p.name) + "/disconnect", { method: "POST" });
    toast(t("{name} desconectado", { name: m.name }), "ok");
  } catch (e) {
    toast((e as Error).message, "err");
  }
  await st.loadHosts();
}

function rawFetch(path: string, opts: RequestInit = {}) {
  const h: Record<string, string> = { ...((opts.headers as Record<string, string>) || {}) };
  if (CFG.token) h.Authorization = "Bearer " + CFG.token;
  return fetch(apiURL(path), { ...opts, headers: h });
}

function gitLabDialog(hst: st.Host) {
  formDialog({
    title: t("Projeto de um repositório git"),
    intro: t("O repositório é clonado no host e o compose dele vira um projeto. Depois, \"Atualizar do git\" na aba Arquivo traz as mudanças."),
    submit: t("Clonar"),
    fields: [
      { id: "url", label: t("Repositório"), placeholder: t("https://github.com/usuario/projeto.git"), hint: t("Privados: o host precisa ter acesso (chave SSH ou credencial do git).") },
      { id: "branch", label: t("Branch ou tag"), placeholder: "main", optional: true },
      { id: "file", label: t("Compose no repositório"), placeholder: "deploy/compose.yaml", optional: true, hint: t("Um arquivo ou uma pasta; vazio procura na raiz.") },
      { id: "name", label: t("Nome do projeto"), placeholder: t("projeto") },
      { id: "dir", label: t("Pasta do clone no host"), placeholder: t("~/labs/projeto"), optional: true },
      { id: "up", label: t("Subir o projeto depois de clonar"), type: "checkbox", value: true },
    ],
    onSubmit: async (v) => {
      const url = String(v.url);
      const name =
        String(v.name || "") ||
        (url.split("/").pop() || "")
          .replace(/\.git$/, "")
          .toLowerCase()
          .replace(/[^a-z0-9_-]/g, "-");
      const r = await api<{ ok: boolean; error?: string; id: string; output?: string }>("/hosts/" + enc(hst.name) + "/labs/git", {
        method: "POST",
        body: { name, url, branch: v.branch, file: v.file, dir: v.dir || "~/labs/" + name, up: v.up },
      });
      if (r.output) showOutput(hst.name + ": " + name, r);
      toast(r.ok ? (v.up ? t("Projeto {name} clonado e no ar", { name }) : t("Projeto {name} clonado", { name })) : r.error || t("falhou"), r.ok ? "ok" : "err");
      await st.loadLabs(hst.name);
      location.hash = labHref(hst.name, r.id);
    },
  });
}

export const gantry = {
  toast,
  openJob,
  store: localStore,
  loadHosts: st.loadHosts,
  loadLocal: st.loadLocal,
  hideHost,
  loadStats: st.loadStats,
  connectMachine: st.connectMachine,
  syncEvents: st.syncEvents,
  scheduleConnectingPoll: st.scheduleConnectingPoll,
  rawFetch,
  loadClusters: st.loadClusters,
  state: () => st.useStore.getState(),
  openLogs,
  openTerminal,
  machines: () => st.machines(),
  labHref: (host: string, lab: string) => labHref(host, lab),
  openHostDialog,
  removeHost,
  disconnectMachine,
  checkAppVersion,
  pickPath,
  loadGroups: st.loadGroups,
  hostByName: st.hostByName,
  refreshHost: st.refreshHost,
  loadLabs: st.loadLabs,
  showOutput,
  gitLabDialog,
  localName: () => st.LOCAL_NAME,
  rtName: (rt: string) => RT_NAME[rt] || rt,
  markBusy: st.markBusy,
};
