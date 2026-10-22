/* eslint-disable @typescript-eslint/no-explicit-any */
import { registriesDialog } from "@/views/container/tools";
import { useEffect, useState } from "react";
import { api, apiURL, enc } from "@/lib/api";
import { CFG } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { copyText } from "@/lib/format";
import { Modal, confirm, formDialog, openDialog } from "@/components/dialog";
import { kubeHref, type Cluster } from "@/views/kube/types";
import { FolderSelect, folderById, itemFolder, moveToFolder, type Groups } from "./folders";
import { addClusterDialog } from "./addCluster";
import { langSetting, setLang, t, tn } from "@/lib/i18n";
import { hostImages } from "@/views/shared/vmimages";

export type MachineInfo = { key: string; local: boolean; name: string; href: string; url: string; kind: string; source: string; engines: number; primary: any };

const KIND_TXT: Record<string, string> = { ssh: "SSH", tcp: "TCP", wsl: "WSL2", unix: "socket" };

export function MachinesTab({ machines, groups }: { machines: MachineInfo[]; groups: Groups }) {
  return (
    <div className="block">
      <div className="block-head">
        <h2 className="block-title">{t("Máquinas")}</h2>
        <span className="grow" />
        <button className="btn primary" type="button" onClick={() => gantry.openHostDialog()}>
          {t("＋ Adicionar máquina")}
        </button>
      </div>
      <div className="panel table-wrap">
        <table className="grid cfg-grid">
          <thead>
            <tr>
              <th>{t("Máquina")}</th>
              <th>{t("Conexão")}</th>
              <th>{t("Pasta")}</th>
              <th className="act">
                <span className="sr">{t("Ações")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {machines.map((m) => {
              const cfg = m.source === "config";
              return (
                <tr key={m.key}>
                  <td>
                    <a href={m.href}>
                      <b>{m.name}</b>
                    </a>
                    <div className="muted small mono">{m.local ? t("este computador") : m.url}</div>
                  </td>
                  <td className="small">
                    {m.local ? t("local") : KIND_TXT[m.kind] ?? m.kind}
                    {m.engines > 1 ? t(" · {n} engines", { n: m.engines }) : ""}
                  </td>
                  <td>
                    <FolderSelect g={groups} label={t("Pasta de {name}", { name: m.name })} value={itemFolder(groups, m.key)} onChange={(v) => moveToFolder(m.key, v)} />
                  </td>
                  <td className="act">
                    {cfg ? (
                      <>
                        <button className="btn small" type="button" onClick={() => gantry.openHostDialog(m.primary)}>
                          {t("Editar")}
                        </button>
                        <button className="btn small danger" type="button" onClick={() => gantry.removeHost(m.primary.name)}>
                          {t("Remover")}
                        </button>
                      </>
                    ) : (
                      <span className="muted small" title={t("Encontrada sozinha neste computador")}>
                        {m.local ? "" : t("detectada")}
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="hint">{t("Servidores entram por SSH (ssh://alias do seu ~/.ssh/config), por um socket, por tcp:// ou, no Windows, pelo WSL2. Eles só conectam quando você os abre.")}</p>
    </div>
  );
}

const MODE_TXT: Record<string, string> = { leitura: t("só leitura"), livre: t("normal") };
const SOURCE_TXT = (c: Cluster) => ({ kubeconfig: "kubeconfig: " + c.file, colado: t("kubeconfig colado (cópia do Gantry)"), gantry: t("criado pelo Gantry") })[c.source] ?? c.source;

export function ClustersTab({ clusters, groups }: { clusters: Cluster[]; groups: Groups }) {
  const reload = async () => {
    await gantry.loadClusters();
  };
  return (
    <div className="block">
      <div className="block-head">
        <h2 className="block-title">{t("Clusters Kubernetes")}</h2>
        <span className="grow" />
        <button className="btn primary" type="button" onClick={() => addClusterDialog(groups)}>
          {t("＋ Adicionar cluster")}
        </button>
      </div>
      {!clusters.length ? (
        <p className="muted">
          {t("Nenhum cluster ainda. Adicione um a partir do seu kubeconfig (~/.kube/config) ou colando um kubeconfig. Clusters criados pelo Gantry (projetos k3s) entram sozinhos.")}
        </p>
      ) : (
        <>
          <div className="panel table-wrap">
            <table className="grid cfg-grid">
              <thead>
                <tr>
                  <th>Cluster</th>
                  <th>{t("Origem")}</th>
                  <th>{t("Modo")}</th>
                  <th>{t("Pasta")}</th>
                  <th className="act">
                    <span className="sr">{t("Ações")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {clusters.map((c) => (
                  <tr key={c.id}>
                    <td>
                      <a href={kubeHref(c.id)}>
                        <b>{c.name}</b>
                      </a>
                      <div className="muted small mono">{c.context}</div>
                    </td>
                    <td className="small">{SOURCE_TXT(c)}</td>
                    <td>
                      <select
                        className="select small"
                        value={c.mode}
                        onChange={async (e) => {
                          try {
                            await api("/clusters/" + enc(c.id), { method: "PATCH", body: { mode: e.target.value } });
                            await reload();
                            gantry.toast(t("Modo de {name}: {mode}", { name: c.name, mode: MODE_TXT[e.target.value] }), "ok");
                          } catch (err) {
                            gantry.toast((err as Error).message, "err");
                          }
                        }}
                      >
                        <option value="livre">{t("Normal")}</option>
                        <option value="leitura">{t("Só leitura")}</option>
                      </select>
                    </td>
                    <td>
                      <FolderSelect g={groups} label={t("Pasta de {name}", { name: c.name })} value={itemFolder(groups, "cluster:" + c.id)} onChange={(v) => moveToFolder("cluster:" + c.id, v)} />
                    </td>
                    <td className="act">
                      <button
                        className="btn small"
                        type="button"
                        onClick={() =>
                          formDialog({
                            title: t("Renomear {name}", { name: c.name }),
                            submit: t("Salvar"),
                            fields: [{ id: "name", label: t("Nome"), value: c.name }],
                            onSubmit: async (v) => {
                              await api("/clusters/" + enc(c.id), { method: "PATCH", body: { name: v.name } });
                              await reload();
                            },
                          })
                        }
                      >
                        {t("Renomear")}
                      </button>
                      <button
                        className="btn small danger"
                        type="button"
                        onClick={async () => {
                          if (!(await confirm(t("Tirar {name} do Gantry?", { name: c.name }), c.source === "kubeconfig" ? t("O cluster não muda; só sai da lista (seu kubeconfig continua igual).") : t("O cluster não muda; só sai da lista."), t("Tirar"), true))) return;
                          try {
                            await api("/clusters/" + enc(c.id), { method: "DELETE" });
                            await reload();
                            await gantry.loadGroups();
                          } catch (err) {
                            gantry.toast((err as Error).message, "err");
                          }
                        }}
                      >
                        {t("Remover")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="hint">{t("Normal: tudo o que a sua conta pode fazer no cluster. Só leitura: nada muda no cluster pelo Gantry (sem shell, sem encaminhar portas, sem valores de Secrets).")}</p>
        </>
      )}
    </div>
  );
}

export function FoldersTab({ machines, groups }: { machines: MachineInfo[]; groups: Groups }) {
  const newFolder = (parent: string) =>
    formDialog({
      title: parent ? t("Nova pasta dentro de {name}", { name: folderById(groups, parent)?.name }) : t("Nova pasta"),
      submit: t("Criar"),
      fields: [{ id: "name", label: t("Nome"), placeholder: "Americanas" }],
      onSubmit: async (v) => {
        await api("/grupos", { method: "POST", body: { name: v.name, parent: parent || "" } });
        await gantry.loadGroups();
      },
    });
  const rows: React.ReactNode[] = [];
  const walk = (parent: string, depth: number) => {
    for (const f of groups.folders.filter((x) => (x.parent || "") === parent)) {
      const direct = machines.filter((m) => itemFolder(groups, m.key) === f.id).length;
      const subs = groups.folders.filter((x) => x.parent === f.id).length;
      const empty = !direct && !subs;
      rows.push(
        <tr key={f.id}>
          <td style={{ paddingLeft: 14 + depth * 20 }}>
            <span className="muted">{depth ? "└ " : ""}</span>
            <b>{f.name}</b>
          </td>
          <td className="small muted">
            {[direct ? tn(direct, "{n} máquina", "{n} máquinas") : null, subs ? tn(subs, "{n} subpasta", "{n} subpastas") : null].filter(Boolean).join(" · ") || t("vazia")}
          </td>
          <td>
            <FolderSelect
              g={groups}
              label={t("Mover {name}", { name: f.name })}
              value={f.parent || ""}
              exclude={f.id}
              top={t("— no topo —")}
              onChange={async (v) => {
                try {
                  await api("/grupos/" + enc(f.id), { method: "PATCH", body: { parent: v } });
                  await gantry.loadGroups();
                } catch (err) {
                  gantry.toast((err as Error).message, "err");
                }
              }}
            />
          </td>
          <td className="act">
            <button className="btn small" type="button" onClick={() => newFolder(f.id)}>
              {t("Subpasta")}
            </button>
            <button
              className="btn small"
              type="button"
              onClick={() =>
                formDialog({
                  title: t("Renomear {name}", { name: f.name }),
                  submit: t("Salvar"),
                  fields: [{ id: "name", label: t("Nome"), value: f.name }],
                  onSubmit: async (v) => {
                    await api("/grupos/" + enc(f.id), { method: "PATCH", body: { name: v.name } });
                    await gantry.loadGroups();
                  },
                })
              }
            >
              {t("Renomear")}
            </button>
            <button
              className="btn small danger"
              type="button"
              disabled={!empty}
              title={empty ? "" : t("Mova ou remova o que está dentro antes de apagar")}
              onClick={async () => {
                if (!(await confirm(t("Apagar a pasta {name}?", { name: f.name }), t("Ela está vazia; nenhuma máquina muda."), t("Apagar"), true))) return;
                try {
                  await api("/grupos/" + enc(f.id), { method: "DELETE" });
                  await gantry.loadGroups();
                } catch (err) {
                  gantry.toast((err as Error).message, "err");
                }
              }}
            >
              {t("Apagar")}
            </button>
          </td>
        </tr>,
      );
      walk(f.id, depth + 1);
    }
  };
  walk("", 0);
  return (
    <div className="block">
      <div className="block-head">
        <h2 className="block-title">{t("Pastas")}</h2>
        <span className="grow" />
        <button className="btn primary" type="button" onClick={() => newFolder("")}>
          {t("＋ Nova pasta")}
        </button>
      </div>
      {rows.length ? (
        <div className="panel table-wrap">
          <table className="grid cfg-grid">
            <thead>
              <tr>
                <th>{t("Pasta")}</th>
                <th>{t("Conteúdo")}</th>
                <th>{t("Dentro de")}</th>
                <th className="act">
                  <span className="sr">{t("Ações")}</span>
                </th>
              </tr>
            </thead>
            <tbody>{rows}</tbody>
          </table>
        </div>
      ) : (
        <p className="muted">{t("Nenhuma pasta. As máquinas aparecem soltas na barra lateral.")}</p>
      )}
      <p className="hint">{t("Uma pasta só é apagada vazia: mova as máquinas (na aba Máquinas) e as subpastas antes. Na barra lateral, a 🔍 de cada pasta filtra só dentro dela.")}</p>
    </div>
  );
}

export function CredentialsTab({ firstHost }: { firstHost: any }) {
  const [ssh, setSSH] = useState<{ hosts: string[]; store: string } | null>(null);
  const [err, setErr] = useState("");
  const load = () =>
    api<{ hosts: string[]; store: string }>("/senhas-ssh")
      .then(setSSH)
      .catch((e: Error) => setErr(e.message));
  useEffect(() => {
    void load();
  }, []);
  return (
    <div>
      <div className="panel panel-pad cfg-card">
        <h2>{t("Registros de imagens")}</h2>
        <p className="muted small">{t("Logins de ghcr.io, Docker Hub e registries próprios, usados para baixar e enviar imagens.")}</p>
        <button className="btn" type="button" onClick={() => registriesDialog(firstHost ? firstHost.name : undefined)}>
          {t("Gerenciar registros…")}
        </button>
      </div>
      <div className="panel panel-pad cfg-card">
        <h2>{t("Senhas de SSH desta sessão")}</h2>
        <p className="muted small">{t("Senhas e frases de chave que você digitou ao conectar. Ficam só na memória enquanto o Gantry está aberto.")}</p>
        {err ? (
          <p className="flash flash-err">{err}</p>
        ) : !ssh ? (
          <span className="muted small">
            <span className="spin" /> {t("carregando…")}
          </span>
        ) : ssh.hosts.length ? (
          <table className="table">
            <tbody>
              {ssh.hosts.map((x) => (
                <tr key={x}>
                  <td className="mono">{x}</td>
                  <td className="row-actions">
                    <button
                      className="btn small"
                      type="button"
                      onClick={async () => {
                        await api("/senhas-ssh/" + enc(x), { method: "DELETE" });
                        void load();
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
          <p className="muted small" style={{ margin: 0 }}>
            {t("Nenhuma senha de SSH guardada agora.")}
          </p>
        )}
      </div>
      <div className="panel panel-pad cfg-card">
        <h2>{t("Segredos dos projetos")}</h2>
        <p className="muted small" style={{ margin: 0 }}>
          {t("Senhas e tokens dos .env movidos para o chaveiro ficam em")} <b>{ssh?.store ?? "…"}</b>. {t("Em cada projeto, aba Arquivo: Mover para o chaveiro e Devolver ao .env.")}
        </p>
      </div>
    </div>
  );
}

function setTheme(v: string) {
  const root = document.documentElement;
  if (v === "sistema") {
    root.removeAttribute("data-theme");
    gantry.store.set("theme", null);
  } else {
    root.setAttribute("data-theme", v);
    gantry.store.set("theme", v);
  }
  const btn = document.getElementById("themeToggle");
  if (btn) btn.textContent = v === "dark" || (v === "sistema" && matchMedia("(prefers-color-scheme: dark)").matches) ? "☀️" : "🌙";
}

// MCPCard: the access of assistants (MCP clients)
// through gantry mcp.
function MCPCard() {
  const [st, setSt] = useState<{ on: boolean; write: boolean; command: string; file: string } | null>(null);
  useEffect(() => {
    api<{ on: boolean; write: boolean; command: string; file: string }>("/mcp")
      .then(setSt)
      .catch(() => setSt(null));
  }, []);
  if (!st) return null;
  const save = async (on: boolean, write: boolean) => {
    try {
      setSt(await api("/mcp", { method: "PUT", body: { on, write } }));
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  const snippet = JSON.stringify({ mcpServers: { gantry: { command: st.command, args: ["mcp"] } } }, null, 2);
  return (
    <div className="panel panel-pad cfg-card" id="mcp">
      <h2>{t("Assistentes (MCP)")}</h2>
      <p className="muted small">{t("Um assistente de IA (um cliente MCP) consulta labs, containers, VMs e clusters por este Gantry, enquanto ele está aberto. Secrets e values do Helm nunca saem; clusters só leitura continuam só leitura.")}</p>
      <label className="check">
        <input type="checkbox" checked={st.on} onChange={(e) => void save(e.target.checked, e.target.checked && st.write)} /> {t("Permitir o acesso de assistentes")}
      </label>
      <label className="check">
        <input type="checkbox" disabled={!st.on} checked={st.write} onChange={(e) => void save(st.on, e.target.checked)} /> {t("Permitir também mudar: subir e remover projetos, reiniciar containers, escalar e reiniciar workloads")}
      </label>
      {st.on ? (
        <>
          <p className="muted small">{t("Na configuração de servidores MCP do cliente, acrescente o servidor abaixo. Mudar a permissão de escrita vale quando o cliente reabrir.")}</p>
          <pre className="kd-pre">{snippet}</pre>
          <button className="btn small" type="button" onClick={() => void copyText(snippet)}>
            {t("Copiar")}
          </button>
        </>
      ) : null}
    </div>
  );
}

export function GeneralTab({ machines }: { machines: MachineInfo[] }) {
  const [theme, setT] = useState(() => document.documentElement.getAttribute("data-theme") || "sistema");
  const vmHosts = machines.filter((m) => m.primary && m.kind !== "tcp");
  return (
    <div>
      <div className="panel panel-pad cfg-card">
        <h2>{t("Tema")}</h2>
        <select
          className="select"
          value={theme}
          onChange={(e) => {
            setT(e.target.value);
            setTheme(e.target.value);
          }}
        >
          <option value="sistema">{t("Igual ao sistema")}</option>
          <option value="light">{t("Claro")}</option>
          <option value="dark">{t("Escuro")}</option>
        </select>
      </div>
      <div className="panel panel-pad cfg-card">
        {/* In both languages: who opened the app in the wrong one finds it. */}
        <h2>Idioma · Language</h2>
        <select
          className="select"
          aria-label="Idioma · Language"
          value={langSetting()}
          onChange={async (e) => {
            const v = e.target.value as "" | "pt" | "en";
            try {
              await api("/idioma", { method: "PUT", body: { language: v } });
            } catch (err) {
              gantry.toast((err as Error).message, "err");
              return;
            }
            setLang(v);
          }}
        >
          <option value="">{t("Igual ao sistema")}</option>
          <option value="pt">Português</option>
          <option value="en">English</option>
        </select>
      </div>
      {vmHosts.length ? (
        <div className="panel panel-pad cfg-card" id="imagens-vm">
          <h2>{t("Imagens de VM")}</h2>
          <p className="muted small">{t("Cada máquina tem as suas pastas de imagens, na página dela (Configurações › Imagens): o Gantry acha lá os .qcow2, as ISOs, os .bin do IOL e do Dynamips e o iourc, e reconhece cada um pelo nome.")}</p>
          <table className="grid compact">
            <tbody>
              {vmHosts.map((m) => (
                <ImagesSummary key={m.key} name={m.name} host={m.primary!.name} href={m.href + "/t/config"} />
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <MCPCard />
      <div className="panel panel-pad cfg-card">
        <h2>{t("Versão")}</h2>
        <p className="muted small">{t("Você está com a {version}. O Gantry avisa quando há uma nova (a cada 6 h) e nunca atualiza sozinho.", { version: CFG.version || "dev" })}</p>
        <button className="btn" type="button" onClick={() => gantry.checkAppVersion(true)}>
          {t("Verificar agora")}
        </button>
      </div>
      <div className="panel panel-pad cfg-card">
        <h2>{t("Boas-vindas")}</h2>
        <p className="muted small">{t("A tela do primeiro uso: trazer servidores do ~/.ssh/config e clusters do ~/.kube/config com um clique.")}</p>
        <a className="btn" href="#/bem-vindo">
          {t("Abrir as boas-vindas")}
        </a>
      </div>
      <div className="panel panel-pad cfg-card">
        <h2>{t("Diagnóstico")}</h2>
        <p className="muted small">
          {t("Algo deu errado? O diagnóstico junta a versão, o estado das máquinas, os erros internos e o fim do log, sem endereços, usuários nem senhas. Mande junto com a descrição do problema.")}
        </p>
        <div className="btn-row">
          <button className="btn" type="button" onClick={diagnosticDialog}>
            {t("Ver diagnóstico")}
          </button>
          <button
            className="btn btn-ghost"
            type="button"
            onClick={async () => {
              try {
                await api("/log/abrir", { method: "POST" });
              } catch (e) {
                gantry.toast((e as Error).message, "err");
              }
            }}
          >
            {t("Abrir pasta do log")}
          </button>
        </div>
      </div>
    </div>
  );
}

function Diagnostic({ close }: { close: () => void }) {
  const [text, setText] = useState(() => t("Juntando…"));
  const [ok, setOk] = useState(false);
  useEffect(() => {
    (async () => {
      try {
        const r = await fetch(apiURL("/diagnostico"), { headers: CFG.token ? { Authorization: "Bearer " + CFG.token } : {} });
        const body = await r.text();
        if (!r.ok) throw new Error(body);
        setText(body);
        setOk(true);
      } catch (e) {
        setText(t("Não deu para gerar: {err}", { err: (e as Error).message }));
      }
    })();
  }, []);
  return (
    <Modal
      title={t("Diagnóstico")}
      wide
      onClose={close}
      actions={
        <>
          <button className="btn" type="button" onClick={close}>
            {t("Fechar")}
          </button>
          <button
            className="btn"
            type="button"
            onClick={async () => {
              const dest = await gantry.pickPath({ kind: "salvar", title: t("Salvar diagnóstico"), name: "gantry-diagnostico.md" });
              if (!dest) return;
              try {
                await api("/diagnostico/salvar", { method: "POST", body: { dest } });
                gantry.toast(t("Diagnóstico salvo em {path}", { path: dest }), "ok");
              } catch (e) {
                gantry.toast((e as Error).message, "err");
              }
            }}
          >
            {t("Salvar…")}
          </button>
          <button
            className="btn primary"
            type="button"
            disabled={!ok}
            onClick={async () => gantry.toast((await copyText(text)) ? t("Diagnóstico copiado.") : t("Não deu para copiar; selecione o texto."), "ok")}
          >
            {t("Copiar")}
          </button>
        </>
      }
    >
      <pre className="release-notes diag-text">{text}</pre>
    </Modal>
  );
}

export function diagnosticDialog() {
  openDialog((close) => <Diagnostic close={close} />);
}

// ImagesSummary: how many images a machine has in its folders, with the way
// to its settings.
function ImagesSummary({ name, host, href }: { name: string; host: string; href: string }) {
  const [n, setN] = useState<{ found: number; dirs: string[] } | null>(null);
  useEffect(() => {
    let live = true;
    hostImages(host)
      .then((r) => live && setN({ found: r.found.length, dirs: r.dirs }))
      .catch(() => live && setN(null));
    return () => {
      live = false;
    };
  }, [host]);
  return (
    <tr>
      <td>{name}</td>
      <td className="muted small">{n ? t("{n} imagem(ns) em {dirs}", { n: n.found, dirs: n.dirs.join(", ") || "—" }) : "…"}</td>
      <td className="row-actions">
        <a className="btn small" href={href}>
          {t("Configurar")}
        </a>
      </td>
    </tr>
  );
}
