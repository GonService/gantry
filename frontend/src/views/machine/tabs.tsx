// The tabs of the machine page.
import { ResourceTab } from "@/views/container/resources";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { ctrHref, enc, fmtAgo, fmtPct, labHref, machHref, newCtrHref, RT_NAME, rtQuery, useApp, type Machine } from "@/lib/app";
import { copyText, fmtBytes } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import { confirm, formDialog } from "@/components/dialog";
import { ErrorBox, Loading } from "@/components/ui";
import { FilterInput, filterItems, useFilter, usePage } from "@/components/list";
import { BulkBar, SelectAll, SelectCell, reportBulk, runBulk, someNames, useSelection } from "@/components/select";
import { copyItem, openMenu } from "@/components/contextmenu";
import { ensureWeb, PortLabel, alertsMuted, setAlertsMuted } from "@/views/shared/web";
import { openVMScreen, SSHCell, VM_STATE, VMAccess, VMUsageCells, vmPorts, vmStatKey } from "@/views/shared/vm";
import { AgentBlock, EnginesList, ObsBlock } from "@/views/host/obs";
import { ImagesBlock } from "@/views/shared/vmimages";
import { queueDriftCheck, useDrift } from "@/views/lab/drift";
import { queueUpdateCheck, updCount, useUpdates } from "@/views/lab/updates";
import { engKey, forgetNet, loadCtrs, loadNet, loadOverview, loadShared, useMachine, type Engine, type Overview, type VMNet } from "./store";

export const Empty = ({ children }: { children: React.ReactNode }) => (
  <div className="panel panel-pad">
    <p className="muted" style={{ margin: 0 }}>
      {children}
    </p>
  </div>
);

function engLabel(m: Machine, e: Engine) {
  const rt = RT_NAME[e.runtime] || e.runtime;
  if (!m.local) return rt || e.label;
  return !e.label || e.label.toLowerCase() === e.runtime ? rt : e.label;
}

/* projects */

