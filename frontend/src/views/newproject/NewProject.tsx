// "Criar projeto": a short wizard that always ends in a compose file of the
// project (and, by default, a template in the library), so every project
// follows the same format.
//   1. what: containers, a VM, or both
//   2. containers: engine, and a template, a pasted compose or run commands
//      VM: a template or an own setup (system, size, ports, startup script, ISO)
//   3. review the YAML, fill the variables, generate
import { newCtrHref } from "@/lib/app";
import { VMCloudInit } from "./cloudinit";
import { NeedImage } from "@/views/shared/vmimages";
import { useEffect, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { CodeEditor, FolderInput, useDebounced } from "@/components/editor";
import { Loading, ErrorBox } from "@/components/ui";
import { ToolWarning } from "@/views/tools/ToolsTab";
import { checkYAML, createLab, envText, loadTemplates, slug, useTemplates, type Check, type Target, type Template } from "@/views/templates/store";
import { CheckSummary } from "@/views/templates/CheckSummary";
import { useShared, VarFields } from "@/views/templates/shared";
import { deleteTemplate, pickTextFile } from "@/views/templates/TemplatesPage";
import { useMachine } from "@/views/machine/store";
import { openDirDialog } from "@/views/machine/tabs";
import { CLOUD_INIT_SAMPLE, NIXOS_IMAGE, NIXOS_SAMPLE, VM_SNIPPETS, VM_SYSTEMS, fixPorts, isAppliance, isProgram, newVM, portWarning, vmFiles, vmYAML, type PortInfo, type PortNote, type VMSetup } from "./vm";

type Engine = { label: string; runtime: string; host: string; rt?: string; ok: boolean };
type HostRef = { name: string; ok: boolean; files?: boolean };

export type NewProjectProps = {
  mkey: string;
  name: string;
  local: boolean;
  href: string;
  toolsHref: string;
  primary: string;
  tcp: boolean;
  hosts: HostRef[];
  others: Target[];
  // Opened from a tab of the machine: what to create, from where, and on
  // which engine (tipo, origem and engine in the link).
  start?: { kind?: string; source?: string; engine?: string };
};

type Kind = "" | "ctr" | "vm" | "both";
type Converted = { yaml: string; services: string[]; warnings: string[] };

type St = {
  step: 1 | 2 | 3;
  kind: Kind;
  engine: Engine | null;
  ctrSource: "modelo" | "compose" | "run";
  ctrTpl: string;
  compose: string;
  commands: string;
  converted: Converted | null;
  vmSource: "modelo" | "proprio";
  vmTpl: string;
  vm: VMSetup;
  bothSource: "" | "pronto" | "montar";
  bothTpl: string;
  files: Record<string, string>;
  fromTpl: Template | null;
  yaml: string;
  name: string;
  dir: string;
  vals: Record<string, string>;
  up: boolean;
  save: boolean;
  keychain: boolean;
  tplName: string;
  tplTouched: boolean;
  remoteHost: string;
  autoDisk: boolean; // opened as "import a disk": ask for the disk at once
};

const engKey = (e: Engine) => e.host + (e.rt ? "@" + e.rt : "");
const hasCtr = (tpl: Template) => /^services:/m.test(tpl.yaml);
const hasVM = (tpl: Template) => /^x-vms:/m.test(tpl.yaml);

function engLabel(local: boolean, e: Engine) {
  const rt = gantry.rtName(e.runtime);
  if (!local) return rt || e.label;
  return !e.label || e.label.toLowerCase() === e.runtime ? rt : e.label;
}

// useEngines: the engines of the machine that take project files.
function useEngines(mkey: string, primary: string): Engine[] | null {
  const [list, setList] = useState<Engine[] | null>(() => (useMachine.getState().ov[mkey]?.data?.engines as Engine[] | undefined) ?? null);
  useEffect(() => {
    if (list || !primary) return;
    api<{ engines: Engine[] }>("/hosts/" + enc(primary) + "/overview")
      .then((d) => setList(d.engines))
      .catch(() => setList([]));
  }, [primary]); // eslint-disable-line react-hooks/exhaustive-deps
  return list ? list.filter((e) => e.ok && gantry.hostByName(e.host)?.files) : null;
}

export function NewProject(p: NewProjectProps) {
  const pre = p.start ?? {};
  const preKind: Kind = pre.kind === "ctr" || pre.kind === "vm" || pre.kind === "both" ? pre.kind : "";
  const [st, setSt] = useState<St>(() => ({
    step: preKind ? 2 : 1,
    kind: preKind,
    engine: null,
    ctrSource: pre.source === "compose" || pre.source === "run" ? pre.source : "modelo",
    ctrTpl: "",
    compose: "",
    commands: "",
    converted: null,
    vmSource: pre.source === "proprio" || pre.source === "disco" ? "proprio" : "modelo",
    vmTpl: "",
    vm: pre.source === "disco" ? { ...newVM(), os: "image" } : newVM(),
    bothSource: "",
    bothTpl: "",
    files: {},
    fromTpl: null,
    yaml: "",
    name: "",
    dir: "",
    vals: {},
    up: true,
    save: true,
    keychain: true,
    tplName: "",
    tplTouched: false,
    remoteHost: "",
    autoDisk: pre.source === "disco" && preKind === "vm",
  }));
  const set = (patch: Partial<St>) => setSt((s) => ({ ...s, ...patch }));
  const list = useTemplates((s) => s.list);
  useEffect(() => {
    if (!list) loadTemplates();
  }, [list]);
  const engines = useEngines(p.mkey, p.primary);
  // The chosen engine, or the first one (podman for ready templates).
  const engine = engines && (st.engine && engines.some((e) => engKey(e) === engKey(st.engine!)) ? st.engine : null);
  const defEngine = engines ? (st.kind === "both" ? engines.find((e) => e.runtime === "podman") || engines[0] : engines[0]) ?? null : null;
  const eng = engine ?? defEngine;
  // The engine of the tab it was opened from.
  useEffect(() => {
    if (!pre.engine || st.engine || !engines) return;
    const e = engines.find((x) => engKey(x) === pre.engine || x.host === pre.engine);
    if (e) set({ engine: e });
  }, [engines]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    document.getElementById("view")?.scrollTo(0, 0);
  }, [st.step]);

  const steps: [string, 1 | 2 | 3][] = [
    [t("Tipo"), 1],
    [t("Configuração"), 2],
    [t("Revisar e gerar"), 3],
  ];
  const needs = st.kind === "ctr" ? ["engine", "compose"] : ["qemu", "iso", "download"].concat(st.kind === "both" ? ["compose"] : []);
  return (
    <div className="np">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={p.href}>{p.name}</a>
            <span aria-hidden="true">/</span>
            <span>{t("Projetos")}</span>
          </nav>
          <h1>{t("Criar projeto")}</h1>
        </div>
        <div className="actions">
          <a className="btn btn-ghost" href={p.href}>
            {t("Cancelar")}
          </a>
        </div>
      </div>
      <ol className="np-steps">
        {steps.map(([l, n]) => (
          <li key={n} className={n === st.step ? "cur" : n < st.step ? "done" : ""}>
            {n < st.step ? (
              <button type="button" onClick={() => set({ step: n })}>
                {l}
              </button>
            ) : (
              <span>{l}</span>
            )}
          </li>
        ))}
      </ol>
      {st.kind && st.step > 1 && !st.remoteHost && !p.tcp ? <ToolWarning mkey={p.mkey} local={p.local} host={p.primary} needs={needs} href={p.toolsHref} /> : null}
      {st.step === 1 ? (
        <KindStep p={p} st={st} set={set} />
      ) : st.step === 2 ? (
        <ConfigStep p={p} st={st} set={set} setSt={setSt} list={list} engines={engines} eng={eng} />
      ) : (
        <Review p={p} st={st} set={set} eng={eng} />
      )}
    </div>
  );
}

