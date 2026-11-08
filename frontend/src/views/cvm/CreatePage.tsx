// Creating the Container VM: the choices, then the whole script to read and
// approve. What runs is exactly the script shown: its sha256 goes with the
// request, and the server refuses if it changed.
import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { ErrorBox, Loading } from "@/components/ui";
import { copyText } from "@/lib/format";
import { openJob } from "@/shell/dock";
import { toast } from "@/shell/toasts";
import { useStore } from "@/lib/store";
import { t } from "@/lib/i18n";
import { cvmHref, engineNames, refreshAfter, type CvmList, type Spec } from "./cvm";

type Script = { script: string; sha256: string; userData: string; error?: string };

export function CvmCreate({ from = "", engine = "" }: { from?: string; engine?: string }) {
  const [list, setList] = useState<CvmList | null>(null);
  const [err, setErr] = useState("");
  const [spec, setSpec] = useState<Spec | null>(null);
  const [step, setStep] = useState<1 | 2>(1);
  useEffect(() => {
    api<CvmList>("/cvm")
      .then((l) => {
        setList(l);
        const m = from ? l.machines.find((x) => x.name === from) : undefined;
        if (from && !m) setErr(t("A máquina {name} não existe.", { name: from }));
        else if (m) {
          const sp = { ...l.default, ...m.spec };
          if (engine === "docker") sp.docker = true;
          if (engine === "podman") sp.podman = true;
          if (engine === "containerd") sp.containerd = true;
          setSpec(sp);
        } else setSpec(l.default);
      })
      .catch((e) => setErr((e as Error).message));
  }, [from, engine]);
  const recreate = !!from;
  if (err) return <ErrorBox>{err}</ErrorBox>;
  if (!list || !spec) return <Loading />;
  if (!list.supported)
    return (
      <div className="panel panel-pad">
        <p style={{ margin: 0 }}>{t("A Container VM só roda no macOS por enquanto. Neste computador, use um engine instalado ou um servidor por SSH.")}</p>
      </div>
    );
  if (!recreate && list.machines.some((m) => m.name === spec.name) && step === 1)
    return (
      <div className="panel panel-pad">
        <p style={{ margin: "0 0 10px" }}>{t("A máquina {name} já existe.", { name: spec.name })}</p>
        <a className="btn primary" href={cvmHref(spec.name)}>
          {t("Abrir a máquina")}
        </a>
      </div>
    );
  return (
    <div id="cvm-create">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href="#/local">{t("Este Mac")}</a>
            <span>{recreate ? spec.name : t("Nova Container VM")}</span>
          </nav>
          <h1>{recreate ? t("Recriar {name}", { name: spec.name }) : t("Criar a Container VM")}</h1>
          <div className="sub">
            {recreate
              ? t("Um sistema novo com estas escolhas; o disco de dados (imagens, volumes e containers) continua, e um snapshot dele é tirado antes.")
              : t("Uma VM Linux leve, só para rodar os containers deste Mac. Os projetos continuam nas suas pastas.")}
          </div>
        </div>
      </div>
      <ol className="cvm-steps" aria-label={t("Etapas")}>
        <li className={step === 1 ? "cur" : "done"}>
          <button type="button" className="cvm-step" onClick={() => setStep(1)}>
            <span>{step === 1 ? "1" : "✓"}</span>
            {t("Configurar")}
          </button>
        </li>
        <li className={step === 2 ? "cur" : ""}>
          <span className="cvm-step">
            <span>2</span>
            {t("Revisar o script")}
          </span>
        </li>
        <li>
          <span className="cvm-step">
            <span>3</span>
            {t("Criar e ligar")}
          </span>
        </li>
      </ol>
      {step === 1 ? <Choices list={list} spec={spec} set={setSpec} next={() => setStep(2)} recreate={recreate} /> : <Review list={list} spec={spec} back={() => setStep(1)} recreate={recreate} />}
    </div>
  );
}

function Check({ on, set, children }: { on: boolean; set: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="cvm-check">
      <input type="checkbox" checked={on} onChange={(e) => set(e.target.checked)} />
      <span>{children}</span>
    </label>
  );
}