export function ProjectsTab({ m, d }: { m: Machine; d: Overview }) {
  const drift = useDrift();
  const upd = useUpdates();
  const files = m.hosts.find((x) => x.ok && x.files);
  const pk = "proj:" + m.key;
  const [q] = useFilter(pk);
  const matched = filterItems(q, d.projects, (p) => p.name + " " + (p.dir || "") + " " + (p.runtimes || []).join(" "));
  const pg = usePage(pk, matched);
  // Several at once: bring up, stop, remove. The loose containers ("_")
  // are not a project with a compose file.
  const pKey = (p: (typeof d.projects)[number]) => p.host + "/" + p.id;
  const real = (p: (typeof d.projects)[number]) => !p.id.startsWith("_") && !!p.host;
  const sel = useSelection(d.projects.filter(real).map(pKey), pg.items.filter(real).map(pKey));
  const [busy, setBusy] = useState("");
  const selectable = !!files && d.projects.some(real);
  // The right-click menu of a project.
  const projectMenu = (e: React.MouseEvent, p: (typeof d.projects)[number]) => {
    const one = (op: string, label: string) => async () => {
      try {
        const r = await api<{ id: string }>("/hosts/" + enc(p.host) + "/labs/" + enc(p.id) + "/" + op + "?job=1", { method: "POST" });
        gantry.openJob(r.id, label + " · " + p.name, async () => {
          await gantry.loadLabs(p.host);
          await loadOverview(m);
        });
      } catch (err) {
        gantry.toast((err as Error).message, "err");
      }
    };
    const ok = real(p) && !!files;
    openMenu(
      e,
      [
        { label: t("Abrir"), onClick: () => void (location.hash = labHref(p.host, p.id)) },
        ok ? { label: t("Arquivos"), onClick: () => void (location.hash = labHref(p.host, p.id, "arquivo")) } : null,
        "-",
        ok ? { label: t("Subir"), onClick: one("up", t("Subir")) } : null,
        ok && (p.running || p.vmsUp) ? { label: t("Parar"), onClick: one("stop", t("Parar")) } : null,
        ok && (p.running || p.vmsUp) ? { label: t("Reiniciar"), onClick: one("restart", t("Reiniciar")) } : null,
        "-",
        p.dir ? copyItem(t("Copiar caminho da pasta"), p.dir, () => gantry.toast(t("Copiado"), "ok")) : null,
        "-",
        ok && (p.total || p.vms)
          ? {
              label: t("Remover…"),
              danger: true,
              onClick: async () => {
                const ans = await removeProjectsDialog([p.name]);
                if (!ans) return;
                const base = "/hosts/" + enc(p.host) + "/labs/" + enc(p.id);
                try {
                  const r = await api<{ ok: boolean; error?: string }>(base + "/down", { method: "POST" });
                  if (!r.ok) throw new Error(r.error || t("falhou"));
                  if (ans.forget) await api(base, { method: "DELETE" });
                  gantry.toast(t("{name} removido", { name: p.name }), "ok");
                } catch (err) {
                  gantry.toast((err as Error).message, "err");
                }
                await gantry.loadLabs(p.host);
                await loadOverview(m);
              },
            }
          : null,
        ok && !p.total && !p.vms
          ? {
              label: t("Esquecer projeto"),
              onClick: async () => {
                await api("/hosts/" + enc(p.host) + "/labs/" + enc(p.id), { method: "DELETE" });
                await gantry.loadLabs(p.host);
                await loadOverview(m);
              },
            }
          : null,
      ],
      p.name,
    );
  };
  const bulk = async (what: "up" | "stop" | "down") => {
    const list = d.projects.filter((p) => real(p) && sel.has(pKey(p)));
    if (!list.length) return;
    let forget = false;
    if (what === "down") {
      const ans = await removeProjectsDialog(list.map((p) => p.name));
      if (!ans) return;
      forget = ans.forget;
    }
    if (what === "up") {
      // Each one in its own tab of the panel, with the output, as Subir does.
      for (const p of list) {
        try {
          const r = await api<{ id: string }>("/hosts/" + enc(p.host) + "/labs/" + enc(p.id) + "/up?job=1", { method: "POST" });
          gantry.openJob(r.id, t("Subir") + " · " + p.name, async () => {
            await gantry.loadLabs(p.host);
          });
        } catch (e) {
          gantry.toast(p.name + ": " + (e as Error).message, "err");
        }
      }
      sel.clear();
      return;
    }
    const verb = what === "stop" ? t("Parando") : t("Removendo");
    const fails = await runBulk(
      list,
      (p) => p.name,
      async (p) => {
        const base = "/hosts/" + enc(p.host) + "/labs/" + enc(p.id);
        if (p.total || p.vms || what === "stop") {
          const r = await api<{ ok: boolean; error?: string }>(base + "/" + what, { method: "POST" });
          if (!r.ok) throw new Error(r.error || t("falhou"));
        }
        if (what === "down" && forget) await api(base, { method: "DELETE" });
      },
      setBusy,
      verb,
      2,
    );
    setBusy("");
    sel.clear();
    reportBulk(list.length, fails, what === "stop" ? tn(list.length, "{n} projeto parado", "{n} projetos parados") : tn(list.length, "{n} projeto removido", "{n} projetos removidos"));
    for (const h of new Set(list.map((p) => p.host))) await gantry.loadLabs(h);
    await loadOverview(m);
  };
  return (
    <div className="block" id="proj-box">
      <div className="block-head">
        <h2 className="block-title">{t("Projetos")}</h2>
        <span className="muted small">{t("do mais recente para o mais antigo")}</span>
        <span className="grow" />
        {files ? (
          <details className="more-menu">
            <summary className="btn">{t("Outras formas")}</summary>
            <div className="more-list panel">
              <a href={newCtrHref(files.name)}>
                <b>{t("Container avulso")}</b>
                <span>{t("Um container fora de projeto, pelo formulário completo")}</span>
              </a>
              <button type="button" onClick={() => gantry.gitLabDialog(files)}>
                <b>{t("Projeto de um repositório git")}</b>
                <span>{t("Clona e usa o compose do repositório")}</span>
              </button>
              <button type="button" onClick={() => openDirDialog(files.name, files.compose)}>
                <b>{t("Abrir pasta com compose")}</b>
                <span>{t("Um compose que já está no host")}</span>
              </button>
              <button type="button" onClick={() => importDialog(files.name)}>
                <b>{t("Importar pacote")}</b>
                <span>{t("Um .tar.gz exportado de outro Gantry")}</span>
              </button>
              <button type="button" onClick={() => importUNLDialog(files.name)}>
                <b>{t("Importar do PNETLab/EVE-NG")}</b>
                <span>{t("Um lab .unl, com os mesmos equipamentos e configurações")}</span>
              </button>
            </div>
          </details>
        ) : null}
        <a className="btn primary" href={(m.local ? "#/local" : "#/h/" + enc(m.primary!.name)) + "/novo"}>
          {t("＋ Criar projeto")}
        </a>
      </div>
      {!d.projects.length ? (
        <Empty>{t("Nenhum projeto ainda. Use Criar projeto: containers, uma VM ou os dois.")}</Empty>
      ) : (
        <>
          {d.projects.length > 10 ? (
            <div className="list-tools">
              <FilterInput listKey={pk} placeholder={t("Filtrar projetos…")} />
            </div>
          ) : null}
          {selectable ? (
            <BulkBar s={sel} busy={busy}>
              <button type="button" className="btn small" onClick={() => void bulk("up")}>
                {t("Subir")}
              </button>
              <button type="button" className="btn small" onClick={() => void bulk("stop")}>
                {t("Parar")}
              </button>
              <button type="button" className="btn small danger" onClick={() => void bulk("down")}>
                {t("Remover")}
              </button>
            </BulkBar>
          ) : null}
          {!matched.length ? (
            <Empty>{t("Nenhum projeto corresponde ao filtro.")}</Empty>
          ) : (
            <div className={"panel proj-list" + (selectable ? " selectable" : "")}>
              {selectable ? (
                <div className="proj-item proj-head">
                  <SelectAll s={sel} keys={pg.items.filter(real).map(pKey)} as="span" />
                  <span className="muted small">{t("Selecionar todos")}</span>
                </div>
              ) : null}
              {pg.items.map((p) => {
                const up = p.running + p.vmsUp,
                  total = p.total + p.vms;
                const state = !total ? ["", t("sem recursos criados")] : up === total ? ["on", t("rodando")] : up ? ["warn", t("{up} de {total} rodando", { up, total })] : ["", t("parado")];
                const what = [p.total ? tn(p.total, "{n} container", "{n} containers") : null, p.vms ? tn(p.vms, "{n} VM", "{n} VMs") : null].filter(Boolean).join(" · ");
                if (!p.id.startsWith("_") && p.host) queueUpdateCheck(p.host, p.id);
                const nUpd = updCount(upd[p.host + "/" + p.id]);
                if (!p.id.startsWith("_") && p.host && (p.total || p.vms)) queueDriftCheck(p.host, p.id);
                const changed = !!drift[p.host + "/" + p.id]?.data?.pending;
                return (
                  <div key={p.host + "/" + p.id} className={"proj-item" + (sel.has(pKey(p)) ? " selected" : "")} onContextMenu={(e) => projectMenu(e, p)}>
                    {selectable ? (
                      <span className="sel">
                        {real(p) ? (
                          <input type="checkbox" aria-label={t("Selecionar {name}", { name: p.name })} checked={sel.has(pKey(p))} onChange={() => {}} onClick={(e) => sel.toggle(pKey(p), e.shiftKey)} />
                        ) : null}
                      </span>
                    ) : null}
                    <a className="proj-row" href={labHref(p.host, p.id)}>
                      <span className="proj-name">
                        <span className={"dot " + state[0]} />
                        {p.name}
                        {nUpd ? (
                          <span className="badge warn" style={{ marginLeft: 8 }} title={t("Há versões ou imagens novas; veja em Versões das imagens no projeto")}>
                            {tn(nUpd, "{n} atualização", "{n} atualizações")}
                          </span>
                        ) : null}
                        {changed ? (
                          <span className="badge accent" style={{ marginLeft: 8 }} title={t("Os arquivos mudaram e ainda não foram aplicados")}>
                            {t("arquivos alterados")}
                          </span>
                        ) : null}
                      </span>
                      <span className="proj-meta">
                        {what || "—"}
                        {(p.runtimes || []).map((r) => (
                          <span key={r} className={"rt-chip rt-" + r}>
                            {r}
                          </span>
                        ))}
                      </span>
                      <span className="proj-dir mono">{p.dir || ""}</span>
                      <span className="proj-when muted small">{state[1] + (p.used ? " · " + (Date.now() / 1000 - p.used < 86400 * 60 ? t("há {ago}", { ago: fmtAgo(p.used) }) : fmtAgo(p.used)) : "")}</span>
                    </a>
                  </div>
                );
              })}
            </div>
          )}
          {pg.bar}
        </>
      )}
    </div>
  );
}

// removeProjectsDialog: confirm, and whether they also leave Gantry's list.
function removeProjectsDialog(names: string[]): Promise<{ forget: boolean } | null> {
  return new Promise((resolve) => {
    let answered = false;
    formDialog({
      title: tn(names.length, "Remover {n} projeto?", "Remover {n} projetos?"),
      intro: t("Apaga os containers, as redes e as VMs (com os discos) dos projetos. Volumes nomeados e os arquivos são mantidos.") + " " + someNames(names),
      submit: t("Remover"),
      fields: [{ id: "forget", type: "checkbox", label: t("Também tirar da lista do Gantry (a pasta continua no disco)"), value: false }],
      onSubmit: (v) => {
        answered = true;
        resolve({ forget: !!v.forget });
      },
      onClose: () => {
        if (!answered) resolve(null);
      },
    });
  });
}

export function openDirDialog(host: string, compose: string[]) {
  formDialog({
    title: t("Abrir pasta com compose"),
    intro: t("Sobe o compose que já está numa pasta do host ({cmd} up -d) e passa a mostrá-lo como projeto.", { cmd: compose.join(" ") }),
    submit: t("Subir"),
    fields: [{ id: "dir", label: t("Pasta"), placeholder: "~/projetos/meu-app", type: "folder", host }],
    onSubmit: async (v) => {
      const res = await api<{ ok: boolean; error?: string; project: string; output?: string }>("/hosts/" + enc(host) + "/open", { method: "POST", body: { dir: v.dir } });
      gantry.showOutput(host + ": " + v.dir, res);
      if (!res.ok) throw new Error(res.error || t("falhou"));
      gantry.toast(t("Projeto {name} no ar", { name: res.project }), "ok");
      location.hash = labHref(host, res.project);
    },
  });
}