type StepProps = { p: NewProjectProps; st: St; set: (x: Partial<St>) => void };

/* step 1 */

function KindStep({ p, st, set }: StepProps) {
  const files = p.hosts.find((x) => x.ok && x.files);
  const card = (kind: Kind, title: string, text: string, icon: string) => (
    <button type="button" className={"panel np-kind" + (st.kind === kind ? " sel" : "")} onClick={() => set({ kind, step: 2 })}>
      <span className="np-icon" aria-hidden="true">
        {icon}
      </span>
      <b>{title}</b>
      <span className="muted small">{text}</span>
    </button>
  );
  return (
    <div>
      {files ? null : (
        <div className="flash flash-warn">
          {p.local
            ? t("Nenhum engine está rodando nesta máquina. Inicie um na aba Engines: o Gantry usa a conexão com ele para gravar os arquivos do projeto e rodar o compose e as VMs.")
            : t("O host ainda não está conectado.")}
        </div>
      )}
      <p className="muted">{t("O que este projeto vai ter?")}</p>
      <div className="np-kinds">
        {card("ctr", t("Containers"), t("Um ou mais serviços em Docker, Podman ou containerd: de um modelo, de um docker-compose ou de comandos docker run."), "▣")}
        {card("vm", t("Máquina virtual"), t("Uma VM no QEMU desta máquina: de um modelo ou com o seu setup (sistema, tamanho, script de inicialização, ISO)."), "🖥")}
        {card("both", t("VM + containers"), t("Os dois no mesmo projeto, subindo e parando juntos."), "⧉")}
      </div>
      {files ? (
        <p className="muted small np-alt">
          {t("Ou")} <a href={newCtrHref(files.name)}>{t("crie um container avulso")}</a> {t("(sem projeto),")}{" "}
          <a
            href="#"
            onClick={(e) => {
              e.preventDefault();
              gantry.gitLabDialog(gantry.hostByName(files.name)!);
            }}
          >
            {t("traga um repositório git")}
          </a>{" "}
          {t("ou")}{" "}
          <a
            href="#"
            onClick={(e) => {
              e.preventDefault();
              const hx = gantry.hostByName(files.name)!;
              openDirDialog(hx.name, hx.compose);
            }}
          >
            {t("abra uma pasta com compose")}
          </a>
          .
        </p>
      ) : null}
    </div>
  );
}

/* step 2 */

