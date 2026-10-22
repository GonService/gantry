// The page of a project: its containers and VMs, the compose file, the
// topology, metrics, shared services and backups.
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, hostHref, labHref, newCtrHref, useApp, type Host, type Machine } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { t } from "@/lib/i18n";
import { confirm, formDialog } from "@/components/dialog";
import { ErrorBox, Loading } from "@/components/ui";
import { AlertsBanner, AppsBar } from "@/views/shared/web";
import { ToolWarning } from "@/views/tools/ToolsTab";
import { useTemplates } from "@/views/templates/store";
import { DriftBanner, labBuilds, useDrift } from "./drift";
import { UpdatesBlock } from "./updates";
import { ContainersTable, VMsTable } from "./tables";
import { CablesPanel } from "./cables";
import { LabStates } from "./states";
import { LabServicesTab } from "./services";
import { BackupTab } from "./backup";
import { TopologyTab } from "./topology";
import { MetricsTab } from "./metrics";
import { FileTab } from "./file";
import type { Lab } from "./types";

export type LabProps = { host: string; lab: string; tab: string };

export function LabPage({ host, lab: id, tab: rtab }: LabProps) {
  const S = useApp();
  const hst = S.hosts.find((x) => x.name === host);
  if (!hst) return null;
  if (!hst.ok) return <ErrorBox>{hst.error || t("host indisponível")}</ErrorBox>;
  const ls = S.labs[host] as Lab[] | undefined;
  if (!ls) return <Loading />;
  const lab = ls.find((l) => l.id === id);
  if (!lab)
    return (
      <div className="empty">
        <h1>{t("Projeto não encontrado")}</h1>
        <p>{t("Ele pode ter sido removido.")}</p>
        <a className="btn" href={hostHref(host)}>
          {t("Ver o host")}
        </a>
      </div>
    );
  return <Page hst={hst} lab={lab} rtab={rtab} busy={S.busy[host + "/" + lab.id]} />;
}