function Choices({ list, spec, set, next, recreate }: { list: CvmList; spec: Spec; set: (s: Spec) => void; next: () => void; recreate: boolean }) {
  const up = (p: Partial<Spec>) => set({ ...spec, ...p });
  const intel = list.arch !== "arm64";
  const engines = engineNames(spec);
  return (
    <div className="cvm-cols">
      <div className="cvm-main">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Máquina")}</h2>
          </div>
          <div className="cvm-pb cvm-grid">
            <label className="cvm-field">
              <span>{t("Nome")}</span>
              <input className="input" value={spec.name} disabled={recreate} onChange={(e) => up({ name: e.target.value.toLowerCase() })} spellCheck={false} />
            </label>
            <div className="cvm-field">
              <span>{t("Hypervisor")}</span>
              <span className="muted small">{t("vfkit · Apple Virtualization, o mesmo da podman machine. {state}", { state: list.vfkit ? t("Já instalado.") : t("O Gantry instala pelo Homebrew.") })}</span>
            </div>
            <div className="cvm-field">
              <span>{t("Sistema")}</span>
              <span className="muted small">{t("Debian 13, imagem cloud oficial, conferida pelo SHA512")}</span>
            </div>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Engines de containers")}</h2>
            <span className="muted small">{t("Pode marcar mais de um")}</span>
          </div>
          <div className="cvm-pb cvm-grid">
            <Check on={spec.docker} set={(v) => up({ docker: v })}>
              <b>Docker Engine</b>
              <br />
              <span className="muted small">{t("com Buildx e Compose, do repositório oficial")}</span>
            </Check>
            <Check on={spec.podman} set={(v) => up({ podman: v })}>
              <b>Podman</b>
              <br />
              <span className="muted small">{t("pacote do Debian, API compatível com Docker")}</span>
            </Check>
            <Check on={!!spec.containerd} set={(v) => up({ containerd: v })}>
              <b>containerd + nerdctl</b>
              <br />
              <span className="muted small">{t("com BuildKit, release oficial conferida pelo sha256. Mais perto do que roda no Kubernetes.")}</span>
            </Check>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Recursos")}</h2>
            <span className="muted small">{t("Dá para mudar depois")}</span>
          </div>
          <div className="cvm-pb cvm-grid">
            <label className="cvm-field">
              <span>
                {t("CPUs")} <span className="mono muted">{spec.cpus}</span>
              </span>
              <input type="range" min={1} max={16} value={spec.cpus} onChange={(e) => up({ cpus: +e.target.value })} />
            </label>
            <label className="cvm-field">
              <span>
                {t("Memória")} <span className="mono muted">{Math.round(spec.memoryMiB / 1024)} GiB</span>
              </span>
              <input type="range" min={2} max={64} value={Math.round(spec.memoryMiB / 1024)} onChange={(e) => up({ memoryMiB: +e.target.value * 1024 })} />
            </label>
            <label className="cvm-field">
              <span>
                {t("Disco de dados")} <span className="mono muted">{t("até {n} GiB", { n: spec.dataGiB })}</span>
              </span>
              <input type="range" min={16} max={1024} step={16} value={spec.dataGiB} disabled={recreate} onChange={(e) => up({ dataGiB: +e.target.value })} />
              <span className="muted small">{t("Arquivo esparso: ocupa no Mac só o que for usado.")}</span>
            </label>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Arquiteturas e credenciais")}</h2>
          </div>
          <div className="cvm-pb">
            <Check on={spec.rosetta && !intel} set={(v) => up({ rosetta: v })}>
              <b>{t("amd64 pelo Rosetta")}</b> <span className="badge accent">{t("quase nativo")}</span>
              <br />
              <span className="muted small">{intel ? t("Só em Macs com Apple Silicon.") : t("Roda e constrói imagens linux/amd64.")}</span>
            </Check>
            <Check on={spec.qemu} set={(v) => up({ qemu: v })}>
              <b>{t("Outras arquiteturas pelo QEMU")}</b> <span className="badge warn">{t("lento")}</span>
              <br />
              <span className="muted small">{t("arm/v7, riscv64, ppc64le, s390x: mais para build do que para rodar.")}</span>
            </Check>
            <Check on={spec.sshAgent} set={(v) => up({ sshAgent: v })}>
              <b>{t("Repassar o ssh-agent do Mac")}</b>
              <br />
              <span className="muted small">{t("git clone e buildx --ssh com as suas chaves, sem copiar nada.")}</span>
            </Check>
            <p className="muted small" style={{ margin: 0 }}>
              {t("A pasta {home} aparece na máquina no mesmo caminho, então os projetos e o ~/.kube funcionam igual.", { home: list.home })}
            </p>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Ligar e desligar")}</h2>
          </div>
          <div className="cvm-pb">
            <Check on={spec.autoStart} set={(v) => up({ autoStart: v })}>
              {t("Ligar quando o Gantry abrir")}
            </Check>
            <p className="muted small" style={{ margin: 0 }}>
              {t("A máquina continua ligada com o Gantry fechado; desligue pela barra lateral ou pela página dela.")}
            </p>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Passos seus")}</h2>
            <span className="muted small">{t("opcional")}</span>
          </div>
          <div className="cvm-pb">
            <label className="cvm-field">
              <span>{t("Script extra, roda por último como root")}</span>
              <textarea className="input" rows={4} value={spec.extra || ""} placeholder="apt-get install -y htop jq" onChange={(e) => up({ extra: e.target.value })} spellCheck={false} />
            </label>
          </div>
        </section>
      </div>
      <aside className="panel cvm-side">
        <div className="cvm-ph">
          <h2>{t("Resumo")}</h2>
        </div>
        <div className="cvm-pb">
          <span className="cvm-kv">
            <span className="muted">{t("Engines")}</span>
            <span>{engines || t("nenhum")}</span>
          </span>
          <span className="cvm-kv">
            <span className="muted">{t("CPU e memória")}</span>
            <span className="mono">
              {spec.cpus} CPU · {Math.round(spec.memoryMiB / 1024)} GiB
            </span>
          </span>
          <span className="cvm-kv">
            <span className="muted">{t("Disco de dados")}</span>
            <span className="mono">{t("até {n} GiB", { n: spec.dataGiB })}</span>
          </span>
          <span className="cvm-kv">
            <span className="muted">{t("Fica em")}</span>
            <span className="mono">~/.gantry/machines/{spec.name}</span>
          </span>
          <p className="muted small">{t("Nada é instalado ainda. No próximo passo você lê o script completo que vai rodar e aprova.")}</p>
          <div className="cvm-actions">
            <a className="btn" href={recreate ? cvmHref(spec.name, "recursos") : "#/local/t/engines"}>
              {t("Cancelar")}
            </a>
            <button className="btn primary" type="button" disabled={!spec.docker && !spec.podman && !spec.containerd} onClick={next}>
              {t("Revisar o script →")}
            </button>
          </div>
        </div>
      </aside>
    </div>
  );
}