// TplCard is a template to pick. The user's own ones can be deleted here.
function TplCard({ tpl, sel, pick }: { tpl: Template; sel: boolean; pick: () => void }) {
  return (
    <div
      className={"np-tpl" + (sel ? " sel" : "")}
      role="button"
      tabIndex={0}
      onClick={pick}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          pick();
        }
      }}
    >
      <span className="np-tpl-top">
        <b>{tpl.name}</b>
        {tpl.builtin ? null : <span className="badge accent">{t("seu")}</span>}
        {tpl.builtin ? null : (
          <button
            type="button"
            className="icon-btn"
            title={t("Excluir este modelo")}
            aria-label={t("Excluir o modelo {name}", { name: tpl.name })}
            onClick={(e) => {
              e.stopPropagation();
              deleteTemplate(tpl);
            }}
          >
            ×
          </button>
        )}
      </span>
      <span className="muted small">{tpl.description || tpl.category || ""}</span>
    </div>
  );
}

function EngineTabs({ p, engines, eng, set, label = "Engine" }: { label?: string; p: NewProjectProps; engines: Engine[]; eng: Engine | null; set: (x: Partial<St>) => void }) {
  return (
    <div className="field">
      <label>{label}</label>
      <div className="eng-tabs">
        {engines.map((e) => (
          <button key={engKey(e)} type="button" className={"eng-tab" + (eng && engKey(e) === engKey(eng) ? " sel" : "")} onClick={() => set({ engine: e })}>
            {engLabel(p.local, e)}
          </button>
        ))}
      </div>
    </div>
  );
}

function Seg<K extends string>({ value, options, onChange }: { value: K; options: [K, string][]; onChange: (k: K) => void }) {
  return (
    <div className="seg seg-sub">
      {options.map(([k, l]) => (
        <button key={k} type="button" className={"seg-btn" + (value === k ? " sel" : "")} onClick={() => onChange(k)}>
          {l}
        </button>
      ))}
    </div>
  );
}

// target: the Gantry host the project goes to, and the x-gantry engine
// when that engine is not a host of its own.
function target(p: NewProjectProps, st: St, eng: Engine | null, ignoreRemote = false): { host: string; runtime: string } {
  if (st.remoteHost && !ignoreRemote) return { host: st.remoteHost, runtime: "" };
  if (st.kind !== "vm" && eng) {
    const ready = st.kind === "both" && st.bothSource === "pronto";
    return { host: eng.host, runtime: ready ? "" : eng.rt || "" };
  }
  const files = p.hosts.find((x) => x.ok && x.files);
  return { host: files ? files.name : p.primary, runtime: "" };
}