export function importDialog(host: string) {
  formDialog({
    title: t("Importar pacote"),
    intro: t("Abre um projeto exportado pelo Gantry (.tar.gz, ou .gantrylab de um lab de rede, com a configuração dos equipamentos) numa pasta nova de {host}. As portas são as do pacote: se alguma estiver em uso aqui, a saída diz qual variável trocar no .env.", { host }),
    submit: t("Importar"),
    fields: [
      { id: "file", label: t("Pacote"), type: "file", accept: ".tar.gz,.tgz,.gantrylab,application/gzip" },
      { id: "dir", label: t("Pasta no host"), placeholder: "~/gantry/nome-do-projeto", type: "folder", host },
      { id: "up", label: t("Subir depois de importar"), type: "checkbox", value: true },
      { id: "replace", label: t("Substituir se a pasta já existir: a pasta antiga fica guardada ao lado (.antes-data) e o projeto que a usava sai do Gantry, com os containers, as VMs e a configuração dos equipamentos"), type: "checkbox", value: false },
    ],
    onSubmit: async (v) => {
      const file = v.file as File | null;
      if (!file) throw new Error(t("Escolha o pacote."));
      const dir =
        String(v.dir || "") ||
        "~/gantry/" +
          (file.name
            .replace(/\.(tar\.gz|tgz|gantrylab)$/, "")
            .replace(/^gantry-/, "")
            .replace(/-\d{8}-\d{4}(-\d+)?$/, "") || "projeto");
      const r = await gantry.rawFetch("/hosts/" + enc(host) + "/labs/import?dir=" + enc(dir) + (v.up ? "&up=1&job=1" : "") + (v.replace ? "&replace=1" : ""), { method: "POST", body: file, headers: { "Content-Type": "application/gzip" } });
      const res = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(res.error || r.statusText);
      if (res.output) gantry.showOutput(host + ": " + res.id, res);
      if (!res.ok) throw new Error(res.error || t("falhou"));
      gantry.toast(
        t("Projeto {name} importado em {dir}", { name: res.id, dir: res.dir }) + (res.equipment ? " · " + t("configuração dos equipamentos aplicada") : "") + (res.kept ? " · " + t("a pasta antiga ficou em {path}", { path: res.kept }) : ""),
        "ok",
      );
      if (res.job)
        gantry.openJob(res.job, t("Subir · {name}", { name: res.id }), async () => {
          await gantry.loadLabs(host);
        });
      await gantry.loadLabs(host);
      location.hash = labHref(host, res.id);
    },
  });
}

export function importUNLDialog(host: string) {
  formDialog({
    title: t("Importar do PNETLab/EVE-NG"),
    intro: t("Cria um projeto em {host} com o lab .unl como ele é: os mesmos equipamentos (IOL, Dynamips com as mesmas placas, VPCS), cabos e configurações salvas, que entram na NVRAM antes do primeiro boot. Nada é adaptado: equipamento que o Gantry ainda não roda impede a importação, e o que ficou de fora vai no IMPORTACAO.md do projeto.", { host }),
    submit: t("Importar"),
    fields: [
      { id: "file", label: t("Lab"), type: "file", accept: ".unl,application/xml,text/xml" },
      { id: "dir", label: t("Pasta no host"), placeholder: "~/labs/nome-do-lab", type: "folder", host },
      { id: "up", label: t("Subir depois de importar"), type: "checkbox", value: true },
    ],
    onSubmit: async (v) => {
      const file = v.file as File | null;
      if (!file) throw new Error(t("Escolha o arquivo .unl."));
      const q = "file=" + enc(file.name) + (v.dir ? "&dir=" + enc(String(v.dir)) : "") + (v.up ? "&up=1" : "");
      const r = await gantry.rawFetch("/hosts/" + enc(host) + "/labs/importar-unl?" + q, { method: "POST", body: file, headers: { "Content-Type": "application/xml" } });
      const res = await r.json().catch(() => ({}));
      if (!r.ok || !res.ok) {
        const bl: string[] = res.blocking || [];
        throw new Error(bl.length ? t("O lab tem equipamento que o Gantry ainda não roda; nada foi criado:") + "\n• " + bl.join("\n• ") : res.error || r.statusText);
      }
      const missing: string[] = res.missing || [];
      const progs: { name: string; install?: string; note?: string }[] = res.programs || [];
      const parts: string[] = [];
      if (missing.length) parts.push(t("faltam imagens nesta máquina ({list}); ponha os arquivos numa pasta de imagens", { list: missing.join(", ") }));
      for (const p of progs)
        parts.push(p.install ? t("falta o programa {name}; instale com: {cmd}", { name: p.name, cmd: p.install }) : t("falta o programa {name}. {note}", { name: p.name, note: p.note || "" }));
      if (parts.length) gantry.toast(t("Projeto {name} criado, sem subir:", { name: res.id }) + " " + parts.join("; ") + ". " + t("Depois use Subir."), "err");
      else gantry.toast(t("Projeto {name} importado; veja o IMPORTACAO.md na aba Arquivo", { name: res.id }), "ok");
      const remarks: string[] = res.remarks || [];
      if (remarks.length) gantry.toast(t("Pontos de atenção no lab original (importado como está):") + "\n• " + remarks.join("\n• "), "err");
      if (res.job)
        gantry.openJob(res.job, t("Subir · {name}", { name: res.id }), async () => {
          await gantry.loadLabs(host);
        });
      await gantry.loadLabs(host);
      location.hash = labHref(host, res.id);
    },
  });
}

/* containers */