export async function labOp(host: string, lab: Lab, op: string, label: string) {
  if (
    op === "down" &&
    !(await confirm(
      t('Remover o projeto "{name}"?', { name: lab.name }),
      t("Apaga os containers e as redes do projeto. Volumes nomeados e os arquivos são mantidos.") + (lab.vms && lab.vms.length ? t(" As VMs do projeto e os discos delas também são apagados.") : ""),
      t("Remover"),
      true,
    ))
  )
    return;
  const key = host + "/" + lab.id;
  gantry.markBusy(key, op);
  const finish = async () => {
    gantry.markBusy(key, null);
    await gantry.loadLabs(host);
  };
  try {
    const r = await api<{ id: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/" + op + "?job=1", { method: "POST" });
    gantry.openJob(r.id, label + " · " + lab.name, finish);
  } catch (e) {
    gantry.toast((e as Error).message, "err");
    await finish();
  }
}

// saveLabAsTemplate turns a project's compose file into a template.
async function saveLabAsTemplate(host: string, lab: Lab) {
  try {
    const f = await api<{ content: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/file");
    const yaml = f.content.replace(/^name:[^\n]*\n\n?/m, "");
    await api("/templates", { method: "POST", body: { name: lab.name, description: t("A partir do projeto {name} em {host}", { name: lab.name, host }), yaml } });
    useTemplates.setState({ list: null });
    gantry.toast(t('Modelo "{name}" salvo: aparece em Criar projeto. As senhas ficam no .env do projeto, não no modelo.', { name: lab.name }), "ok");
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

function exportLabDialog(host: string, lab: Lab) {
  formDialog({
    title: t("Exportar o projeto {name}", { name: lab.name }),
    intro: lab.vms?.length
      ? t("Gera um pacote .gantrylab com o compose, os arquivos da pasta e a configuração dos equipamentos; você escolhe onde salvar. Na outra máquina, use Importar pacote: o lab sobe igual (as imagens precisam estar cadastradas lá). Os dados dos volumes não vão (para isso, use Backup).")
      : t(
          "Gera um pacote .tar.gz com o compose e os arquivos da pasta do projeto; você escolhe onde salvar. Para levar a outra máquina, use Importar pacote lá. Os dados dos volumes e os discos das VMs não vão no pacote (para isso, use Backup).",
        ),
    submit: t("Escolher onde salvar…"),
    fields: [
      { id: "env", label: t("Incluir o .env (tem as senhas do projeto)"), type: "checkbox", value: false },
      ...(lab.vms?.length
        ? [
            { id: "nvram", label: t("Levar a configuração dos nós IOL (NVRAM): o lab sobe igual na outra máquina"), type: "checkbox" as const, value: true },
            { id: "write", label: t("Gravar a configuração dos nós rodando antes (write memory)"), type: "checkbox" as const, value: true },
            { id: "disks", label: t("Levar os discos das VMs (o pacote fica grande)"), type: "checkbox" as const, value: false },
          ]
        : []),
    ],
    onSubmit: async (v) => {
      const d = new Date();
      const p2 = (n: number) => String(n).padStart(2, "0");
      const stamp = d.getFullYear() + p2(d.getMonth() + 1) + p2(d.getDate()) + "-" + p2(d.getHours()) + p2(d.getMinutes());
      const labPack = !!(v.nvram || v.disks);
      const path = await gantry.pickPath({ kind: "salvar", title: t("Salvar o pacote de {name}", { name: lab.name }), dir: "~/Downloads", name: "gantry-" + lab.id + "-" + stamp + (labPack ? ".gantrylab" : ".tar.gz") });
      if (!path) throw new Error(t("Escolha onde salvar o pacote."));
      const r = await api<{ path: string; size: number; lab?: boolean; output?: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/export", {
        method: "POST",
        body: { env: !!v.env, path, nvram: !!v.nvram, disks: !!v.disks, write: !!v.write },
      });
      gantry.toast(t("Pacote salvo: {path} ({size})", { path: r.path, size: fmtBytes(r.size) }) + (r.lab ? " · " + t("com a configuração dos equipamentos") : ""), "ok");
    },
  });
}

function sendLabDialog(host: string, lab: Lab) {
  const mine = (gantry.machines() as Machine[]).find((m) => m.hosts.some((x) => x.name === host));
  const S = gantry.state();
  const targets = (S.hosts as Host[]).filter((x) => x.files && !(mine ? mine.hosts : []).some((y) => y.name === x.name) && x.name !== host);
  if (!targets.length) {
    gantry.toast(t("Adicione outra máquina (ssh://) para enviar o projeto para ela."), "err");
    return;
  }
  formDialog({
    title: t("Enviar {name} para outra máquina", { name: lab.name }),
    intro: t("Copia a pasta do projeto (compose e arquivos) para a máquina escolhida e sobe lá. Os dados dos volumes e os discos das VMs não vão: lá o projeto começa do zero."),
    submit: t("Enviar|projeto"),
    fields: [
      { id: "target", label: t("Máquina"), type: "select", options: targets.map((x) => [x.name, x.name + (x.ok ? "" : t(" (conecta ao enviar)"))] as [string, string]), value: targets[0].name },
      { id: "dir", label: t("Pasta lá"), placeholder: "~/gantry/" + lab.id },
      { id: "env", label: t("Levar o .env (senhas e portas)"), type: "checkbox", value: true },
      { id: "up", label: t("Subir lá depois de copiar"), type: "checkbox", value: true },
    ],
    onSubmit: async (v) => {
      const target = String(v.target);
      const r = await api<{ ok: boolean; error?: string; id: string; dir: string; job?: string; output?: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/copy", {
        method: "POST",
        body: { target, dir: v.dir, env: !!v.env, up: !!v.up, job: !!v.up },
      });
      if (r.output) gantry.showOutput(target + ": " + r.id, r);
      if (!r.ok) throw new Error(r.error || t("falhou"));
      gantry.toast(t("Projeto {id} em {target} ({dir})", { id: r.id, target, dir: r.dir }), "ok");
      if (r.job)
        gantry.openJob(r.job, t("Subir · {id} em {target}", { id: r.id, target }), async () => {
          await gantry.loadLabs(target);
        });
      await gantry.loadLabs(target);
      location.hash = labHref(target, r.id);
    },
  });
}

async function forgetLab(host: string, lab: Lab) {
  try {
    await api("/hosts/" + enc(host) + "/labs/" + enc(lab.id), { method: "DELETE" });
    location.hash = hostHref(host);
    await gantry.loadLabs(host);
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

function Head({ hst, lab, busy }: { hst: Host; lab: Lab; busy?: string }) {
  const builds = useDrift((s) => s[hst.name + "/" + lab.id]?.data?.builds) ?? labBuilds(hst.name, lab.id);
  const opBtn = (op: string, label: string, cls = "") => (
    <button className={"btn " + cls} disabled={!!busy || !hst.files} title={hst.files ? undefined : t("Indisponível neste tipo de host")} onClick={() => labOp(hst.name, lab, op, label)}>
      {busy === op ? (
        <>
          <span className="spin" />
          {label}…
        </>
      ) : (
        label
      )}
    </button>
  );
  const runtimes = lab.runtimes && lab.runtimes.length ? lab.runtimes : [hst.runtime || "docker"];
  const vms = lab.vms ?? [];
  return (
    <div className="page-head" id="lab-head">
      <div className="page-title">
        <nav className="crumbs" aria-label={t("Caminho")}>
          <a href={hostHref(hst.name)}>{hst.name}</a>
          <span aria-hidden="true">/</span>
          <span>{t("Projetos")}</span>
          {lab.workingDir ? (
            <>
              <span aria-hidden="true">/</span>
              <span className="mono">{(lab.files && lab.files[0]) || lab.workingDir}</span>
            </>
          ) : null}
        </nav>
        <h1>{lab.name}</h1>
        <div className="badges">
          {lab.loose ? <span className="badge">{t("fora de qualquer compose")}</span> : <span className="badge accent">compose</span>}
          <span className="badge">{t("{running} de {total} rodando", { running: lab.running, total: lab.total })}</span>
          {vms.length ? <span className="badge mixed">{vms.length + (vms.length > 1 ? " VMs" : " VM")}</span> : null}
          {vms.length && !lab.containers.length && !(lab.runtimes && lab.runtimes.length) ? null : (
            <span className={"badge" + (runtimes.length > 1 ? " mixed" : "")} title={runtimes.length > 1 ? t("Projeto misto: x-gantry divide os serviços entre engines") : undefined}>
              {runtimes.join(" + ")}
            </span>
          )}
        </div>
      </div>
      {lab.loose ? (
        <div className="actions">
          <a className="btn primary" href={newCtrHref(hst.name)}>
            {t("Novo container")}
          </a>
        </div>
      ) : (
        <div className="actions">
          {opBtn("up", t("Subir"), "primary")}
          {opBtn("stop", t("Parar"))}
          {opBtn("restart", t("Reiniciar"))}
          {opBtn("pull", t("Atualizar imagens"))}
          {builds.length ? (
            <span title={t("Constrói de novo as imagens de {names} (build:) e recria os containers (compose up --build --force-recreate). Use depois de mudar o código.", { names: builds.join(", ") })}>{opBtn("rebuild", t("Reconstruir"))}</span>
          ) : null}
          <button className="btn" title={t("Cria um modelo a partir do compose deste projeto")} onClick={() => saveLabAsTemplate(hst.name, lab)}>
            {t("Salvar como modelo")}
          </button>
          <button className="btn" disabled={!hst.files} title={t("Gera um pacote .tar.gz para levar o projeto a outra máquina")} onClick={() => exportLabDialog(hst.name, lab)}>
            {t("Exportar")}
          </button>
          <button className="btn" disabled={!hst.files} title={t("Copia o projeto para outra máquina e sobe lá")} onClick={() => sendLabDialog(hst.name, lab)}>
            {t("Enviar para…")}
          </button>
          {lab.total ? (
            opBtn("down", t("Remover"), "danger")
          ) : (
            <button className="btn" onClick={() => forgetLab(hst.name, lab)}>
              {t("Esquecer projeto")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function Page({ hst, lab, rtab, busy }: { hst: Host; lab: Lab; rtab: string; busy?: string }) {
  const m = (gantry.machines() as Machine[]).find((x) => x.hosts.some((y) => y.name === hst.name));
  const vms = lab.vms ?? [];
  // A lab with VMs groups its devices in sub-tabs (VMs, containers, cables,
  // states); "containers" is the address of the first one.
  const subs: [string, string, number?][] = [];
  if (vms.length && !lab.loose) {
    subs.push(["containers", t("Máquinas virtuais"), vms.length]);
    if (lab.containers.length) subs.push(["ctrs", "Containers", lab.containers.length]);
    if (hst.files) subs.push(["cabos", t("Cabos")], ["estados", t("Estados do lab")]);
  }
  let tab = lab.loose ? (rtab === "metricas" ? "metricas" : "containers") : rtab || "containers";
  if (!subs.some(([k]) => k === tab) && ["ctrs", "cabos", "estados"].includes(tab)) tab = "containers";
  const sub = subs.some(([k]) => k === tab) ? tab : "";
  const tabs: [string, string][] = lab.loose
    ? [
        ["containers", "Containers"],
        ["metricas", t("Métricas")],
      ]
    : [
        ["containers", subs.length ? t("Dispositivos") : "Containers"],
        ["arquivo", t("Arquivo")],
        ["topologia", t("Topologia")],
        ["metricas", t("Métricas")],
        ...(hst.files
          ? ([
              ["servicos", t("Serviços")],
              ["backup", "Backup"],
            ] as [string, string][])
          : []),
      ];
  let body: React.ReactNode;
  if (tab === "metricas") body = <MetricsTab hst={hst} lab={lab} />;
  else if (tab === "topologia") body = <TopologyTab key={hst.name + "/" + lab.id} hst={hst} lab={lab} />;
  else if (tab === "arquivo") body = <FileTab key={hst.name + "/" + lab.id} hst={hst} lab={lab} />;
  else if (tab === "backup") body = <BackupTab hst={hst} lab={lab} busy={busy} />;
  else if (tab === "servicos") body = <LabServicesTab host={hst.name} lab={lab} />;
  else if (subs.length)
    body = (
      <>
        <div className="seg seg-sub" role="tablist" aria-label={t("Dispositivos do projeto")}>
          {subs.map(([k, l, n]) => (
            <a key={k} className={"seg-btn" + (k === sub ? " sel" : "")} role="tab" aria-selected={k === sub} href={labHref(hst.name, lab.id, k)}>
              {l}
              {n ? <span className="tab-count">{String(n)}</span> : null}
            </a>
          ))}
        </div>
        {sub === "ctrs" ? (
          <>
            <ContainersTable hst={hst} lab={lab} />
            {hst.files ? <UpdatesBlock host={hst.name} lab={lab.id} /> : null}
          </>
        ) : sub === "cabos" ? (
          <CablesPanel host={hst.name} lab={lab} />
        ) : sub === "estados" ? (
          <LabStates key={hst.name + "/" + lab.id} host={hst.name} lab={lab} onChange={() => void gantry.loadLabs(hst.name)} />
        ) : (
          <VMsTable hst={hst} lab={lab} />
        )}
      </>
    );
  else
    body = (
      <>
        {vms.length ? <VMsTable hst={hst} lab={lab} /> : null}
        <ContainersTable hst={hst} lab={lab} />
        {!vms.length && !lab.loose && hst.files ? <CablesPanel host={hst.name} lab={lab} quiet /> : null}
        {!lab.loose && hst.files ? <UpdatesBlock host={hst.name} lab={lab.id} /> : null}
      </>
    );
  return (
    <div>
      <Head hst={hst} lab={lab} busy={busy} />
      <AppsBar host={hst.name} lab={lab.id} loose={lab.loose} containers={lab.containers} />
      {lab.loose ? null : <AlertsBanner hosts={[hst.name]} lab={lab.id} />}
      {!lab.loose && hst.files ? <DriftBanner host={hst.name} lab={lab} /> : null}
      {!lab.loose && hst.files && m && m.primary && m.primary.kind !== "tcp" ? (
        <ToolWarning mkey={m.key} local={m.local} host={m.primary.name} needs={["compose"].concat(vms.length ? ["qemu", "iso", "download"] : [])} href={(m.local ? "#/local" : hostHref(m.primary.name)) + "/t/ferramentas"} />
      ) : null}
      <div className="tabs" role="tablist" aria-label={t("Seções do projeto")}>

        {tabs.map(([k, l]) => (
          <a key={k} className="tab" role="tab" href={labHref(hst.name, lab.id, k)} aria-selected={tab === k || (k === "containers" && !!sub)}>
            {l}
          </a>
        ))}
      </div>
      {body}
    </div>
  );
}