// buildYAML puts together the compose of the project from step 2.
async function buildYAML(p: NewProjectProps, st: St, list: Template[], eng: Engine | null): Promise<Partial<St>> {
  const out: Partial<St> = { files: {}, step: 3 };
  const byId = (id: string) => list.find((x) => x.id === id);
  if (st.kind === "both" && st.bothSource === "pronto") {
    const tpl = byId(st.bothTpl);
    if (!tpl) throw new Error(t("Escolha um modelo."));
    const vals = { ...st.vals };
    if (/\$\{ENGINE[:}-]/.test(tpl.yaml) && eng) vals.ENGINE = eng.rt || eng.runtime;
    return { ...out, yaml: tpl.yaml, files: { ...(tpl.files || {}) }, fromTpl: tpl, tplName: st.tplName || tpl.name, vals, name: st.name || slug(tpl.name) || "projeto", save: false };
  }
  const parts: string[] = [];
  const files: Record<string, string> = {};
  let fromTpl: Template | null = null;
  let tplName = st.tplName;
  let converted = st.converted;
  if (st.kind !== "vm") {
    let base = "";
    if (st.ctrSource === "modelo") {
      const tpl = byId(st.ctrTpl);
      if (!tpl) throw new Error(t("Escolha um modelo de containers."));
      base = tpl.yaml;
      tplName = tplName || tpl.name;
      fromTpl = tpl;
      Object.assign(files, tpl.files || {});
    } else if (st.ctrSource === "compose") {
      if (!st.compose.trim()) throw new Error(t("Cole o docker-compose."));
      base = st.compose.replace(/^name:[^\n]*\n/m, "").replace(/^version:[^\n]*\n/m, "");
    } else {
      if (!converted) converted = await api<Converted>("/convert", { method: "POST", body: { commands: st.commands } });
      base = converted.yaml;
    }
    const tgt = target(p, st, eng);
    if (tgt.runtime && !/^x-gantry:/m.test(base)) parts.push("x-gantry:\n  runtime: " + tgt.runtime + "\n");
    parts.push(base.replace(/\s*$/, "\n"));
  }
  if (st.kind !== "ctr") {
    if (st.vmSource === "modelo") {
      const tpl = byId(st.vmTpl);
      if (!tpl) throw new Error(t("Escolha um modelo de VM."));
      parts.push(
        tpl.yaml
          .split("\n")
          .filter((l) => !/^#/.test(l))
          .join("\n")
          .replace(/^\s+/, ""),
      );
      Object.assign(files, tpl.files || {});
      if (st.kind === "vm") {
        fromTpl = tpl;
        tplName = tplName || tpl.name;
      }
    } else {
      parts.push(vmYAML(st.vm));
      Object.assign(files, vmFiles(st.vm));
      if (st.kind === "vm") fromTpl = null;
    }
  }
  if (st.kind === "both") fromTpl = null;
  let name = st.name;
  if (!name) {
    const guess = fromTpl ? fromTpl.name : st.kind === "vm" ? st.vm.name : converted?.services[0] || "projeto";
    name = slug(guess) || "projeto";
  }
  return { ...out, yaml: parts.join("\n"), files, fromTpl, tplName, converted, name, save: !fromTpl };
}

function ConfigStep({
  p,
  st,
  set,
  setSt,
  list,
  engines,
  eng,
}: StepProps & { setSt: React.Dispatch<React.SetStateAction<St>>; list: Template[] | null; engines: Engine[] | null; eng: Engine | null }) {
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const ready = (list ?? []).filter((tpl) => hasCtr(tpl) && hasVM(tpl));
  const bothSource = st.bothSource || (ready.length ? "pronto" : "montar");
  return (
    <div className="np-config">
      {st.kind === "both" ? (
        <Seg
          value={bothSource}
          options={[
            ["pronto", t("Modelo pronto")],
            ["montar", t("Montar: containers + VM")],
          ]}
          onChange={(k) => set({ bothSource: k })}
        />
      ) : null}
      {st.kind === "both" && bothSource === "pronto" ? (
        <section className="panel panel-pad np-sec">
          <h2>{t("Modelos com VM e containers")}</h2>
          {engines?.length ? <EngineTabs label={t("Engine dos containers")} p={p} engines={engines} eng={eng} set={set} /> : null}
          <div className="np-tpls">
            {ready.map((tpl) => (
              <TplCard key={tpl.id} tpl={tpl} sel={st.bothTpl === tpl.id} pick={() => set({ bothTpl: tpl.id })} />
            ))}
          </div>
        </section>
      ) : (
        <>
          {st.kind !== "vm" ? <Containers p={p} st={st} set={set} list={list} engines={engines} eng={eng} /> : null}
          {st.kind !== "ctr" ? <VMSection p={p} st={st} set={set} setSt={setSt} list={list} eng={eng} /> : null}
        </>
      )}
      {err ? <p className="flash flash-err">{err}</p> : null}
      <div className="cf-actions">
        <button className="btn btn-ghost" onClick={() => set({ step: 1 })}>
          {t("Voltar")}
        </button>
        <button
          className="btn primary"
          disabled={busy}
          onClick={async () => {
            setErr("");
            setBusy(true);
            try {
              const cur = { ...st, bothSource, engine: eng };
              set({ ...(await buildYAML(p, cur, list ?? [], eng)), bothSource, engine: eng });
            } catch (x) {
              setErr((x as Error).message);
            }
            setBusy(false);
          }}
        >
          {t("Continuar")}
        </button>
      </div>
    </div>
  );
}

function Containers({ p, st, set, list, engines, eng }: StepProps & { list: Template[] | null; engines: Engine[] | null; eng: Engine | null }) {
  if (!engines)
    return (
      <section className="panel panel-pad np-sec">
        <h2>Containers</h2>
        <Loading>{t("Procurando os engines…")}</Loading>
      </section>
    );
  if (!engines.length)
    return (
      <section className="panel panel-pad np-sec">
        <h2>Containers</h2>
        <ErrorBox>{t("Nenhum engine de containers respondeu nesta máquina.")}</ErrorBox>
      </section>
    );
  return (
    <section className="panel panel-pad np-sec">
      <h2>Containers</h2>
      <EngineTabs p={p} engines={engines} eng={eng} set={set} />
      <Seg
        value={st.ctrSource}
        options={[
          ["modelo", t("Modelo")],
          ["compose", t("Colar docker-compose")],
          ["run", t("Comando docker/podman/nerdctl")],
        ]}
        onChange={(k) => set({ ctrSource: k })}
      />
      {st.ctrSource === "modelo" ? (
        <>
          {!list ? <Loading>{t("Carregando modelos…")}</Loading> : null}
          <div className="np-tpls">
            {(list ?? [])
              .filter((tpl) => hasCtr(tpl) && !hasVM(tpl))
              .map((tpl) => (
                <TplCard key={tpl.id} tpl={tpl} sel={st.ctrTpl === tpl.id} pick={() => set({ ctrTpl: tpl.id })} />
              ))}
          </div>
        </>
      ) : st.ctrSource === "compose" ? (
        <>
          <div className="field">
            <label>docker-compose.yaml</label>
            <textarea
              className="code-area"
              rows={14}
              spellCheck={false}
              placeholder={"services:\n  web:\n    image: nginx:alpine\n    ports: [\"8080:80\"]"}
              value={st.compose}
              onChange={(e) => set({ compose: e.target.value })}
            />
            <p className="hint">{t("Cole o compose inteiro. Senhas e valores que mudam podem virar variáveis: troque por ${NOME} e preencha no próximo passo.")}</p>
          </div>
          <button
            type="button"
            className="btn small"
            onClick={async () => {
              const f = await pickTextFile(".yaml,.yml");
              if (f) set({ compose: await f.text() });
            }}
          >
            {t("Abrir arquivo…")}
          </button>
        </>
      ) : (
        <>
          <div className="field">
            <label>{t("Comandos")}</label>
            <textarea
              className="code-area"
              rows={8}
              spellCheck={false}
              placeholder={"docker run -d --name web -p 8080:80 -v dados:/usr/share/nginx/html nginx:alpine\npodman run -d --name cache redis:7"}
              value={st.commands}
              onChange={(e) => set({ commands: e.target.value, converted: null })}
            />
            <p className="hint">{t("Um comando por linha (pode quebrar com \\). Portas, volumes, variáveis, rede, limites, reinício e o comando da imagem viram o compose.")}</p>
          </div>
          <button
            type="button"
            className="btn small"
            onClick={async () => {
              try {
                set({ converted: await api<Converted>("/convert", { method: "POST", body: { commands: st.commands } }) });
              } catch (e) {
                gantry.toast((e as Error).message, "err");
                set({ converted: null });
              }
            }}
          >
            {t("Converter")}
          </button>
          {st.converted ? (
            <div>
              <p className="small" style={{ margin: "8px 0" }}>
                {t("Viram os serviços:")} <b>{st.converted.services.join(", ")}</b>. {t("O YAML aparece no próximo passo para revisar.")}
              </p>
              {st.converted.warnings.map((w, i) => (
                <p key={i} className="flash flash-warn" style={{ margin: "4px 0" }}>
                  {w}
                </p>
              ))}
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}

function VMSection({ p, st, set, setSt, list, eng }: StepProps & { setSt: React.Dispatch<React.SetStateAction<St>>; list: Template[] | null; eng: Engine | null }) {
  const v = st.vm;
  const setV = (patch: Partial<VMSetup>) => setSt((s) => ({ ...s, vm: { ...s.vm, ...patch } }));
  const inp = (k: keyof VMSetup, ph: string, extra: React.InputHTMLAttributes<HTMLInputElement> = {}) => (
    <input placeholder={ph} spellCheck={false} value={String(v[k] ?? "")} onChange={(e) => setV({ [k]: e.target.value })} {...extra} />
  );
  const field = (label: string, el: React.ReactNode, hint?: React.ReactNode) => (
    <div className="field">
      <label>{label}</label>
      {el}
      {hint ? <p className="hint">{hint}</p> : null}
    </div>
  );
  // A disk from another hypervisor, picked on this computer, becomes a qcow2
  // on the target machine and the image of the VM.
  const importDisk = async () => {
    const tgt = target(p, st, eng);
    const path = await gantry.pickPath({ kind: "abrir", title: t("Disco para importar (OVA, VMDK, VDI, VHD, VHDX, raw)"), dir: "~" });
    if (!path) return;
    const name =
      (
        path
          .split(/[\\/]/)
          .pop()!
          .replace(/\.[^.]+$/, "")
          .toLowerCase()
          .replace(/[^a-z0-9._-]+/g, "-")
          .replace(/^[^a-z0-9]+/, "") || "disco"
      ).slice(0, 60);
    try {
      const r = await api<{ id: string; path: string }>("/hosts/" + enc(tgt.host) + "/discos/importar", { method: "POST", body: { path, name } });
      gantry.openJob(r.id, t("Importar disco {name}", { name }), (res) => {
        if (!res.ok) return;
        setV({ image: r.path, tela: true, os: "image" });
        gantry.toast(t("Disco pronto: {path}", { path: r.path }), "ok");
      });
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };  useEffect(() => {
    if (!st.autoDisk) return;
    set({ autoDisk: false });
    void importDisk();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const iso = v.os === "iso";
  return (
    <section className="panel panel-pad np-sec">
      <h2>{t("Máquina virtual")}</h2>
      <Seg
        value={st.vmSource}
        options={[
          ["modelo", t("Modelo")],
          ["proprio", t("Setup próprio")],
        ]}
        onChange={(k) => set({ vmSource: k })}
      />
      {st.vmSource === "modelo" ? (
        <>
          <div className="np-tpls">
            {(list ?? [])
              .filter((tpl) => hasVM(tpl) && !hasCtr(tpl))
              .map((tpl) => (
                <TplCard key={tpl.id} tpl={tpl} sel={st.vmTpl === tpl.id} pick={() => set({ vmTpl: tpl.id })} />
              ))}
          </div>
          {st.vmTpl ? <NeedImage host={target(p, st, eng).host} yaml={(list ?? []).find((x) => x.id === st.vmTpl)?.yaml ?? ""} /> : null}
          <p className="hint">{t("Quer mudar sistema, tamanho ou o que roda no primeiro boot? Use Setup próprio.")}</p>
        </>
      ) : (
        <>
          <div className="grid3">
            {field(t("Nome da VM"), inp("name", "vm"))}
            {field(
              t("Sistema"),
              <select
                value={v.os}
                onChange={(e) => {
                  const os = e.target.value;
                  // The memory each appliance needs (the profile of the image).
                  const mem: Record<string, string> = { "cisco-iosvl2": "1g", "cisco-iosv": "512m", "cisco-csr1000v": "4g", "cisco-iol": "512m", "cisco-iol-l2": "768m", "cisco-c7200": "256m", "cisco-c3725": "256m", "cisco-c2691": "256m" };
                  setV(mem[os] ? { os, memory: mem[os] } : { os });
                }}
              >
                {VM_SYSTEMS.map(([k, l]) => (
                  <option key={k} value={k}>
                    {l}
                  </option>
                ))}
              </select>,
            )}
            {v.os === "image" ? (
              field(
                t("Imagem (URL ou caminho)"),
                <div className="input-pick">
                  {inp("image", t("https://…/imagem.qcow2"))}
                  <button type="button" className="btn small" title={t("Um disco do VirtualBox, VMware, Hyper-V ou Proxmox (OVA, VMDK, VDI, VHD/VHDX, raw), convertido para qcow2 na máquina")} onClick={importDisk}>
                    {t("Importar disco…")}
                  </button>
                </div>,
                v.tela ? t("Disco importado: a VM abre com tela (VNC), para entrar pelo sistema dele.") : t("Uma imagem cloud com cloud-init, ou um disco importado de outro hipervisor."),
              )
            ) : iso ? (
              field(t("ISO (URL ou caminho no host)"), inp("iso", t("~/isos/instalador.iso")), t("Baixada uma vez e guardada em cache."))
            ) : (
              <div />
            )}
          </div>
          {isProgram(v.os) ? (
            <div className="grid3">{field(t("Memória"), inp("memory", "512m"))}</div>
          ) : (
            <div className="grid3">
              {field("CPUs", inp("cpus", "2", { inputMode: "numeric" }))}
              {field(t("Memória"), inp("memory", "2g"))}
              {field(t("Disco"), inp("disk", "20G"))}
            </div>
          )}
          {isAppliance(v.os) ? null : (
            <>
              {field(t("Portas"), inp("ports", "8080:80, 2222:22"), t("porta do host:porta da VM, separadas por vírgula. O SSH do Gantry já é publicado sozinho."))}
              <label className="check">
                <input type="checkbox" checked={!!v.lan} onChange={(e) => setV({ lan: e.target.checked })} /> {t("Rede do projeto: a VM ganha um IP fixo, e os containers e as outras VMs do projeto a acessam pelo nome")}
              </label>
              {iso || v.tela ? null : (
                <label className="check">
                  <input type="checkbox" checked={!!v.metrics} onChange={(e) => setV({ metrics: e.target.checked })} /> {t("Métricas de dentro da VM: instala o node_exporter e mostra CPU, memória, disco e rede da VM na aba Métricas")}
                </label>
              )}
            </>
          )}
          {isAppliance(v.os) ? (
            <>
              <NeedImage host={target(p, st, eng).host} yaml={v.os} />
              {field(t("Portas de rede (cabos)"), inp("ports2", "4", { inputMode: "numeric" }), t("Quantas portas o equipamento tem. Elas começam sem cabo; ligue-as em Cabos, na página do projeto, a outras VMs."))}
              <div className="banner info">
                <span className="grow">
                  {/^cisco-c\d/.test(v.os)
                    ? t("Roteador Dynamips: o IOS clássico roda emulado como um programa no host (Linux ou Mac, sem KVM; instale o dynamips). A configuração é pelo botão Console. Depois de subir, calcule o idle-PC em Gerenciar para o roteador não usar um núcleo inteiro.")
                    : /^cisco-iol/.test(v.os)
                    ? t("Nó IOL: o .bin roda direto como um programa no host (Linux x86_64, sem KVM), muito mais leve que uma VM. A configuração é pelo botão Console. Se a imagem pedir licença, informe o arquivo iourc em Configurações › Geral › Imagens de VM.")
                    : t("Equipamento de rede: sobe sem cloud-init e sem a placa de gerência do Gantry. A configuração é pelo botão Console (serial), como num equipamento de verdade. Precisa de um host Linux x86 com KVM.")}
                </span>
              </div>
            </>
          ) : v.os === NIXOS_IMAGE ? (
            <>
              <NeedImage host={target(p, st, eng).host} yaml={NIXOS_IMAGE} />
              <div className="field">
                <label>{t("Módulo NixOS ({file})", { file: (v.name || "vm") + ".nix" })}</label>
                <textarea className="code-area" rows={12} spellCheck={false} value={v.nixModule ?? NIXOS_SAMPLE} onChange={(e) => setV({ nixModule: e.target.value })} />
                <p className="hint">
                  {t("Vai para a pasta do projeto e chega à VM em /etc/nixos/lab.nix. Pacotes, serviços e usuários vão aqui. Depois de criada, edite o arquivo e use Aplicar na página do projeto: a VM roda nixos-rebuild sem recriar o disco. O NixOS precisa de pelo menos 2 GB de memória.")}
                </p>
              </div>
            </>
          ) : iso ? (
            <div className="banner info">
              <span className="grow">
                {t("A VM começa com o disco vazio e inicia pelo instalador. Depois de subir, use Tela na aba VMs para abrir o VNC e instalar o sistema. Pacotes e script de inicialização não se aplicam aqui.")}
              </span>
            </div>
          ) : (
            <>
              {field(t("Pacotes"), inp("packages", "curl, vim, htop"), t("Instalados no primeiro boot."))}
              <div className="field">
                <label>{t("Script de inicialização")}</label>
                <div className="np-snips">
                  <span className="muted small">{t("Exemplos:")}</span>
                  {VM_SNIPPETS.map(([l, code]) => (
                    <button key={l} type="button" className="btn small" onClick={() => setV({ script: (v.script.trim() ? v.script.replace(/\s*$/, "\n") : "") + code + "\n" })}>
                      + {l}
                    </button>
                  ))}
                </div>
                <textarea
                  className="code-area"
                  rows={7}
                  spellCheck={false}
                  placeholder={t("# roda uma vez, como root, no primeiro boot") + "\napt-get install -y htop"}
                  value={v.script}
                  onChange={(e) => setV({ script: e.target.value })}
                />
                <p className="hint">{t("Comandos de shell, rodados uma vez pelo cloud-init. A VM tem o usuário gantry com a sua chave, para o SSH do app.")}</p>
              </div>
              <details className="np-adv" open={!!v.cloudInit.trim()}>
                <summary>{t("cloud-init avançado")}</summary>
                <div className="np-snips">
                  <button type="button" className="btn small" onClick={() => setV({ cloudInit: CLOUD_INIT_SAMPLE })}>
                    {t("Usar exemplo")}
                  </button>
                </div>
                <textarea className="code-area" rows={6} spellCheck={false} value={v.cloudInit} onChange={(e) => setV({ cloudInit: e.target.value })} />
                <p className="hint">{t("YAML do cloud-config (write_files, users, timezone…), mesclado ao que o Gantry gera.")}</p>
              </details>
            </>
          )}
        </>
      )}
    </section>
  );
}

/* step 3 */

function Review({ p, st, set, eng }: StepProps & { eng: Engine | null }) {
  const tgt = target(p, st, eng);
  const here = target(p, st, eng, true).host;
  const [chk, setChk] = useState<Check | null>(null);
  const [ports, setPorts] = useState<PortInfo | null>(null);
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [notes, setNotes] = useState<Record<string, PortNote>>({});
  const [dirTouched, setDirTouched] = useState(!!st.dir);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const shared = useShared(tgt.host, chk);
  const fromShared = shared.vars(st.name);
  const dir = dirTouched ? st.dir : "~/gantry/" + (st.name || "projeto");

  useDebounced(
    () => {
      const my = ++seq.current;
      checkYAML(st.yaml).then((r) => {
        if (my === seq.current) setChk(r);
      });
    },
    300,
    [st.yaml],
  );
  // The ports in use on the target machine, so the project does not fail to start.
  useEffect(() => {
    setPorts(null);
    setTouched({});
    setNotes({});
    let live = true;
    api<{ busy: Record<string, string>; minPort: number }>("/hosts/" + enc(tgt.host) + "/ports")
      .then((r) => live && setPorts({ host: tgt.host, ports: r.busy || {}, min: r.minPort || 0 }))
      .catch(() => live && setPorts({ host: tgt.host, ports: {}, min: 0 }));
    return () => {
      live = false;
    };
  }, [tgt.host]);
  // New variables take their default; untouched ports move off busy ones.
  useEffect(() => {
    const vars = chk?.vars ?? [];
    const vals = { ...st.vals };
    for (const v of vars) if (!(v.name in vals)) vals[v.name] = v.default || "";
    const n = { ...notes };
    fixPorts(ports, st.yaml, vars, vals, touched, n);
    if (JSON.stringify(vals) !== JSON.stringify(st.vals)) set({ vals });
    if (JSON.stringify(n) !== JSON.stringify(notes)) setNotes(n);
  }, [chk, ports]); // eslint-disable-line react-hooks/exhaustive-deps

  const go = async () => {
    setErr("");
    if (!/^[a-z0-9][a-z0-9_-]*$/.test(st.name)) return setErr(t("Nome do projeto: letras minúsculas, números, - ou _."));
    if (chk?.error) return setErr(chk.error);
    const missing = (chk?.vars ?? []).filter((v) => v.required && !String(st.vals[v.name] || "").trim() && !fromShared[v.name]).map((v) => v.name);
    if (missing.length) return setErr(t("Preencha: {list}", { list: missing.join(", ") }));
    setBusy(true);
    try {
      const vals = { ...st.vals };
      // Shared services first: a database and a user for this project.
      await shared.provision(st.name, vals);
      const env = envText(vals, (k) => (chk?.vars ?? []).some((x) => x.name === k));
      if (st.save) {
        const name = (st.tplTouched ? st.tplName : st.tplName || st.name).trim() || st.name;
        await api("/templates", {
          method: "POST",
          body: { name, category: st.kind === "vm" ? "VMs" : st.kind === "both" ? t("VMs e containers") : t("Containers"), description: t("Criado em {name}", { name: p.name }), yaml: st.yaml, files: st.files },
        });
        useTemplates.setState({ list: null });
      }
      await createLab(tgt.host, { name: st.name, dir, compose: st.yaml, env, up: st.up, keychain: st.keychain, files: st.files }, p.mkey);
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };

  const fileList = Object.keys(st.files || {}).sort();
  return (
    <div className="np-review">
      <div className="deploy-grid">
        <div>
          <div className="panel panel-pad">
            <h2 style={{ marginBottom: 10 }}>{t("Projeto")}</h2>
            <div className="grid2">
              <div className="field">
                <label>{t("Nome")}</label>
                <input spellCheck={false} value={st.name} onChange={(e) => set({ name: e.target.value.trim() })} />
              </div>
              <div className="field">
                <label>{t("Pasta no host")}</label>
                <FolderInput
                  value={dir}
                  onChange={(v) => {
                    setDirTouched(true);
                    set({ dir: v });
                  }}
                  host={tgt.host}
                  title={t("Pasta do projeto {name}", { name: st.name })}
                />
                <p className="hint">{t("O compose.yaml e o .env ficam aqui.")}</p>
              </div>
            </div>
            <div className="field">
              <label>{t("Máquina de destino")}</label>
              <select
                value={st.remoteHost || here}
                onChange={(e) => {
                  const remote = e.target.value === here ? "" : e.target.value;
                  const x = remote ? gantry.hostByName(remote) : null;
                  if (x && !x.ok && !x.connecting) gantry.refreshHost(x.name);
                  set({ remoteHost: remote });
                }}
              >
                <option value={here}>{t("{name} (esta máquina)", { name: p.name })}</option>
                {p.others
                  .filter((x) => x.name !== here)
                  .map((x) => (
                    <option key={x.name} value={x.name}>
                      {x.name + (x.ok ? "" : t(" · não conectado"))}
                    </option>
                  ))}
              </select>
              <p className="hint">
                {tgt.runtime ? t("Containers no {engine}. ", { engine: gantry.rtName(tgt.runtime) }) : ""}
                {t("Escolha outra máquina para criar o projeto nela (deploy remoto); as portas são conferidas lá.")}
              </p>
            </div>
            <label className="check">
              <input type="checkbox" checked={st.up} onChange={(e) => set({ up: e.target.checked })} /> {t("Subir depois de gerar")}
            </label>
            <label className="check" title={t("Senhas e tokens ficam no chaveiro deste computador em vez do .env; o Gantry os entrega ao compose")}>
              <input type="checkbox" checked={st.keychain} onChange={(e) => set({ keychain: e.target.checked })} /> {t("Guardar senhas e tokens no chaveiro (fora do .env)")}
            </label>
            <label className="check">
              <input type="checkbox" checked={st.save} onChange={(e) => set({ save: e.target.checked })} /> {t("Guardar como modelo, para gerar de novo depois")}
            </label>
            {st.save ? (
              <div className="field">
                <label>{t("Nome do modelo")}</label>
                <input spellCheck={false} value={st.tplTouched ? st.tplName : st.tplName || st.name} onChange={(e) => set({ tplName: e.target.value, tplTouched: true })} />
              </div>
            ) : null}
          </div>
          {shared.section(st.name)}
          <div className="panel panel-pad" style={{ marginTop: 16 }}>
            <h2 style={{ marginBottom: 10 }}>{t("Variáveis")}</h2>
            <VarFields
              vars={chk?.vars ?? []}
              vals={st.vals}
              setVal={(k, v) => {
                set({ vals: { ...st.vals, [k]: v } });
                setTouched((s) => ({ ...s, [k]: true }));
                setNotes((s) => ({ ...s, [k]: portWarning(ports, st.yaml, k, v) }));
              }}
              fromShared={fromShared}
              notes={notes}
              empty={
                <p className="muted small" style={{ margin: 0 }}>
                  {t("Sem variáveis. Troque valores por ${NOME} no YAML para pedir aqui.")}
                </p>
              }
            />
          </div>
          <section className="editor" style={{ marginTop: 16 }} aria-label={t("YAML do projeto")}>
            <div className="editor-bar">
              <span className="mono">compose.yaml</span>
              <span className="muted small">{t("pode editar à vontade")}</span>
            </div>
            <CodeEditor value={st.yaml} onChange={(y) => set({ yaml: y })} label={t("compose do projeto")} />
          </section>
          {/^x-vms:/m.test(st.yaml) ? (
            <VMCloudInit yaml={st.yaml} env={envText(st.vals)} files={st.files} onChange={(yaml, files) => set({ yaml, files })} />
          ) : null}
        </div>
        <div className="side-panels">
          <div className="panel panel-pad">
            <h2 style={{ marginBottom: 6 }}>{t("O que vai ser criado")}</h2>
            <CheckSummary r={chk} />
            {fileList.length ? (
              <>
                <h3 className="sub">{t("Arquivos junto do compose")}</h3>
                <ul className="np-files mono small">
                  {fileList.map((f) => (
                    <li key={f}>{f}</li>
                  ))}
                </ul>
              </>
            ) : null}
          </div>
        </div>
      </div>
      {err ? <p className="flash flash-err">{err}</p> : null}
      <div className="cf-actions">
        <button className="btn btn-ghost" onClick={() => set({ step: 2 })}>
          {t("Voltar")}
        </button>
        <button className="btn primary" disabled={busy} onClick={go}>
          {busy ? (
            <>
              <span className="spin" />
              {st.up ? t("Gerando e subindo…") : t("Gerando…")}
            </>
          ) : (
            t("Gerar projeto")
          )}
        </button>
      </div>
    </div>
  );
}