export function ContainersTab({ m, engines, meng, mseg }: { m: Machine; engines: Engine[]; meng: string; mseg: string }) {
  if (!engines.length) return <p className="muted">{t("Nenhum engine respondeu.")}</p>;
  const sel = engines.find((e) => engKey(e) === meng) || engines.find((e) => e.ok) || engines[0];
  const seg = mseg || "containers";
  const segs: [string, string][] = [
    ["containers", "Containers"],
    ["images", t("Imagens")],
    ["volumes", "Volumes"],
    ["networks", t("Redes")],
  ];
  const hst = gantry.hostByName(sel.host);
  return (
    <div>
      <div className="eng-tabs" role="tablist" aria-label="Engines">
        {engines.map((e) => (
          <a
            key={engKey(e)}
            className={"eng-tab" + (e === sel ? " sel" : "")}
            role="tab"
            aria-selected={e === sel}
            href={machHref(m, "containers", engKey(e), seg)}
            title={e.error || (e.version ? e.runtime + " " + e.version : "")}
          >
            <span className={"dot " + (e.ok ? (e.running ? "on" : "") : "err")} />
            <span>{engLabel(m, e)}</span>
            {e.ok ? <span className="tab-count">{String(e.containers)}</span> : null}
          </a>
        ))}
      </div>
      {!sel.ok ? (
        <ErrorBox>{sel.error || t("engine indisponível")}</ErrorBox>
      ) : (
        <>
          <div className="seg seg-sub">
            {segs.map(([k, l]) => (
              <a key={k} className={"seg-btn" + (k === seg ? " sel" : "")} href={machHref(m, "containers", engKey(sel), k)}>
                {l}
              </a>
            ))}
          </div>
          {seg === "containers" ? (
            <>
              <div className="block-head">
                <h2 className="block-title">{engLabel(m, sel) + (sel.version ? " " + sel.version : "")}</h2>
                <span className="grow" />
                <CreateMenu
                  label={t("＋ Criar container")}
                  ways={[
                    { title: t("Um container"), text: t("Fora de projeto, pelo formulário completo (imagem, portas, volumes, variáveis)"), href: newCtrHref(sel.host, sel.rt) },
                    { title: t("De comandos docker run"), text: t("Cole um ou mais docker run; viram um projeto com compose"), href: newProjHref(m, { tipo: "ctr", origem: "run", engine: engKey(sel) }) },
                    { title: t("De um arquivo compose"), text: t("Cole ou abra um docker-compose.yaml do computador"), href: newProjHref(m, { tipo: "ctr", origem: "compose", engine: engKey(sel) }) },
                    { title: t("De um modelo"), text: t("Postgres, Redis, observabilidade e os seus modelos"), href: newProjHref(m, { tipo: "ctr", origem: "modelo", engine: engKey(sel) }) },
                  ]}
                />
              </div>
              <CtrTable e={sel} />
            </>
          ) : hst ? (
            <ResourceTab key={engKey(sel) + seg} hst={hst} kind={seg} rt={sel.rt} />
          ) : null}
        </>
      )}
    </div>
  );
}

