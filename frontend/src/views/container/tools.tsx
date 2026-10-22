// Container tools (files inside it, live limits, image from it, events),
// image tools (layers, push, save/load) and registry logins.
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, fmtAgo, rtQuery } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { confirm, formDialog, Modal, openDialog } from "@/components/dialog";
import { Loading } from "@/components/ui";
import { loadResources } from "./resources";
import { LOCALE, t } from "@/lib/i18n";

/* eslint-disable @typescript-eslint/no-explicit-any */
export const qsJoin = (url: string, q: string) => url + (url.includes("?") ? "&" : "?") + q;

type CtrRef = { id: string; name: string; service?: string };

// ctrFilesDialog browses the container's files: download, upload, delete.
export function ctrFilesDialog(host: string, c: CtrRef, rt?: string) {
  openDialog((close) => <FilesDialog host={host} c={c} rt={rt} close={close} />);
}

type FileEntry = { name: string; type: string; size: number; mtime?: number };

function FilesDialog({ host, c, rt, close }: { host: string; c: CtrRef; rt?: string; close: () => void }) {
  const base = "/hosts/" + enc(host) + "/containers/" + enc(c.id);
  const rq = rtQuery(rt);
  const [cwd, setCwd] = useState("/");
  const [input, setInput] = useState("/");
  const [files, setFiles] = useState<FileEntry[] | null>(null);
  const [err, setErr] = useState("");
  const join = (dir: string, name: string) => (dir.replace(/\/$/, "") + "/" + name).replace(/^\/\//, "/");
  const go = async (p: string) => {
    setFiles(null);
    setErr("");
    try {
      const r = await api<{ path: string; files: FileEntry[] }>(qsJoin(base + "/arquivos" + rq, "path=" + enc(p)));
      setCwd(r.path);
      setInput(r.path);
      setFiles(r.files);
    } catch (e) {
      setInput(p);
      setErr((e as Error).message);
    }
  };
  useEffect(() => {
    go("/");
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const download = async (p: string, isDir: boolean) => {
    const nm = p.split("/").pop() || t("raiz");
    const dest = await gantry.pickPath({ kind: "salvar", title: t("Salvar {path}", { path: p }), name: isDir ? nm + ".tar" : nm });
    if (!dest) return;
    gantry.toast(t("Baixando {path}…", { path: p }));
    try {
      const r = await api<{ kind: string; path: string; bytes: number }>(base + "/arquivos/baixar" + rq, { method: "POST", body: { path: p, dest } });
      gantry.toast(r.kind === "tar" ? t("Pasta salva como .tar em {path} ({size})", { path: r.path, size: fmtBytes(r.bytes) }) : t("Salvo em {path} ({size})", { path: r.path, size: fmtBytes(r.bytes) }), "ok");
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  const upload = async (kind: "abrir" | "pasta") => {
    const src = await gantry.pickPath({ kind, title: kind === "pasta" ? t("Pasta a enviar para {dir}", { dir: cwd }) : t("Arquivo a enviar para {dir}", { dir: cwd }) });
    if (!src) return;
    try {
      const r = await api<{ path: string }>(base + "/arquivos/enviar" + rq, { method: "POST", body: { src, dir: cwd } });
      gantry.toast(t("Enviado para {path}", { path: r.path }), "ok");
      go(cwd);
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  return (
    <Modal
      title={t("Arquivos de {name}", { name: c.service || c.name })}
      wide
      onClose={close}
      actions={
        <button className="btn" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      <div className="input-pick">
        <button className="btn small" title={t("Pasta acima")} onClick={() => go(cwd.replace(/\/[^/]*$/, "") || "/")}>
          ↑
        </button>
        <input
          className="mono"
          spellCheck={false}
          aria-label={t("Pasta")}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              go(input.trim() || "/");
            }
          }}
        />
        <button className="btn small" onClick={() => go(input.trim() || "/")}>
          {t("Abrir")}
        </button>
      </div>
      <div className="tools-row">
        <button className="btn small" onClick={() => upload("abrir")}>
          {t("Enviar arquivo…")}
        </button>
        <button className="btn small" onClick={() => upload("pasta")}>
          {t("Enviar pasta…")}
        </button>
        <button className="btn small" onClick={() => download(cwd, true)}>
          {t("Baixar esta pasta")}
        </button>
      </div>
      <div className="files-list">
        {err ? (
          <>
            <p className="flash flash-err">{err}</p>
            <div className="row-actions">
              <button className="btn small" onClick={() => download(input, false)}>
                {t("Baixar {path} mesmo assim", { path: input })}
              </button>
            </div>
          </>
        ) : !files ? (
          <Loading />
        ) : files.length ? (
          <div className="table-wrap">
            <table className="grid">
              <thead>
                <tr>
                  <th>{t("Nome")}</th>
                  <th className="num">{t("Tamanho")}</th>
                  <th>{t("Modificado")}</th>
                  <th className="act">
                    <span className="sr">{t("Ações")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {files.map((f) => {
                  const full = join(cwd, f.name);
                  const isDir = f.type === "dir";
                  return (
                    <tr key={f.name}>
                      <td>
                        {isDir ? (
                          <a
                            href="#"
                            className="mono"
                            onClick={(e) => {
                              e.preventDefault();
                              go(full);
                            }}
                          >
                            {"📁 " + f.name + "/"}
                          </a>
                        ) : (
                          <span className="mono">{(f.type === "link" ? "↪ " : "") + f.name}</span>
                        )}
                      </td>
                      <td className="num small">{isDir ? "" : f.size < 0 ? "—" : fmtBytes(f.size)}</td>
                      <td className="small muted">{f.mtime ? new Date(f.mtime * 1000).toLocaleString(LOCALE) : ""}</td>
                      <td className="act">
                        <button className="btn small" onClick={() => download(full, isDir)}>
                          {t("Baixar|arquivo")}
                        </button>
                        <button
                          className="btn small danger"
                          onClick={async () => {
                            if (!(await confirm(t("Apagar {path}?", { path: full }), isDir ? t("A pasta e tudo dentro dela são apagados do container.") : t("O arquivo é apagado do container."), t("Apagar"), true))) return;
                            try {
                              await api(qsJoin(base + "/arquivos" + rq, "path=" + enc(full)), { method: "DELETE" });
                              go(cwd);
                            } catch (e) {
                              gantry.toast((e as Error).message, "err");
                            }
                          }}
                        >
                          {t("Apagar")}
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="muted">{t("Pasta vazia.")}</p>
        )}
      </div>
      <p className="hint">{t("Navegar usa o shell do container (precisa estar rodando). Baixar e enviar funcionam com ele parado também. Mudanças fora dos volumes somem quando o container é recriado.")}</p>
    </Modal>
  );
}

const memText = (n: number) => (!n ? "" : n % (1 << 30) === 0 ? n / (1 << 30) + "g" : Math.round(n / (1 << 20)) + "m");

// ctrResourcesDialog changes CPU, memory and restart policy without
// recreating the container.
export function ctrResourcesDialog(host: string, runtime0: string | undefined, ins: any, rt: string | undefined, done?: () => void) {
  const hc = ins.HostConfig || {};
  const restart0 = (hc.RestartPolicy && hc.RestartPolicy.Name) || "no";
  const runtime = rt || runtime0 || "docker";
  const note =
    runtime === "podman"
      ? t(" No Podman, a política de reinício só muda sem recriar a partir da versão 5.1, e o Podman 4 pode continuar mostrando os limites antigos (os novos valem mesmo assim).")
      : runtime !== "docker"
        ? t(" No containerd (nerdctl), os valores atuais não aparecem aqui: preencha só o que quer mudar.")
        : "";
  const labels = (ins.Config && ins.Config.Labels) || {};
  const compose = !!labels["com.docker.compose.project"];
  formDialog({
    title: t("Recursos de {name}", { name: (ins.Name || "").replace(/^\//, "") }),
    intro:
      t("Muda na hora, sem recriar o container. Campos vazios ficam como estão.") +
      (compose ? t(" É um container de projeto: ao recriar (Subir depois de mudar o compose), valem os limites do compose; para manter, ajuste o compose também.") : "") +
      note,
    submit: t("Aplicar"),
    fields: [
      { id: "cpus", label: "CPUs", placeholder: "1.5", value: hc.NanoCpus ? String(hc.NanoCpus / 1e9) : "", optional: true, hint: t("Quantos núcleos ele pode usar no máximo (0.5 = meio núcleo).") },
      { id: "memory", label: t("Memória máxima"), placeholder: "512m", value: memText(hc.Memory), optional: true, hint: t("Acima disso o container é encerrado por falta de memória. Ex.: 512m, 2g.") },
      { id: "reservation", label: t("Memória reservada"), placeholder: "256m", value: memText(hc.MemoryReservation), optional: true, hint: t("Limite brando, usado quando a máquina está apertada.") },
      { id: "pids", label: t("Máximo de processos"), placeholder: "512", value: hc.PidsLimit > 0 ? String(hc.PidsLimit) : "", optional: true },
      {
        id: "restart",
        label: t("Reiniciar automaticamente"),
        type: "select",
        value: restart0,
        options: [
          ["no", t("Não")],
          ["unless-stopped", t("Sempre, a menos que eu pare")],
          ["always", t("Sempre")],
          ["on-failure", t("Só se falhar")],
        ],
      },
    ],
    onSubmit: async (v) => {
      const r = await api<{ warnings?: string[] }>("/hosts/" + enc(host) + "/containers/" + enc(ins.Id) + "/recursos" + rtQuery(rt), {
        method: "POST",
        body: { cpus: v.cpus, memory: v.memory, reservation: v.reservation, pids: v.pids ? parseInt(String(v.pids), 10) || 0 : 0, restart: v.restart !== restart0 ? v.restart : "" },
      });
      gantry.toast(r.warnings && r.warnings.length ? t("Recursos aplicados: {warnings}", { warnings: r.warnings.join("; ") }) : t("Recursos aplicados"), "ok");
      done?.();
    },
  });
}

// ctrCommitDialog saves the container as a new image.
export function ctrCommitDialog(host: string, ins: any, rt?: string) {
  const name = (ins.Name || "").replace(/^\//, "");
  const img = ((ins.Config && ins.Config.Image) || name).replace(/@.*/, "").replace(/:[^/:]*$/, "");
  formDialog({
    title: t("Criar imagem de {name}", { name }),
    intro: t("Grava o estado atual do container (arquivos fora dos volumes) numa imagem nova. O container é pausado por um instante."),
    submit: t("Criar imagem"),
    fields: [
      { id: "ref", label: t("Nome da imagem"), value: img + ":snapshot-" + new Date().toISOString().slice(0, 10), placeholder: t("meu-app:snapshot") },
      { id: "comment", label: t("Comentário"), optional: true, placeholder: t("antes da migração") },
    ],
    onSubmit: async (v) => {
      await api("/hosts/" + enc(host) + "/containers/" + enc(ins.Id) + "/commit" + rtQuery(rt), { method: "POST", body: v });
      gantry.toast(t("Imagem {ref} criada", { ref: String(v.ref) }), "ok");
      loadResources(host, "images", rt);
    },
  });
}

const EVENT_PT: Record<string, string> = {
  create: t("criado"),
  start: t("iniciado"),
  stop: t("parado"),
  die: t("encerrou"),
  kill: t("sinal enviado"),
  restart: t("reiniciado"),
  pause: t("pausado"),
  unpause: t("retomado"),
  destroy: t("removido"),
  oom: t("sem memória (OOM)"),
  update: t("recursos alterados"),
  rename: t("renomeado"),
  commit: t("virou imagem"),
  pull: t("baixada"),
  push: t("enviada"),
  tag: t("novo nome"),
  untag: t("nome removido"),
  delete: t("apagada"),
  connect: t("conectado"),
  disconnect: t("desconectado"),
  mount: t("montado"),
  unmount: t("desmontado"),
  import: t("importada"),
  load: t("carregada"),
  save: t("salva"),
  copy: t("cópia"),
  export: t("exportado"),
};
const TYPE_PT: Record<string, string> = { container: "container", image: t("imagem"), volume: t("volume"), network: t("rede") };

// eventsDialog lists what happened on a machine (or to one container).
export function eventsDialog(host: string, opts: { container?: string; title?: string; rt?: string } = {}) {
  openDialog((close) => <Events host={host} {...opts} close={close} />);
}

type Ev = { time: string; type: string; name: string; action: string; detail?: string };

function Events({ host, container, title, rt, close }: { host: string; container?: string; title?: string; rt?: string; close: () => void }) {
  const [range, setRange] = useState("86400");
  const [kind, setKind] = useState("");
  const [list, setList] = useState<Ev[] | null>(null);
  const [err, setErr] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    let q = "since=" + range;
    if (container) q += "&container=" + enc(container);
    if (kind) q += "&type=" + kind;
    setList(null);
    setErr("");
    api<Ev[]>(qsJoin("/hosts/" + enc(host) + "/eventos" + rtQuery(rt), q))
      .then(setList)
      .catch((e) => setErr((e as Error).message));
  }, [range, kind, n]); // eslint-disable-line react-hooks/exhaustive-deps
  const bad = (a: string) => a === "die" || a === "oom" || a === "kill" || a === "destroy";
  return (
    <Modal
      title={title || t("Eventos de {name}", { name: host })}
      wide
      onClose={close}
      actions={
        <button className="btn" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      <div className="tools-row">
        <select className="select small" aria-label={t("Período")} value={range} onChange={(e) => setRange(e.target.value)}>
          <option value="3600">{t("Última hora")}</option>
          <option value="86400">{t("Últimas 24 h")}</option>
          <option value="604800">{t("Últimos 7 dias")}</option>
        </select>
        {container ? null : (
          <select className="select small" aria-label={t("Tipo")} value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">{t("Tudo")}</option>
            <option value="container">{t("Containers")}</option>
            <option value="image">{t("Imagens")}</option>
            <option value="volume">{t("Volumes")}</option>
            <option value="network">{t("Redes")}</option>
          </select>
        )}
        <button className="btn small" onClick={() => setN(n + 1)}>
          {t("Atualizar")}
        </button>
      </div>
      {err ? (
        <p className="flash flash-err">{err}</p>
      ) : !list ? (
        <Loading />
      ) : !list.length ? (
        <p className="muted">{t("Nada aconteceu neste período.")}</p>
      ) : (
        <div className="table-wrap events-list">
          <table className="grid">
            <thead>
              <tr>
                <th>{t("Quando")}</th>
                {container ? null : <th>{t("O quê")}</th>}
                <th>{t("Evento")}</th>
                <th>{t("Detalhe")}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((e, i) => (
                <tr key={i}>
                  <td className="small mono">{new Date(e.time).toLocaleString(LOCALE)}</td>
                  {container ? null : (
                    <td>
                      <span className="muted small">{TYPE_PT[e.type] || e.type}</span> <b>{e.name}</b>
                    </td>
                  )}
                  <td>
                    <span className={"badge" + (bad(e.action) ? " warn" : "")}>{EVENT_PT[e.action] || e.action}</span>
                  </td>
                  <td className="small muted">{e.detail || ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="hint">{t("O engine guarda os eventos por um tempo limitado (e os perde ao reiniciar). Comandos no shell e health checks não aparecem.")}</p>
    </Modal>
  );
}

/* images */

type Img = { id: string; name: string };

export function imageHistoryDialog(host: string, x: Img, rt?: string) {
  openDialog((close) => <History host={host} x={x} rt={rt} close={close} />);
}

function History({ host, x, rt, close }: { host: string; x: Img; rt?: string; close: () => void }) {
  const [list, setList] = useState<{ createdBy: string; comment?: string; size: number; created: number }[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    api<{ createdBy: string; comment?: string; size: number; created: number }[]>("/hosts/" + enc(host) + "/images/" + enc(x.id) + "/historico" + rtQuery(rt))
      .then(setList)
      .catch((e) => setErr((e as Error).message));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const clean = (s: string) => (s || "").replace(/^\/bin\/sh -c (#\(nop\)\s*)?/, "").trim();
  return (
    <Modal
      title={t("Camadas de {name}", { name: x.name })}
      wide
      onClose={close}
      actions={
        <button className="btn" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      {err ? (
        <p className="flash flash-err">{err}</p>
      ) : !list ? (
        <Loading />
      ) : (
        <div className="table-wrap events-list">
          <table className="grid">
            <thead>
              <tr>
                <th>{t("Instrução")}</th>
                <th className="num">{t("Tamanho")}</th>
                <th>{t("Criada")}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((l, i) => (
                <tr key={i}>
                  <td className="mono small history-cmd" title={l.createdBy}>
                    {clean(l.createdBy) || <span className="muted">—</span>}
                    {l.comment ? <div className="muted">{l.comment}</div> : null}
                  </td>
                  <td className="num small">{l.size ? fmtBytes(l.size) : "0"}</td>
                  <td className="small">{fmtAgo(l.created)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Modal>
  );
}

export function imagePushDialog(host: string, x: Img, rt?: string) {
  const named = x.name && !x.name.startsWith("<");
  formDialog({
    title: named ? t("Enviar {name} para um registry", { name: x.name }) : t("Enviar imagem para um registry"),
    intro: t("O nome diz para onde vai: ghcr.io/usuario/app:1.0, registry.exemplo.com:5000/app:1.0 ou usuario/app:1.0 (Docker Hub). O login vem de Registros (no chaveiro)."),
    submit: t("Enviar"),
    fields: [{ id: "ref", label: t("Nome no registry"), value: named ? x.name : "", placeholder: t("ghcr.io/usuario/app:1.0") }],
    onSubmit: async (v) => {
      const r = await api<{ id: string }>("/hosts/" + enc(host) + "/images/" + enc(x.id) + "/push" + rtQuery(rt), { method: "POST", body: v });
      gantry.openJob(r.id, t("Enviar {name}", { name: String(v.ref) }), () => loadResources(host, "images", rt));
    },
  });
}

export async function imageSaveAs(host: string, x: Img, rt?: string) {
  const named = x.name && !x.name.startsWith("<");
  const file = (named ? x.name : x.id.replace(/^sha256:/, "").slice(0, 12)).replace(/[/:]/g, "_") + ".tar";
  const dest = await gantry.pickPath({ kind: "salvar", title: t("Salvar a imagem"), name: file });
  if (!dest) return;
  try {
    const r = await api<{ id: string }>("/hosts/" + enc(host) + "/images/" + enc(x.id) + "/salvar" + rtQuery(rt), { method: "POST", body: { dest, ref: named ? x.name : "" } });
    gantry.openJob(r.id, named ? t("Salvar {name}", { name: x.name }) : t("Salvar imagem"));
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

export async function imageLoadFrom(host: string, rt?: string) {
  const src = await gantry.pickPath({ kind: "abrir", title: t("Imagem a carregar (.tar do docker save)") });
  if (!src) return;
  try {
    const r = await api<{ id: string }>("/hosts/" + enc(host) + "/images/carregar" + rtQuery(rt), { method: "POST", body: { src } });
    gantry.openJob(r.id, t("Carregar imagem"), () => loadResources(host, "images", rt));
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

/* registries */

export function registriesDialog(host?: string) {
  openDialog((close) => <Registries host={host} close={close} />);
}

function Registries({ host, close }: { host?: string; close: () => void }) {
  const [r, setR] = useState<{ registries: { server: string; user: string }[]; store: string } | null>(null);
  const [err, setErr] = useState("");
  const load = () =>
    api<{ registries: { server: string; user: string }[]; store: string }>("/registros")
      .then(setR)
      .catch((e) => setErr((e as Error).message));
  useEffect(() => {
    load();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Modal
      title={t("Registros de imagens")}
      onClose={close}
      actions={
        <>
          <button className="btn" onClick={close}>
            {t("Fechar")}
          </button>
          <button
            className="btn primary"
            onClick={() => {
              close();
              addRegistryDialog(host);
            }}
          >
            {t("Adicionar")}
          </button>
        </>
      }
    >
      {err ? (
        <p className="flash flash-err">{err}</p>
      ) : !r ? (
        <Loading />
      ) : (
        <>
          {r.registries.length ? (
            <table className="table">
              <tbody>
                {r.registries.map((x) => (
                  <tr key={x.server}>
                    <td className="mono">{x.server}</td>
                    <td className="small">{x.user}</td>
                    <td className="row-actions">
                      {host ? (
                        <button
                          className="btn small"
                          title={t("Grava o login no docker/podman de {host} (para o compose e o terminal de lá)", { host })}
                          onClick={async (e) => {
                            const b = e.currentTarget;
                            b.disabled = true;
                            try {
                              await api("/hosts/" + enc(host) + "/registros/" + enc(x.server) + "/login", { method: "POST" });
                              gantry.toast(t("Login feito em {host}", { host }), "ok");
                            } catch (err) {
                              gantry.toast((err as Error).message, "err");
                            }
                            b.disabled = false;
                          }}
                        >
                          {t("Login em {host}", { host })}
                        </button>
                      ) : null}
                      <button
                        className="btn small danger"
                        onClick={async () => {
                          if (!(await confirm(t("Esquecer o login de {server}?", { server: x.server }), t("A senha sai do chaveiro. Logins já gravados nas máquinas continuam lá."), t("Esquecer"), true))) return;
                          try {
                            await api("/registros/" + enc(x.server), { method: "DELETE" });
                            load();
                          } catch (err) {
                            gantry.toast((err as Error).message, "err");
                          }
                        }}
                      >
                        {t("Esquecer")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <p className="muted small">{t("Nenhum registry cadastrado. Imagens públicas não precisam.")}</p>
          )}
          <p className="hint">{t("As senhas ficam em: {store}. O Gantry usa o login ao baixar e enviar imagens; \"Login em\" grava também na linha de comando da máquina.", { store: r.store })}</p>
        </>
      )}
    </Modal>
  );
}

function addRegistryDialog(host?: string) {
  formDialog({
    title: t("Adicionar registry"),
    submit: t("Salvar"),
    intro: t("Para ghcr.io, use um token com permissão de pacotes no lugar da senha."),
    fields: [
      { id: "server", label: "Registry", placeholder: "ghcr.io", hint: t("docker.io para o Docker Hub; registry.exemplo.com:5000 para um próprio.") },
      { id: "user", label: t("Usuário") },
      { id: "password", label: t("Senha ou token"), type: "password" },
    ],
    onSubmit: async (v) => {
      await api("/registros", { method: "POST", body: { check: host || "", ...v } });
      gantry.toast(t("Login de {server} guardado", { server: String(v.server) }), "ok");
      registriesDialog(host);
    },
  });
}