function Review({ list, spec, back, recreate }: { list: CvmList; spec: Spec; back: () => void; recreate: boolean }) {
  const [sc, setSc] = useState<Script | null>(null);
  const [err, setErr] = useState("");
  const [ok, setOk] = useState(false);
  const [view, setView] = useState<"script" | "cloud">("script");
  const [busy, setBusy] = useState(false);
  const s = useMemo(() => (list.arch !== "arm64" ? { ...spec, rosetta: false } : spec), [spec, list.arch]);
  useEffect(() => {
    api<Script>("/cvm/script", { method: "POST", body: s })
      .then(setSc)
      .catch((e) => setErr((e as Error).message));
  }, [s]);
  const create = async () => {
    if (!sc) return;
    setBusy(true);
    try {
      const r = await api<{ id: string }>("/cvm", { method: "POST", body: { spec: s, sha256: sc.sha256, recreate } });
      useStore.setState({ localBusy: true });
      openJob(r.id, recreate ? t("Recriar {name}", { name: s.name }) : t("Criar a Container VM {name}", { name: s.name }), (end) => {
        refreshAfter();
        if (end.ok) location.hash = cvmHref(s.name);
      });
      location.hash = recreate ? cvmHref(s.name, "logs") : "#/local/t/engines";
    } catch (e) {
      toast((e as Error).message, "err");
      setBusy(false);
    }
  };
  const download = () => {
    if (!sc) return;
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([sc.script], { type: "text/x-shellscript" }));
    a.download = "provision.sh";
    a.click();
    URL.revokeObjectURL(a.href);
  };
  if (err) return <ErrorBox>{err}</ErrorBox>;
  if (!sc) return <Loading />;
  const steps = sc.script
    .split("\n")
    .filter((l) => l.startsWith("passo "))
    .map((l) => l.replace(/^passo "(.*)"$/, "$1"));
  return (
    <div className="cvm-cols">
      <section className="panel cvm-main">
        <div className="cvm-ph">
          <span>
            <h2>
              provision.sh <span className="badge">{t("roda como root, dentro da máquina")}</span>
            </h2>
            <span className="mono muted small">sha256 {sc.sha256}</span>
          </span>
          <span className="cvm-actions">
            <span className="cvm-seg" role="tablist">
              <button type="button" role="tab" aria-selected={view === "script"} onClick={() => setView("script")}>
                {t("Script")}
              </button>
              <button type="button" role="tab" aria-selected={view === "cloud"} onClick={() => setView("cloud")}>
                cloud-init
              </button>
            </span>
            <button className="btn small" type="button" onClick={() => void copyText(sc.sha256).then(() => toast(t("Copiado"), "ok"))}>
              {t("Copiar sha256")}
            </button>
            <button className="btn small" type="button" onClick={download}>
              {t("Baixar .sh")}
            </button>
          </span>
        </div>
        <pre className="cvm-code">{view === "script" ? sc.script : sc.userData}</pre>
      </section>
      <aside className="cvm-side">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("O que vai acontecer")}</h2>
          </div>
          <div className="cvm-pb">
            <span className="cvm-group">{t("No Mac · seu usuário, sem sudo")}</span>
            <ol className="cvm-list">
              {recreate ? <li>{t("Desligar a máquina, tirar um snapshot do disco de dados e remover só o disco do sistema")}</li> : null}
              {!list.vfkit ? <li><span className="mono">brew install vfkit</span></li> : null}
              <li>
                {t("Baixar a imagem do sistema e conferir o SHA512")}
                <br />
                <span className="mono muted small">{list.image}</span>
              </li>
              <li>{recreate ? t("Criar um sistema.raw novo (o dados.raw fica) e uma chave SSH nova") : t("Criar sistema.raw e dados.raw (esparsos) e uma chave SSH só desta máquina")}</li>
              <li>{t("Ligar o vfkit; os sockets ficam em ~/.gantry/machines/{name}", { name: s.name })}</li>
            </ol>
            <span className="cvm-group">{t("Dentro da máquina · provision.sh")}</span>
            <ol className="cvm-list">
              {steps.map((x) => (
                <li key={x}>{x.replace(/^\d+\.\s*/, "")}</li>
              ))}
            </ol>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-pb">
            {sc.error ? <ErrorBox>{sc.error}</ErrorBox> : null}
            <label className="cvm-check">
              <input type="checkbox" checked={ok} onChange={(e) => setOk(e.target.checked)} />
              <span>{t("Li o script e aprovo que ele rode na máquina {name}", { name: s.name })}</span>
            </label>
            <div className="cvm-actions">
              <button className="btn" type="button" onClick={back}>
                {t("← Voltar")}
              </button>
              <button className="btn primary" type="button" disabled={!ok || busy || !!sc.error} onClick={() => void create()}>
                {recreate ? t("Aprovar e recriar") : t("Aprovar e criar")}
              </button>
            </div>
            <span className="muted small">{t("O script fica salvo com a máquina, com o log de cada execução.")}</span>
          </div>
        </section>
      </aside>
    </div>
  );
}
