// The setup tabs of a Container VM: how the Mac reaches its engines,
// storage (shared folders, snapshots) and platforms and credentials.
import { useEffect, useState } from "react";
import { api, enc } from "@/lib/api";
import { fmtBytes } from "@/lib/format";
import { confirm } from "@/components/dialog";
import { CopyCommand, ErrorBox, Loading } from "@/components/ui";
import { openJob } from "@/shell/dock";
import { toast } from "@/shell/toasts";
import { t } from "@/lib/i18n";
import { recreateHref, refreshAfter, type Creds, type CvmView, type Integration, type Platform, type Share, type Snap } from "./cvm";

function Toggle({ on, label, sub, disabled, set }: { on: boolean; label: React.ReactNode; sub?: React.ReactNode; disabled?: boolean; set: (v: boolean) => void }) {
  return (
    <label className={"cvm-check cvm-row-check" + (disabled ? " off" : "")}>
      <input type="checkbox" checked={on} disabled={disabled} onChange={(e) => set(e.target.checked)} />
      <span>
        {label}
        {sub ? (
          <>
            <br />
            <span className="muted small">{sub}</span>
          </>
        ) : null}
      </span>
    </label>
  );
}

/* Engines e sockets */

export function EnginesTab({ v }: { v: CvmView }) {
  const [it, setIt] = useState<Integration | null>(null);
  const [err, setErr] = useState("");
  const [test, setTest] = useState<{ name: string; ok: boolean; ms: number; info: string }[] | null>(null);
  const [busy, setBusy] = useState(false);
  const base = "/cvm/" + enc(v.name);
  useEffect(() => {
    api<Integration>(base + "/integracao")
      .then(setIt)
      .catch((e) => setErr((e as Error).message));
  }, [base]);
  const put = async (body: Record<string, boolean>) => {
    setBusy(true);
    try {
      setIt(await api<Integration>(base + "/integracao", { method: "PUT", body }));
      refreshAfter();
    } catch (e) {
      toast((e as Error).message, "err");
    }
    setBusy(false);
  };
  const link = async (on: boolean) => {
    setBusy(true);
    try {
      setIt(await api<Integration>(base + "/link", { method: "POST", body: { on } }));
      toast(on ? t("/var/run/docker.sock aponta para a máquina.") : t("Link removido."), "ok");
    } catch (e) {
      toast((e as Error).message, "err");
    }
    setBusy(false);
  };
  const runTest = async () => {
    setTest(null);
    try {
      setTest(await api(base + "/teste"));
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  if (err) return <ErrorBox>{err}</ErrorBox>;
  if (!it) return <Loading />;
  const s = v.spec;
  const missing = [!s.docker ? "docker" : "", !s.podman ? "podman" : "", !s.containerd ? "containerd" : ""].filter(Boolean);
  return (
    <div className="cvm-cols">
      <div className="cvm-main">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Sockets no Mac")}</h2>
            <button className="btn small" type="button" disabled={v.state !== "running"} onClick={() => void runTest()}>
              {t("Testar conexão")}
            </button>
          </div>
          <div className="table-wrap">
            <table className="grid" style={{ minWidth: 560 }}>
              <thead>
                <tr>
                  <th>{t("Engine")}</th>
                  <th>{t("Socket no Mac")}</th>
                  <th>{t("Dentro da máquina")}</th>
                </tr>
              </thead>
              <tbody>
                {s.docker ? (
                  <tr>
                    <td>
                      <b>Docker</b>
                    </td>
                    <td className="mono">{it.dockerSock}</td>
                    <td className="mono">/run/docker.sock</td>
                  </tr>
                ) : null}
                {s.podman ? (
                  <tr>
                    <td>
                      <b>Podman</b>
                    </td>
                    <td className="mono">{it.podmanSock}</td>
                    <td className="mono">/run/podman/podman.sock</td>
                  </tr>
                ) : null}
                {s.containerd ? (
                  <tr>
                    <td>
                      <b>containerd</b> <span className="muted small">nerdctl</span>
                    </td>
                    <td className="muted">{t("comando nerdctl em {dir}", { dir: "~/.gantry/bin" })}</td>
                    <td className="mono">/run/containerd/containerd.sock</td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
          {test ? (
            <div className="cvm-pb">
              {test.map((r) => (
                <span key={r.name} className="cvm-kv">
                  <span>{r.name}</span>
                  <span className={"badge " + (r.ok ? "ok" : "err")}>{r.ok ? t("ok · {ms} ms", { ms: r.ms }) : r.info}</span>
                </span>
              ))}
            </div>
          ) : null}
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Apontar o terminal do Mac")}</h2>
            <span className="muted small">{t("contexto atual do docker: {ctx}", { ctx: it.currentContext || "default" })}</span>
          </div>
          <div className="cvm-pb">
            {s.docker ? (
              <>
                <span className="cvm-group">{t("Docker: contexto (recomendado)")}</span>
                <CopyCommand cmd={"docker context use " + (it.dockerContext || "gantry")} />
                {it.currentContext !== it.dockerContext || !it.dockerContext ? (
                  <div>
                    <button className="btn small" type="button" disabled={busy} onClick={() => void put({ use: true })}>
                      {t("Usar como padrão do docker")}
                    </button>
                  </div>
                ) : (
                  <span className="badge ok">{t("é o contexto atual")}</span>
                )}
              </>
            ) : null}
            <span className="cvm-group">{t("Só nesta sessão do terminal")}</span>
            {it.commands
              .filter((c) => c.startsWith("export"))
              .map((c) => (
                <CopyCommand key={c} cmd={c} />
              ))}
            {s.containerd ? (
              <>
                <span className="cvm-group">nerdctl</span>
                <p className="muted small" style={{ margin: 0 }}>
                  {t("O nerdctl não fala com um containerd remoto: o comando de ~/.gantry/bin roda ele na máquina, na mesma pasta.")}
                </p>
                <CopyCommand cmd="nerdctl run --rm -it alpine" />
              </>
            ) : null}
          </div>
        </section>
        {missing.length ? (
          <section className="panel panel-pad">
            <h2 style={{ marginBottom: 6 }}>{t("Adicionar engine")}</h2>
            <p className="muted small" style={{ margin: "0 0 10px" }}>
              {t("Recria o sistema da máquina com o engine novo, mantendo o disco de dados (imagens, volumes e containers). Você revisa o script antes.")}
            </p>
            <div className="cvm-actions">
              {missing.map((m) => (
                <a key={m} className="btn" href={recreateHref(v.name, m)}>
                  {m === "containerd" ? t("Adicionar containerd + nerdctl") : t("Adicionar {engine}", { engine: m === "docker" ? "Docker" : "Podman" })}
                </a>
              ))}
            </div>
          </section>
        ) : null}
      </div>
      <aside className="cvm-side">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Integração com o Mac")}</h2>
          </div>
          <div className="cvm-pb">
            {s.docker ? <Toggle on={!!it.dockerContext} disabled={busy} set={(x) => void put({ dockerContext: x })} label={t("Contexto {name} no Docker", { name: it.dockerContext || "gantry" })} sub={t("escolha com docker context use")} /> : null}
            {s.podman ? <Toggle on={!!it.podmanContext} disabled={busy} set={(x) => void put({ podmanContext: x })} label={t("Contexto do Docker para o Podman")} sub={t("o CLI docker falando com o Podman da máquina")} /> : null}
            {s.podman ? <Toggle on={it.podmanConn} disabled={busy || !it.podmanCli} set={(x) => void put({ podmanConn: x })} label={t("Conexão no Podman do Mac")} sub={it.podmanCli ? t("podman system connection") : t("o podman não está instalado no Mac")} /> : null}
            <Toggle
              on={it.path}
              disabled={busy}
              set={(x) => void put({ path: x })}
              label={t("Comandos da máquina no PATH")}
              sub={t("docker, podman e nerdctl de ~/.gantry/bin, depois dos do Mac (em {rc})", { rc: it.rc.replace(/^.*\//, "~/") })}
            />
            <Toggle
              on={it.link}
              disabled={busy || !s.docker}
              set={(x) => void link(x)}
              label={t("Link em /var/run/docker.sock")}
              sub={it.linkOther ? t("hoje aponta para {p}; pede a senha de administrador", { p: it.linkOther }) : t("para ferramentas que só olham esse caminho; pede a senha de administrador")}
            />
            {it.shims.length ? (
              <p className="muted small" style={{ margin: 0 }}>
                {t("Em ~/.gantry/bin: {list}", { list: it.shims.join(", ") })}
              </p>
            ) : null}
            {!it.dockerCli ? (
              <p className="muted small" style={{ margin: 0 }}>
                {t("O Mac não tem o CLI docker: o Gantry usa o docker da máquina pelo comando de ~/.gantry/bin.")}
              </p>
            ) : null}
          </div>
        </section>
      </aside>
    </div>
  );
}

/* Armazenamento */

export function StorageTab({ v, reload }: { v: CvmView; reload: () => void }) {
  const [snaps, setSnaps] = useState<Snap[] | null>(null);
  const [shares, setShares] = useState<Share[]>(v.spec.shares || []);
  const [newPath, setNewPath] = useState("");
  const [busy, setBusy] = useState(false);
  const base = "/cvm/" + enc(v.name);
  useEffect(() => {
    void api<Snap[]>(base + "/snapshots").then(setSnaps);
  }, [base]);
  const saveSpec = async (body: Record<string, unknown>, msg: string) => {
    try {
      await api(base, { method: "PUT", body });
      toast(msg, "ok");
      reload();
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  const take = async () => {
    setBusy(true);
    try {
      setSnaps(await api<Snap[]>(base + "/snapshots", { method: "POST", body: { reason: t("feito por você") } }));
      toast(t("Snapshot tirado."), "ok");
    } catch (e) {
      toast((e as Error).message, "err");
    }
    setBusy(false);
  };
  const restore = async (sn: Snap) => {
    if (!(await confirm(t("Restaurar o snapshot de {when}?", { when: new Date(sn.at * 1000).toLocaleString() }), t("A máquina desliga, o disco de dados volta para esse momento e ela liga de novo. O disco de agora vira mais um snapshot, então nada se perde."), t("Restaurar"), true))) return;
    try {
      const r = await api<{ id: string }>(base + "/snapshots/" + sn.id + "/restaurar", { method: "POST" });
      openJob(r.id, t("Restaurar snapshot de {name}", { name: v.name }), () => {
        refreshAfter();
        void api<Snap[]>(base + "/snapshots").then(setSnaps);
      });
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  const del = async (sn: Snap) => {
    if (!(await confirm(t("Apagar o snapshot?"), sn.reason, t("Apagar"), true))) return;
    try {
      setSnaps(await api<Snap[]>(base + "/snapshots/" + sn.id, { method: "DELETE" }));
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  const addShare = () => {
    const p = newPath.trim();
    if (!p) return;
    const next = [...shares, { path: p, readOnly: false }];
    setShares(next);
    setNewPath("");
    void saveSpec({ shares: next }, t("Pasta adicionada. Vale quando a máquina reiniciar."));
  };
  const rmShare = (i: number) => {
    const next = shares.filter((_, k) => k !== i);
    setShares(next);
    void saveSpec({ shares: next }, t("Pasta removida. Vale quando a máquina reiniciar."));
  };
  const roShare = (i: number, ro: boolean) => {
    const next = shares.map((x, k) => (k === i ? { ...x, readOnly: ro } : x));
    setShares(next);
    void saveSpec({ shares: next }, t("Salvo. Vale quando a máquina reiniciar."));
  };
  return (
    <div className="cvm-cols">
      <div className="cvm-main">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Pastas do Mac")}</h2>
            <span className="muted small">{t("no mesmo caminho dentro da máquina")}</span>
          </div>
          <div className="table-wrap">
            <table className="grid" style={{ minWidth: 560 }}>
              <thead>
                <tr>
                  <th>{t("Pasta")}</th>
                  <th>{t("Acesso")}</th>
                  <th className="act" />
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td className="mono">~ ({t("sua pasta de usuário")})</td>
                  <td>
                    <span className="badge">{t("leitura e escrita")}</span>
                  </td>
                  <td className="act muted small">{t("sempre")}</td>
                </tr>
                {shares.map((sh, i) => (
                  <tr key={sh.path}>
                    <td className="mono">{sh.path}</td>
                    <td>
                      <label className="cvm-check small">
                        <input type="checkbox" checked={!!sh.readOnly} onChange={(e) => roShare(i, e.target.checked)} />
                        <span>{t("só leitura")}</span>
                      </label>
                    </td>
                    <td className="act">
                      <button className="btn small danger" type="button" onClick={() => rmShare(i)}>
                        {t("Remover")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="cvm-pb">
            <div className="cvm-actions">
              <input className="input mono" style={{ maxWidth: 420 }} placeholder="/Volumes/Dados/projetos" value={newPath} onChange={(e) => setNewPath(e.target.value)} aria-label={t("Caminho da pasta no Mac")} spellCheck={false} />
              <button className="btn" type="button" disabled={!newPath.trim().startsWith("/")} onClick={addShare}>
                {t("Adicionar pasta")}
              </button>
            </div>
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Snapshots do disco de dados")}</h2>
            <button className="btn small primary" type="button" disabled={busy} onClick={() => void take()}>
              {t("Tirar snapshot agora")}
            </button>
          </div>
          {!snaps ? (
            <Loading />
          ) : !snaps.length ? (
            <p className="cvm-pb muted" style={{ margin: 0 }}>
              {t("Nenhum snapshot ainda.")}
            </p>
          ) : (
            <div className="table-wrap">
              <table className="grid" style={{ minWidth: 560 }}>
                <thead>
                  <tr>
                    <th>{t("Quando")}</th>
                    <th>{t("Motivo")}</th>
                    <th>{t("Ocupa a mais")}</th>
                    <th className="act" />
                  </tr>
                </thead>
                <tbody>
                  {snaps.map((sn) => (
                    <tr key={sn.id}>
                      <td className="mono">{new Date(sn.at * 1000).toLocaleString()}</td>
                      <td>
                        {sn.reason} {sn.auto ? <span className="badge">{t("automático")}</span> : <span className="badge accent">{t("manual")}</span>}
                      </td>
                      <td className="mono">{fmtBytes(sn.extra)}</td>
                      <td className="act">
                        <button className="btn small" type="button" onClick={() => void restore(sn)}>
                          {t("Restaurar")}
                        </button>
                        <button className="btn small danger" type="button" onClick={() => void del(sn)}>
                          {t("Apagar")}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="cvm-pb">
            <label className="cvm-check">
              <input type="checkbox" checked={!!v.spec.snapDaily} onChange={(e) => void saveSpec({ snapDaily: e.target.checked }, t("Salvo."))} />
              <span>{t("Um snapshot por dia, ao ligar (guarda os últimos {n} automáticos)", { n: v.spec.snapKeep || 7 })}</span>
            </label>
          </div>
        </section>
      </div>
      <aside className="cvm-side">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Onde guardar cada coisa")}</h2>
          </div>
          <div className="cvm-pb">
            <span className="cvm-tip">
              <span className="badge ok">{t("volume")}</span>
              <span>{t("Banco de dados, filas, cache: velocidade de disco nativo.")}</span>
            </span>
            <span className="cvm-tip">
              <span className="badge ok">{t("volume")}</span>
              <span>{t("node_modules, .venv e target/: um volume por cima da pasta do código.")}</span>
            </span>
            <span className="cvm-tip">
              <span className="badge accent">{t("pasta")}</span>
              <span>{t("O código que você edita no Mac. Rápido para ler, mais lento com milhares de arquivos pequenos.")}</span>
            </span>
            <p className="muted small" style={{ margin: 0 }}>
              {t("Snapshots são clones do APFS: instantâneos, e só ocupam o que mudar depois. Com a máquina ligada ela pausa por um instante durante o clone.")}
            </p>
          </div>
        </section>
      </aside>
    </div>
  );
}

/* Multi-arch e credenciais */

const HOW: Record<string, string> = { nativo: t("nativo"), rosetta: "Rosetta", qemu: "QEMU" };

export function PlatformsTab({ v, reload }: { v: CvmView; reload: () => void }) {
  const [ps, setPs] = useState<Platform[] | null>(null);
  const [cr, setCr] = useState<Creds | null>(null);
  const [err, setErr] = useState("");
  const base = "/cvm/" + enc(v.name);
  const running = v.state === "running";
  const load = () => {
    if (running)
      api<Platform[]>(base + "/plataformas")
        .then(setPs)
        .catch((e) => setErr((e as Error).message));
    void api<Creds>(base + "/credenciais").then(setCr);
  };
  useEffect(load, [base, running]); // eslint-disable-line react-hooks/exhaustive-deps
  const saveSpec = async (body: Record<string, unknown>, msg: string) => {
    try {
      await api(base, { method: "PUT", body });
      toast(msg, "ok");
      reload();
      load();
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  const qemu = async () => {
    try {
      const r = await api<{ id: string }>(base + "/plataformas/qemu", { method: "POST" });
      openJob(r.id, t("QEMU em {name}", { name: v.name }), () => load());
    } catch (e) {
      toast((e as Error).message, "err");
    }
  };
  const plats = (ps || []).filter((p) => p.active && !p.name.includes(",")).map((p) => p.name);
  const list = plats.slice(0, 3).join(",") || "linux/arm64,linux/amd64";
  const qemuOn = (ps || []).some((p) => p.how === "qemu" && p.active);
  return (
    <div className="cvm-cols">
      <div className="cvm-main">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Plataformas")}</h2>
            {!qemuOn && running ? (
              <button className="btn small" type="button" onClick={() => void qemu()}>
                {t("Instalar o QEMU para as outras")}
              </button>
            ) : null}
          </div>
          {!running ? (
            <p className="cvm-pb muted" style={{ margin: 0 }}>
              {t("Ligue a máquina para ver as plataformas.")}
            </p>
          ) : err ? (
            <div className="cvm-pb">
              <ErrorBox>{err}</ErrorBox>
            </div>
          ) : !ps ? (
            <Loading />
          ) : (
            <div className="table-wrap">
              <table className="grid" style={{ minWidth: 520 }}>
                <thead>
                  <tr>
                    <th>{t("Plataforma")}</th>
                    <th>{t("Como roda")}</th>
                    <th>{t("Velocidade")}</th>
                    <th>{t("Ativa")}</th>
                  </tr>
                </thead>
                <tbody>
                  {ps.map((p) => (
                    <tr key={p.name}>
                      <td className="mono">{p.name}</td>
                      <td>{HOW[p.how] || p.how}</td>
                      <td>
                        <span className={"badge " + (p.how === "qemu" ? "warn" : "ok")}>{p.how === "nativo" ? t("nativa") : p.how === "rosetta" ? t("quase nativa") : t("lenta")}</span>
                      </td>
                      <td>{p.active ? <span className="badge ok">{t("sim")}</span> : <span className="badge">{t("não")}</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="cvm-pb">
            <Toggle on={v.spec.rosetta} set={(x) => void saveSpec({ rosetta: x }, t("Salvo. Vale quando a máquina reiniciar."))} label={t("amd64 pelo Rosetta")} sub={t("precisa do Rosetta no Mac (softwareupdate --install-rosetta)")} />
          </div>
        </section>
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Build multi-arch")}</h2>
          </div>
          <div className="cvm-pb">
            <p className="muted small" style={{ margin: 0 }}>
              {t("O Docker da máquina usa o containerd image store: a imagem com as várias plataformas fica local e dá para testar cada uma antes do push.")}
            </p>
            <CopyCommand cmd={"docker buildx build --platform " + list + " -t app:dev --load ."} />
            <CopyCommand cmd={"docker buildx build --platform " + list + " -t registry.example.com/app:1.0 --push ."} />
            {v.spec.podman ? <CopyCommand cmd={"podman build --platform " + list + " --manifest app:dev ."} /> : null}
          </div>
        </section>
      </div>
      <aside className="cvm-side">
        <section className="panel">
          <div className="cvm-ph">
            <h2>{t("Credenciais do Mac")}</h2>
          </div>
          {!cr ? (
            <Loading />
          ) : (
            <div className="cvm-pb">
              <Toggle
                on={v.spec.sshAgent}
                set={(x) => void saveSpec({ sshAgent: x }, t("Salvo."))}
                label={
                  <>
                    <b>ssh-agent</b> {cr.agent ? <span className="badge ok">{cr.agentKeys >= 0 ? t("{n} chaves", { n: cr.agentKeys }) : t("ativo")}</span> : null}
                  </>
                }
                sub={cr.agentSock ? t("Na máquina em /run/host-services/ssh-auth.sock. As chaves não saem do Mac.") : t("O Gantry não vê um ssh-agent (SSH_AUTH_SOCK) no Mac.")}
              />
              <span className="cvm-group">{t("Para um container usar")}</span>
              <CopyCommand cmd="-v /run/host-services/ssh-auth.sock:/ssh-agent -e SSH_AUTH_SOCK=/ssh-agent" />
              <span className="cvm-group">{t("Registries")}</span>
              <p className="muted small" style={{ margin: 0 }}>
                {cr.store ? t("O CLI no Mac lê as senhas do {store} e manda em cada pull, push e build. Nada é copiado para a máquina.", { store: cr.store }) : t("O CLI no Mac manda as senhas em cada pull, push e build. Nada é copiado para a máquina.")}
              </p>
              <span className="cvm-actions">
                {cr.registries.length ? cr.registries.map((r) => <span key={r} className="badge ok">{r}</span>) : <span className="muted small">{t("nenhum login no ~/.docker/config.json")}</span>}
              </span>
              <span className="cvm-group">{t("Pastas de credenciais")}</span>
              <span className="cvm-actions">
                {cr.folders.map((f) => (
                  <span key={f} className="badge mono">
                    {f}
                  </span>
                ))}
              </span>
              <p className="muted small" style={{ margin: 0 }}>
                {t("Estão na máquina no mesmo caminho (dentro da sua pasta) e só entram num container com um volume explícito.")}
              </p>
            </div>
          )}
        </section>
      </aside>
    </div>
  );
}