function CtrTable({ e }: { e: Engine }) {
  const k = engKey(e);
  const st = useMachine((s) => s.ctrs[k]);
  const S = useApp();
  const pk = "ctr:" + k;
  const [q] = useFilter(pk);
  useEffect(() => {
    if (!useMachine.getState().ctrs[k]) loadCtrs(e);
    ensureWeb(e.host);
  }, [k]); // eslint-disable-line react-hooks/exhaustive-deps
  const items = st?.items ?? [];
  const matched = filterItems(q, items, (c) => c.name + " " + c.image + " " + (c.project || "") + " " + (c.service || "") + " " + c.state + " " + c.ports.map((p) => p.PublicPort || "").join(" "));
  const pg = usePage(pk, matched);
  const sel = useSelection(
    items.map((c) => c.id),
    pg.items.map((c) => c.id),
  );
  const [busy, setBusy] = useState("");
  if (!st) return <Loading />;
  if (st.err) return <ErrorBox>{st.err}</ErrorBox>;
  if (!items.length) return <Empty>{t("Nenhum container neste engine.")}</Empty>;
  const stats = e.rt ? {} : S.stats[e.host] || {};
  const running = items.filter((c) => c.state === "running").length;
  const act = (id: string, action: string, label: string) => (
    <button
      className="btn small"
      onClick={async (ev) => {
        const b = ev.currentTarget;
        b.disabled = true;
        try {
          await api("/hosts/" + enc(e.host) + "/containers/" + id + "/" + action + rtQuery(e.rt), { method: "POST" });
        } catch (err) {
          gantry.toast((err as Error).message, "err");
        }
        await loadCtrs(e);
        b.disabled = false;
      }}
    >
      {label}
    </button>
  );
  // One container from the right-click menu.
  const ctrOp = async (id: string, action: string) => {
    try {
      const base = "/hosts/" + enc(e.host) + "/containers/" + id;
      await api(action === "rm" ? base + rtQuery(e.rt) : base + "/" + action + rtQuery(e.rt), { method: action === "rm" ? "DELETE" : "POST" });
    } catch (err) {
      gantry.toast((err as Error).message, "err");
    }
    await loadCtrs(e);
  };
  // Several at once: start, stop, remove.
  const bulk = async (what: "start" | "stop" | "rm") => {
    const list = items.filter((c) => sel.has(c.id) && (what === "rm" || (what === "start" ? c.state !== "running" : c.state === "running")));
    if (!list.length) {
      gantry.toast(what === "start" ? t("Os selecionados já estão rodando.") : t("Nenhum dos selecionados está rodando."), "err");
      return;
    }
    if (what === "rm" && !(await confirm(tn(list.length, "Remover {n} container?", "Remover {n} containers?"), t("Os que estão rodando são parados antes. Volumes não são apagados.") + " " + someNames(list.map((c) => c.name)), t("Remover"), true)))
      return;
    const verb = what === "rm" ? t("Removendo") : what === "stop" ? t("Parando") : t("Iniciando");
    const fails = await runBulk(
      list,
      (c) => c.name,
      async (c) => {
        const base = "/hosts/" + enc(e.host) + "/containers/" + c.id;
        await api(what === "rm" ? base + rtQuery(e.rt) : base + "/" + what + rtQuery(e.rt), { method: what === "rm" ? "DELETE" : "POST" });
      },
      setBusy,
      verb,
    );
    setBusy("");
    sel.clear();
    reportBulk(
      list.length,
      fails,
      what === "rm" ? tn(list.length, "{n} container removido", "{n} containers removidos") : what === "stop" ? tn(list.length, "{n} container parado", "{n} containers parados") : tn(list.length, "{n} container iniciado", "{n} containers iniciados"),
    );
    await loadCtrs(e);
  };
  return (
    <div id="ctr-list" data-key={k}>
      <div className="list-tools">
        <FilterInput listKey={pk} placeholder={t("Filtrar por nome, imagem, projeto…")} />
        <span className="muted small">
          {t("{running} rodando de {total}", { running, total: items.length })}
        </span>
      </div>
      <BulkBar s={sel} busy={busy}>
        <button type="button" className="btn small" onClick={() => void bulk("start")}>
          {t("Iniciar")}
        </button>
        <button type="button" className="btn small" onClick={() => void bulk("stop")}>
          {t("Parar")}
        </button>
        <button type="button" className="btn small danger" onClick={() => void bulk("rm")}>
          {t("Remover")}
        </button>
      </BulkBar>
      {!matched.length ? (
        <Empty>{t("Nenhum container corresponde ao filtro.")}</Empty>
      ) : (
        <div className="panel table-wrap">
          <table className="grid">
            <thead>
              <tr>
                <SelectAll s={sel} keys={pg.items.map((c) => c.id)} />
                <th>{t("Nome")}</th>
                <th>{t("Imagem")}</th>
                <th>{t("Estado")}</th>
                <th className="num">CPU</th>
                <th className="num">{t("Memória")}</th>
                <th>{t("Portas")}</th>
                <th className="act">
                  <span className="sr">{t("Ações")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {pg.items.map((c) => {
                const on = c.state === "running";
                const dot = on ? "on" : c.state === "paused" || c.state === "restarting" ? "warn" : c.state === "dead" ? "err" : "";
                const s = stats[c.id];
                const ref = { id: c.id, name: c.name, service: c.service, runtime: e.rt };
                const ports = (c.ports || []).filter((p, i, a) => a.findIndex((x) => x.PublicPort === p.PublicPort && x.PrivatePort === p.PrivatePort) === i);
                return (
                  <tr
                    key={c.id}
                    className={sel.has(c.id) ? "selected" : undefined}
                    onContextMenu={(ev) =>
                      openMenu(
                        ev,
                        [
                          { label: t("Abrir"), onClick: () => void (location.hash = ctrHref(e.host, e.rt, c.id)) },
                          { label: "Logs", onClick: () => gantry.openLogs(e.host, ref) },
                          on ? { label: "Shell", onClick: () => gantry.openTerminal(e.host, ref) } : null,
                          "-",
                          c.state === "paused"
                            ? { label: t("Retomar"), onClick: () => ctrOp(c.id, "unpause") }
                            : on
                              ? { label: t("Parar"), onClick: () => ctrOp(c.id, "stop") }
                              : { label: t("Iniciar"), onClick: () => ctrOp(c.id, "start") },
                          on ? { label: t("Reiniciar"), onClick: () => ctrOp(c.id, "restart") } : null,
                          "-",
                          c.project ? { label: t("Abrir o projeto"), onClick: () => void (location.hash = labHref(e.host, c.project!)) } : null,
                          copyItem(t("Copiar ID"), c.id, () => gantry.toast(t("Copiado"), "ok")),
                          "-",
                          {
                            label: t("Remover…"),
                            danger: true,
                            onClick: async () => {
                              if (!(await confirm(t("Remover {name}?", { name: c.name }), t("Se estiver rodando, é parado antes. Volumes não são apagados."), t("Remover"), true))) return;
                              await ctrOp(c.id, "rm");
                            },
                          },
                        ],
                        c.name,
                      )
                    }
                  >
                    <SelectCell s={sel} k={c.id} label={c.name} />
                    <td>
                      <div className="cname">
                        <a className="cname-link" href={ctrHref(e.host, e.rt, c.id)}>
                          <span className={"dot " + dot} />
                          {c.name}
                        </a>
                        {c.project ? (
                          <small>
                            {t("projeto")} <a href={labHref(e.host, c.project)}>{c.project}</a>
                            {c.service ? " · " + c.service : ""}
                          </small>
                        ) : null}
                      </div>
                    </td>
                    <td className="mono">{c.image.length > 40 ? "…" + c.image.slice(-38) : c.image}</td>
                    <td className="small">{c.status}</td>
                    <td className="num">{on && s ? fmtPct(s.cpu) : "—"}</td>
                    <td className="num">{on && s ? fmtBytes(s.mem) : "—"}</td>
                    <td className="mono">
                      {ports.length ? (
                        <div className="ports">
                          {ports.map((p, i) =>
                            p.PublicPort ? (
                              <PortLabel key={i} host={e.host} port={p.PublicPort} text={":" + p.PublicPort + "→" + p.PrivatePort} />
                            ) : (
                              <span key={i} className="muted">
                                {p.PrivatePort + "/" + p.Type}
                              </span>
                            ),
                          )}
                        </div>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td className="act">
                      {on ? (
                        <button className="btn small" onClick={() => gantry.openTerminal(e.host, ref)}>
                          Shell
                        </button>
                      ) : null}
                      <button className="btn small" onClick={() => gantry.openLogs(e.host, ref)}>
                        Logs
                      </button>
                      {c.state === "paused" ? act(c.id, "unpause", t("Retomar")) : on ? act(c.id, "stop", t("Parar")) : act(c.id, "start", t("Iniciar"))}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {pg.bar}
    </div>
  );
}

/* VMs */

// NetPanel: the network between the VMs of this machine (lan: true).
function NetPanel({ m }: { m: Machine }) {
  const host = m.hosts.find((x) => x.ok && x.kind !== "tcp")?.name;
  const c = useMachine((s) => s.net[m.key]);
  useEffect(() => {
    if (host && !c) loadNet(m, host);
  }, [host, c]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!host || !c || c === "loading") return null;
  const bridged = c.net === "bridge";
  const configure = (
    <button className="btn small btn-ghost" onClick={() => netDialog(m, host, c)}>
      {t("Modo da rede…")}
    </button>
  );
  if (c.mode !== "none" && c.mode !== "error" && !(c.problems || []).length) {
    return (
      <p className="muted small" style={{ margin: "0 0 12px" }}>
        {bridged
          ? t("Rede entre VMs: bridge em {iface}, na mesma rede desta máquina (endereços {prefix}.{first} a {prefix}.{last}).", { iface: c.interface, prefix: c.prefix, first: c.first, last: c.last })
          : t("Rede entre VMs: ativa ({mode}, {prefix}.0/24, máquina em {gateway}).", { mode: c.mode === "vmnet" ? "socket_vmnet" : "bridge gantry0", prefix: c.prefix, gateway: c.gateway })}{" "}
        {t("Use")} <code>lan: true</code> {t("numa VM para colocá-la nessa rede.")} {configure}
      </p>
    );
  }
  const cmd = c.hint || "";
  return (
    <div className="banner info" style={{ marginBottom: 12 }}>
      <div className="grow">
        <b>{t("Rede entre VMs")}</b> —{" "}
        {(c.problems || []).length
          ? t("quase pronta: {problems}.", { problems: c.problems!.join("; ") })
          : t("ainda não ativada nesta máquina. Ela deixa as VMs de um projeto se enxergarem e os containers acessarem as VMs pelo nome (lan: true). Precisa de administrador uma vez:")}
        {cmd ? (
          <pre className="code" style={{ margin: "8px 0 0", whiteSpace: "pre-wrap" }}>
            {cmd}
          </pre>
        ) : null}
      </div>
      {cmd ? (
        <button className="btn small" onClick={async () => gantry.toast((await copyText(cmd)) ? t("Comando copiado; cole no Terminal") : cmd, "ok")}>
          {t("Copiar comando")}
        </button>
      ) : null}
      <button className="btn small btn-ghost" onClick={() => forgetNet(m.key)}>
        {t("Verificar de novo")}
      </button>
      {configure}
    </div>
  );
}

// netDialog chooses the VM network of the machine: compartilhada (a network
// only of its VMs) or bridge (the network the machine itself is on, with
// fixed addresses from a range outside the router's DHCP).
function netDialog(m: Machine, host: string, c: VMNet) {
  const bridged = c.net === "bridge";
  formDialog({
    title: t("Modo da rede entre VMs"),
    intro: (
      <>
        <p>{t("Compartilhada: uma rede só das VMs desta máquina, que o resto da rede não vê. Bridge: as VMs ficam na mesma rede desta máquina (a da sua casa ou do escritório), e qualquer aparelho dela chega nelas pelo IP. Use a bridge quando a compartilhada não funcionar, por exemplo com uma VPN como o Cloudflare WARP.")}</p>
        <p>{t("No modo bridge, o Gantry dá a cada VM um IP fixo da faixa abaixo: escolha uma faixa que o DHCP do roteador não usa. As VMs que já existem só mudam de rede depois de removidas e criadas de novo.")}</p>
      </>
    ),
    fields: [
      { id: "mode", label: t("Modo"), type: "select", options: [["compartilhada", t("Compartilhada")], ["bridge", t("Bridge (a rede desta máquina)")]], value: bridged ? "bridge" : "compartilhada" },
      { id: "interface", label: t("Interface"), value: c.interface || c.lanIface || "en0", hint: t("só no modo bridge; a placa por onde esta máquina sai para a rede (no Mac, en0 costuma ser o Wi-Fi ou o cabo)") },
      { id: "gateway", label: t("Gateway (o roteador)"), value: (bridged ? c.gateway : "") || c.lanGateway || "", placeholder: "192.168.0.1", hint: t("a rede precisa ser /24 (máscara 255.255.255.0), o comum em casa") },
      { id: "first", label: t("Faixa: do endereço"), type: "number", value: String(c.first && bridged ? c.first : 200) },
      { id: "last", label: t("até o endereço"), type: "number", value: String(c.last && bridged ? c.last : 230) },
    ],
    submit: t("Salvar"),
    onSubmit: async (v) => {
      const body = { mode: String(v.mode), interface: String(v.interface || "").trim(), gateway: String(v.gateway || "").trim(), first: Number(v.first), last: Number(v.last) };
      await api("/hosts/" + enc(host) + "/vmnet", { method: "PUT", body });
      forgetNet(m.key);
      gantry.toast(body.mode === "bridge" ? t("Rede entre VMs em bridge na {iface}", { iface: body.interface }) : t("Rede entre VMs compartilhada"), "ok");
    },
  });
}

export function VMsTab({ m, d }: { m: Machine; d: Overview }) {
  const S = useApp();
  const sys = useMachine((s) => s.sys[m.key]?.info) || {};
  const pk = "vms:" + m.key;
  const [q] = useFilter(pk);
  const matched = filterItems(q, d.vms, (vm) => vm.name + " " + vm.project + " " + vm.state);
  const pg = usePage(pk, matched);
  const vmKey = (vm: (typeof d.vms)[number]) => vm.host + "/" + vm.project + "/" + vm.name;
  const sel = useSelection(d.vms.map(vmKey), pg.items.map(vmKey));
  const [busy, setBusy] = useState("");
  useEffect(() => {
    for (const host of new Set(d.vms.map((vm) => vm.host))) ensureWeb(host);
  }, [d.vms.length]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!d.vms.length)
    return (
      <div>
        <NetPanel m={m} />
        <div className="panel panel-pad">
          <div className="block-head" style={{ marginBottom: 6 }}>
            <p style={{ margin: 0 }}>{t("Nenhuma VM nesta máquina.")}</p>
            <span className="grow" />
            {m.primary ? <CreateMenu label={t("＋ Criar VM")} ways={vmWays(m)} /> : null}
          </div>
          <p className="muted small" style={{ margin: 0 }}>
            {t("VMs vêm de projetos com x-vms (QEMU na própria máquina). Use Criar VM: um formulário, um modelo, um arquivo ou um disco importado.")}
            {sys.qemu === false && sys.cpus ? t(" O QEMU não foi encontrado aqui.") : ""}
            {sys.kvm ? t(" KVM disponível.") : ""}
          </p>
        </div>
      </div>
    );
  const op = (vm: (typeof d.vms)[number], action: string, label: string, ask?: [string, string]) => (
    <button
      className={"btn small" + (action === "rm" ? " danger" : "")}
      onClick={async (ev) => {
        if (ask && !(await confirm(ask[0], ask[1], label, true))) return;
        const b = ev.currentTarget;
        b.disabled = true;
        try {
          const r = await api<{ ok: boolean; error?: string }>("/hosts/" + enc(vm.host) + "/labs/" + enc(vm.project) + "/vms/" + enc(vm.name) + "/" + action, { method: "POST" });
          if (!r.ok) {
            gantry.showOutput("VM " + vm.name, r);
            gantry.toast(r.error || t("falhou"), "err");
          } else gantry.toast(t("VM {name}: ok", { name: vm.name }), "ok");
        } catch (err) {
          gantry.toast((err as Error).message, "err");
        }
        await loadOverview(m);
        b.disabled = false;
      }}
    >
      {label}
    </button>
  );
  // One VM from the right-click menu.
  const vmOp = async (vm: (typeof d.vms)[number], action: string) => {
    try {
      const r = await api<{ ok: boolean; error?: string }>("/hosts/" + enc(vm.host) + "/labs/" + enc(vm.project) + "/vms/" + enc(vm.name) + "/" + action, { method: "POST" });
      if (!r.ok) {
        gantry.showOutput("VM " + vm.name, r);
        gantry.toast(r.error || t("falhou"), "err");
      } else gantry.toast(t("VM {name}: ok", { name: vm.name }), "ok");
    } catch (err) {
      gantry.toast((err as Error).message, "err");
    }
    await loadOverview(m);
  };
  // Several at once: start, stop, remove (the disk goes too).
  const bulk = async (what: "start" | "stop" | "rm") => {
    const list = d.vms.filter((vm) => sel.has(vmKey(vm)) && (what === "rm" ? vm.state !== "missing" : what === "start" ? vm.state !== "running" : vm.state === "running"));
    if (!list.length) {
      gantry.toast(what === "start" ? t("As VMs selecionadas já estão rodando.") : what === "stop" ? t("Nenhuma das VMs selecionadas está rodando.") : t("As VMs selecionadas não foram criadas ainda."), "err");
      return;
    }
    if (what === "rm" && !(await confirm(tn(list.length, "Remover {n} VM?", "Remover {n} VMs?"), t("As VMs são desligadas e os discos delas são apagados.") + " " + someNames(list.map((vm) => vm.name)), t("Remover"), true))) return;
    const verb = what === "rm" ? t("Removendo") : what === "stop" ? t("Parando") : t("Iniciando");
    const fails = await runBulk(
      list,
      (vm) => vm.name,
      async (vm) => {
        const r = await api<{ ok: boolean; error?: string }>("/hosts/" + enc(vm.host) + "/labs/" + enc(vm.project) + "/vms/" + enc(vm.name) + "/" + what, { method: "POST" });
        if (!r.ok) throw new Error(r.error || t("falhou"));
      },
      setBusy,
      verb,
      2,
    );
    setBusy("");
    sel.clear();
    reportBulk(list.length, fails, what === "rm" ? tn(list.length, "{n} VM removida", "{n} VMs removidas") : what === "stop" ? tn(list.length, "{n} VM parada", "{n} VMs paradas") : tn(list.length, "{n} VM iniciada", "{n} VMs iniciadas"));
    await loadOverview(m);
  };
  const lanCol = d.vms.some((vm) => vm.lan);
  const heads = ["VM", t("Projeto"), t("Estado"), "CPU", t("Memória"), t("Disco"), lanCol ? t("Rede") : null, t("Portas"), "SSH", ""].filter((h) => h !== null) as string[];
  return (
    <div>
      <NetPanel m={m} />
      <div className="block-head">
        <h2 className="block-title">{t("VMs")}</h2>
        <span className="muted small">{t("cada VM pertence a um projeto")}</span>
        <span className="grow" />
        {m.primary ? <CreateMenu label={t("＋ Criar VM")} ways={vmWays(m)} /> : null}
      </div>
      {d.vms.length > 10 ? (
        <div className="list-tools">
          <FilterInput listKey={pk} placeholder={t("Filtrar VMs…")} />
        </div>
      ) : null}
      <BulkBar s={sel} busy={busy}>
        <button type="button" className="btn small" onClick={() => void bulk("start")}>
          {t("Iniciar")}
        </button>
        <button type="button" className="btn small" onClick={() => void bulk("stop")}>
          {t("Parar")}
        </button>
        <button type="button" className="btn small danger" onClick={() => void bulk("rm")}>
          {t("Remover")}
        </button>
      </BulkBar>
      <div className="panel table-wrap">
        <table className="grid">
          <thead>
            <tr>
              <SelectAll s={sel} keys={pg.items.map(vmKey)} />
              {heads.map((h, i, a) => (
                <th key={i} className={i === a.length - 1 ? "act" : i >= 3 && i <= 5 ? "num" : undefined}>
                  {h || <span className="sr">{t("Ações")}</span>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pg.items.map((vm) => {
              const st = VM_STATE[vm.state] || ["", vm.state];
              const running = vm.state === "running";
              const base = "/hosts/" + enc(vm.host) + "/labs/" + enc(vm.project) + "/vms/" + enc(vm.name);
              const u = running ? (S.stats[vm.host] || {})[vmStatKey(vm.project, vm.name)] : null;
              return (
                <tr
                  key={vmKey(vm)}
                  className={sel.has(vmKey(vm)) ? "selected" : undefined}
                  onContextMenu={(ev) =>
                    openMenu(
                      ev,
                      [
                        { label: t("Abrir o projeto"), onClick: () => void (location.hash = labHref(vm.host, vm.project)) },
                        { label: t("Ver as métricas"), onClick: () => void (location.hash = labHref(vm.host, vm.project, "metricas")) },
                        running && vm.vnc ? { label: t("Tela"), onClick: () => openVMScreen(base, vm.name) } : null,
                        "-",
                        running ? { label: t("Parar"), onClick: () => vmOp(vm, "stop") } : { label: vm.state === "missing" ? t("Criar") : t("Iniciar"), onClick: () => vmOp(vm, "start") },
                        "-",
                        vm.state !== "missing"
                          ? {
                              label: t("Remover…"),
                              danger: true,
                              onClick: async () => {
                                if (!(await confirm(t("Remover a VM \"{name}\"?", { name: vm.name }), t("A VM é desligada e o disco dela é apagado."), t("Remover"), true))) return;
                                await vmOp(vm, "rm");
                              },
                            }
                          : null,
                      ],
                      vm.name,
                    )
                  }
                >
                  <SelectCell s={sel} k={vmKey(vm)} label={vm.name} />
                  <td>
                    <div className="cname">
                      <a className="cname-link" href={labHref(vm.host, vm.project, "metricas")} title={t("Ver as métricas")}>
                        <span className={"dot " + st[0]} />
                        {vm.name}
                      </a>
                    </div>
                  </td>
                  <td>
                    <a href={labHref(vm.host, vm.project)}>{vm.project}</a>
                  </td>
                  <td className="small">
                    {st[1]}
                    {vm.accel === "tcg" && running ? <span className="muted">{t(" · sem aceleração")}</span> : null}
                  </td>
                  <VMUsageCells u={u} running={running} />
                  {lanCol ? <td className="mono">{vm.lan || "—"}</td> : null}
                  <td className="mono">
                    {vm.ports && vm.ports.length ? (
                      <div className="ports">
                        {vm.ports.map((raw) => (
                          <PortLabel key={raw} host={vm.host} port={raw.split(":")[2]} text={vmPorts([raw])[0]} />
                        ))}
                      </div>
                    ) : (
                      "—"
                    )}
                  </td>
                  <td className="mono">
                    <SSHCell vm={vm} base={base} />
                  </td>
                  <td className="act">
                    {running && vm.vnc ? (
                      <button className="btn small" title={t("Abre a tela da VM (VNC) para instalar ou usar o sistema")} onClick={() => openVMScreen(base, vm.name)}>
                        {t("Tela")}
                      </button>
                    ) : null}
                    <VMAccess host={vm.host} project={vm.project} vm={vm} base={base} />
                    {running ? op(vm, "stop", t("Parar")) : op(vm, "start", vm.state === "missing" ? t("Criar") : t("Iniciar"))}
                    {vm.state !== "missing" ? op(vm, "rm", t("Remover"), [t("Remover a VM \"{name}\"?", { name: vm.name }), t("A VM é desligada e o disco dela é apagado.")]) : null}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {pg.bar}
    </div>
  );
}

/* shared services */

const SHARED_NEW: [string, string, string, string][] = [
  ["postgres", "PostgreSQL", "gantry-postgres", "postgres"],
  ["mysql", "MySQL", "gantry-mysql", "mysql"],
  ["mariadb", "MariaDB", "gantry-mariadb", "mariadb"],
  ["redis", "Redis", "gantry-redis", "redis"],
  ["rabbitmq", "RabbitMQ", "gantry-rabbitmq", "rabbitmq"],
  ["mongo", "MongoDB", "gantry-mongo", "mongo"],
];

// newSharedDialog: a shared service is just a project from one of these
// templates on the machine, with its port published; it shows up by itself.
export function newSharedDialog(host: string) {
  formDialog({
    title: t("Novo serviço compartilhado"),
    intro: t("Cria um projeto só com o serviço, com a porta publicada nesta máquina. Depois, ao criar outros projetos, o assistente oferece usá-lo e cria um banco e um usuário para cada um."),
    submit: t("Continuar"),
    fields: [
      { id: "kind", label: t("Serviço"), type: "select", options: SHARED_NEW.map(([k, l]) => [k, l]), value: "postgres" },
      { id: "name", label: t("Nome do projeto"), value: "compartilhado-postgres", hint: t("Ex.: banco-casa, postgres-comum") },
    ],
    derive: (id, v, vals) => (id === "kind" && /^compartilhado-/.test(String(vals.name)) ? { name: "compartilhado-" + SHARED_NEW.find((x) => x[0] === v)![3] } : null),
    onSubmit: async (v) => {
      const it = SHARED_NEW.find((x) => x[0] === v.kind)!;
      const name = String(v.name || "compartilhado-" + it[3]).trim();
      location.hash = "#/modelos/" + it[2] + "/gerar?host=" + enc(host || "") + "&nome=" + enc(name);
    },
  });
}

export function SharedTab({ m }: { m: Machine }) {
  const st = useMachine((s) => s.shared[m.key]);
  useEffect(() => {
    if (!st || (!st.data && !st.loading) || Date.now() - (st.at || 0) > 30000) loadShared(m);
  }, [m.key]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div>
      <div className="banner info">
        <span className="grow">{t("Bancos e filas que já rodam nesta máquina, para outros projetos usarem. Qualquer PostgreSQL, MySQL/MariaDB, Redis, RabbitMQ ou MongoDB com a porta publicada entra aqui sozinho.")}</span>
        <button className="btn small primary" onClick={() => newSharedDialog(m.primary?.name ?? "")}>
          {t("Novo serviço compartilhado…")}
        </button>
      </div>
      {!st?.data ? (
        <Loading>{t("Procurando serviços…")}</Loading>
      ) : (
        <>
          {st.err ? <ErrorBox>{st.err}</ErrorBox> : null}
          {!st.data.length ? (
            <Empty>{t("Nenhum PostgreSQL, MySQL/MariaDB, Redis, RabbitMQ ou MongoDB rodando aqui.")}</Empty>
          ) : (
            <div className="panel">
              <table className="table">
                <thead>
                  <tr>
                    {[t("Serviço"), t("Tipo"), t("Porta na máquina"), t("Projeto"), t("Usado por")].map((h) => (
                      <th key={h}>{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {st.data.map((x) => {
                    const cons = Object.entries(x.consumers || {});
                    return (
                      <tr key={x.host + x.id}>
                        <td>
                          <span className="state">
                            <span className={"dot" + (x.running ? " on" : "")} />
                            {x.name}
                          </span>
                          <div className="muted small mono">{x.image}</div>
                        </td>
                        <td>
                          {x.title}
                          {m.hosts.length > 1 ? <div className="muted small">{RT_NAME[x.runtime ?? ""] || x.runtime || ""}</div> : null}
                        </td>
                        <td className="mono">{x.port ? String(x.port) : <span className="muted small">{t("não publicada")}</span>}</td>
                        <td>{x.lab ? <a href={labHref(x.host, x.lab)}>{x.lab}</a> : <span className="muted">{t("avulso")}</span>}</td>
                        <td>
                          {cons.length ? (
                            cons.map(([proj, u]) => (
                              <div key={proj}>
                                <a href={labHref(x.host, proj)}>{proj}</a>
                                {u.db ? <span className="muted small">{u.user && u.user !== u.db ? t(" · banco {db}, usuário {user}", { db: u.db, user: u.user }) : t(" · banco {db}", { db: u.db })}</span> : null}
                              </div>
                            ))
                          ) : (
                            <span className="muted small">{t("ninguém ainda")}</span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}

/* configuration */

const SOURCE: Record<string, string> = { config: t("arquivo de configuração"), detected: t("detectado automaticamente"), context: "docker context" };

export function ConfigTab({ m }: { m: Machine }) {
  useApp();
  const muted = m.hosts.filter((x) => alertsMuted(x.name));
  const p = m.primary;
  const kv = (k: string, v: React.ReactNode) => (
    <div>
      <span className="k">{k}</span>
      <span className="v">{v}</span>
    </div>
  );
  return (
    <div>
      {muted.length ? (
        <div className="banner info" style={{ marginBottom: 12 }}>
          <span className="grow">{t("Os alertas do Prometheus estão silenciados nesta máquina.")}</span>
          <button
            className="btn small"
            onClick={() => {
              setAlertsMuted(
                muted.map((x) => x.name),
                false,
              );
            }}
          >
            {t("Mostrar de novo")}
          </button>
        </div>
      ) : null}
      {m.hosts.map((hst) => {
        const info = hst.info || {};
        return (
          <div className="block" key={hst.name}>
            <div className="block-head">
              <h2 className="block-title">{hst.name}</h2>
              <span className={"badge " + (hst.ok ? "ok" : hst.idle ? "" : "err")}>{hst.ok ? t("conectado") : hst.idle ? t("não conectado") : t("sem conexão")}</span>
              <span className="grow" />
              {hst.source === "config" ? (
                <button className="btn small" onClick={() => gantry.openHostDialog(hst)}>
                  {t("Editar")}
                </button>
              ) : null}
              {hst.source === "config" && hst !== p ? (
                <button className="btn small danger" onClick={() => gantry.removeHost(hst.name)}>
                  {t("Remover")}
                </button>
              ) : null}
              {hst.source !== "config" ? (
                <button className="btn small" onClick={() => gantry.hideHost(hst)}>
                  {t("Ocultar")}
                </button>
              ) : null}
            </div>
            <div className="panel panel-pad kv">
              {kv(t("Endereço"), hst.url)}
              {hst.socket ? kv("Socket", hst.socket) : null}
              {kv(t("Origem"), SOURCE[hst.source] || hst.source)}
              {kv("Runtime", (hst.runtime || "—") + (hst.version ? " " + hst.version : ""))}
              {kv("Compose", hst.files ? hst.compose.join(" ") : t("indisponível neste tipo de host"))}
              {info.OperatingSystem ? kv(t("Sistema do engine"), info.OperatingSystem) : null}
            </div>
            {hst.kind === "ssh" && hst.ok ? <EnginesList hst={hst} /> : null}
          </div>
        );
      })}
      {p && (p.ok || m.local) && p.kind !== "tcp" ? <ImagesBlock host={p.name} /> : null}
      {p && p.ok ? <ObsBlock host={p.name} /> : null}
      {p && p.ok && p.agent ? <AgentBlock hst={p} /> : null}
    </div>
  );
}

// CreateMenu: the create button of a tab, with the ways to create that kind
// of thing there: one at a time (a form) or from a file.
type CreateWay = { title: string; text: string; href?: string; run?: () => void };
export function CreateMenu({ label, ways }: { label: string; ways: CreateWay[] }) {
  return (
    <details className="more-menu create-menu">
      <summary className="btn primary">{label} ▾</summary>
      <div className="more-list panel">
        {ways.map((w) =>
          w.href ? (
            <a key={w.title} href={w.href} onClick={(e) => (e.currentTarget.closest("details") as HTMLDetailsElement | null)?.removeAttribute("open")}>
              <b>{w.title}</b>
              <span>{w.text}</span>
            </a>
          ) : (
            <button
              key={w.title}
              type="button"
              onClick={(e) => {
                (e.currentTarget.closest("details") as HTMLDetailsElement | null)?.removeAttribute("open");
                w.run?.();
              }}
            >
              <b>{w.title}</b>
              <span>{w.text}</span>
            </button>
          ),
        )}
      </div>
    </details>
  );
}

// newProjHref: the create wizard of the machine, already on a kind and source.
export function newProjHref(m: Machine, q: Record<string, string>) {
  return (m.local ? "#/local" : "#/h/" + enc(m.primary!.name)) + "/novo?" + new URLSearchParams(q).toString();
}

function vmWays(m: Machine): CreateWay[] {
  return [
    { title: t("VM nova"), text: t("Sistema, tamanho, pacotes e script de inicialização, num formulário"), href: newProjHref(m, { tipo: "vm", origem: "proprio" }) },
    { title: t("De um modelo"), text: t("Uma VM pronta: Nginx, NixOS, labs de rede Cisco…"), href: newProjHref(m, { tipo: "vm", origem: "modelo" }) },
    { title: t("De um arquivo"), text: t("Um compose com x-vms, colado ou aberto do computador"), href: newProjHref(m, { tipo: "ctr", origem: "compose" }) },
    { title: t("Importar disco"), text: t("Um disco do VirtualBox, VMware, Hyper-V ou Proxmox (OVA, VMDK, VDI, VHD, raw)"), href: newProjHref(m, { tipo: "vm", origem: "disco" }) },
  ];
}
