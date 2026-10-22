// The Arquivo tab of a project: the compose file (and the .env and the
// configs it mounts) in an editor that follows the disk, what differs from
// what runs, the git repository and the secrets.
import { useEffect, useRef, useState } from "react";
import { api, apiURL, APIError } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, type Host } from "@/lib/app";
import { LOCALE, t, tn } from "@/lib/i18n";
import { confirm } from "@/components/dialog";
import { CodeEditor } from "@/components/editor";
import { AutoApplyToggle, NixApplyButton, driftText, setDrift, type Drift } from "./drift";
import { labOp } from "./LabPage";
import type { Lab } from "./types";

type FileRes = { path: string; content: string; hash: string; missing?: boolean; files?: { name: string; label: string; kind?: string }[] };

const KV = ({ k, children }: { k: string; children: React.ReactNode }) => (
  <div>
    <span className="k">{k}</span>
    <span className="v">{children}</span>
  </div>
);

const DRIFT_LABELS: Record<string, [string, string]> = {
  ok: ["on", t("igual ao arquivo")],
  changed: ["warn", t("diferente · aplicar")],
  missing: ["warn", t("ainda não criado")],
  orphan: ["err", t("fora do arquivo")],
  nix: ["warn", t("Configuração Nix alterada")],
};

export function FileTab({ hst, lab }: { hst: Host; lab: Lab }) {
  if (!hst.files) return <div className="banner info">{t("Este host não dá acesso aos arquivos. Conecte-o via ssh:// para editar o compose.")}</div>;
  return <Editor hst={hst} lab={lab} />;
}

function Editor({ hst, lab }: { hst: Host; lab: Lab }) {
  const base = "/hosts/" + enc(hst.name) + "/labs/" + enc(lab.id);
  const [name, setName] = useState("");
  const [files, setFiles] = useState<FileRes["files"]>([]);
  const [path, setPath] = useState("");
  const [text, setText] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [missing, setMissing] = useState(false);
  const [err, setErr] = useState("");
  const [conflict, setConflict] = useState<FileRes | null>(null);
  const [lastRead, setLastRead] = useState<Date | null>(null);
  const [watching, setWatching] = useState(false);
  const [drift, setDriftState] = useState<Drift | null>(null);
  const [gitKey, setGitKey] = useState(0);
  const [diskBase, setDiskBase] = useState("");
  // What the disk had when last read (base, hash), kept in a ref for the
  // polling and the handlers.
  const st = useRef({ base: "", hash: "", name: "", text: "", watching: false, lastRead: 0 });
  st.current.name = name;
  st.current.text = text;
  const dirty = loaded && text !== diskBase;
  const extra = !!(files || []).find((x) => x.name === name && x.kind === "extra");
  const fq = (n: string) => (n ? "?name=" + enc(n) : "");

  const updateDrift = async () => {
    try {
      const d = await api<Drift>(base + "/drift");
      setDriftState(d);
      setDrift(hst.name, lab.id, d);
    } catch (e) {
      setDriftState({ error: (e as Error).message, services: [], pending: false });
    }
  };

  const load = async (force: boolean) => {
    const want = st.current.name;
    try {
      const f = await api<FileRes>(base + "/file" + fq(want));
      if (want !== st.current.name) return;
      if (f.files) setFiles(f.files);
      st.current.lastRead = Date.now();
      setLastRead(new Date());
      const cur = st.current;
      const isDirty = cur.text !== cur.base && !!cur.hash;
      if (!cur.hash || force || (!isDirty && f.hash !== cur.hash)) {
        const changedOnDisk = cur.hash && f.hash !== cur.hash && !force;
        cur.base = f.content;
        cur.hash = f.hash;
        cur.text = f.content;
        setDiskBase(f.content);
        setPath(f.path);
        setText(f.content);
        setLoaded(true);
        setMissing(!!f.missing);
        setConflict(null);
        setErr("");
        if (changedOnDisk) gantry.toast(t("Arquivo atualizado a partir do disco"));
        updateDrift();
      } else if (isDirty && f.hash !== cur.hash) {
        setConflict((c) => c ?? f);
      }
    } catch (e) {
      if (!st.current.hash) setErr((e as Error).message);
    }
  };

  useEffect(() => {
    load(true);
  }, [name]); // eslint-disable-line react-hooks/exhaustive-deps

  // Follow the disk: every 3 s, or by the agent's watch when it runs.
  useEffect(() => {
    const timer = setInterval(() => {
      if (document.hidden) return;
      // With the agent watching, read only now and then as a safety net.
      if (st.current.watching && st.current.lastRead && Date.now() - st.current.lastRead < 30000) return;
      load(false);
    }, 3000);
    let es: EventSource | null = null;
    if (hst.agent && hst.agent.state === "running") {
      es = new EventSource(apiURL(base + "/watch", true));
      es.addEventListener("ready", () => {
        st.current.watching = true;
        setWatching(true);
      });
      es.addEventListener("change", () => load(false));
      const stop = () => {
        es?.close();
        st.current.watching = false;
        setWatching(false);
      };
      es.addEventListener("end", stop);
      es.onerror = stop;
    }
    const dtimer = setInterval(() => {
      if (!document.hidden) updateDrift();
    }, 5000);
    return () => {
      clearInterval(timer);
      clearInterval(dtimer);
      es?.close();
    };
  }, [hst.name, lab.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async (apply: boolean) => {
    if (dirty) {
      try {
        const r = await api<{ hash: string }>(base + "/file" + fq(name), { method: "PUT", body: { content: text, hash: st.current.hash } });
        st.current.base = text;
        st.current.hash = r.hash;
        setDiskBase(text);
        setConflict(null);
        setMissing(false);
        setLoaded(true);
        gantry.toast(t("Arquivo salvo"), "ok");
      } catch (e) {
        if (e instanceof APIError && e.status === 409 && e.data) {
          setConflict(e.data as FileRes);
          return;
        }
        gantry.toast((e as Error).message, "err");
        return;
      }
    }
    if (apply && extra) {
      try {
        const r = await api<{ ok: boolean; error?: string; services?: string[] }>(base + "/file/restart" + fq(name), { method: "POST" });
        gantry.showOutput(hst.name + ": " + lab.name + " · " + t("reiniciar"), r);
        gantry.toast(r.ok ? (r.services && r.services.length ? t("Reiniciado: {list}", { list: r.services.join(", ") }) : t("Nenhum serviço usa este arquivo")) : r.error || t("falhou"), r.ok ? "ok" : "err");
      } catch (e) {
        gantry.toast((e as Error).message, "err");
      }
      return;
    }
    if (apply) await labOp(hst.name, lab, "up", t("Aplicar"));
    updateDrift();
  };

  return (
    <div>
      <div id="file-banner">
        {err ? <div className="banner err">{err}</div> : null}
        {conflict ? (
          <div className="banner" id="conflict">
            <span className="grow">{t("O arquivo mudou no disco enquanto você editava.")}</span>
            <button
              className="btn small"
              onClick={() => {
                st.current.hash = "";
                load(true);
              }}
            >
              {t("Descartar as minhas e recarregar")}
            </button>
            <button
              className="btn small"
              onClick={() => {
                st.current.hash = conflict.hash;
                setConflict(null);
              }}
            >
              {t("Manter as minhas")}
            </button>
          </div>
        ) : null}
      </div>
      <div className="editor-layout">
        <section className="editor" aria-label={t("Editor do compose")}>
          <div className="editor-bar">
            <span className="file-pick">
              <select
                className="select small file-sel"
                aria-label={t("Arquivo")}
                value={name}
                onChange={async (e) => {
                  const v = e.target.value;
                  if (dirty && !(await confirm(t("Trocar de arquivo?"), t("As alterações não salvas deste arquivo serão perdidas."), t("Trocar|arquivo"), true))) return;
                  st.current.hash = "";
                  st.current.base = "";
                  setDiskBase("");
                  setLoaded(false);
                  setName(v);
                }}
              >
                {(files || []).map((x) => (
                  <option key={x.name} value={x.name}>
                    {x.label}
                  </option>
                ))}
              </select>
              <span className="mono">{path}</span>
            </span>
            <span style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
              <span>{!loaded ? (err ? t("erro") : t("carregando…")) : missing && !dirty ? t("arquivo novo") : dirty ? t("alterações não salvas") : t("salvo")}</span>
              <button className="btn small ghost" onClick={() => load(true)}>
                {t("Recarregar")}
              </button>
              <button className="btn small" disabled={!dirty} onClick={() => save(false)}>
                {t("Salvar")}
              </button>
              <button className="btn small primary" disabled={!loaded} title={extra ? t("Reinicia os serviços que montam este arquivo") : ""} onClick={() => save(true)}>
                {extra ? t("Salvar e reiniciar") : t("Salvar e aplicar")}
              </button>
            </span>
          </div>
          <CodeEditor
            value={text}
            onChange={setText}
            readOnly={!loaded}
            label={t("Conteúdo do arquivo compose")}
            file={name}
            onSave={() => save(false)}
            placeholder={missing && name === ".env" ? t("CHAVE=valor\nOutra=coisa\n\nO compose usa estes valores em ${CHAVE}. Salve para criar o arquivo.") : ""}
            bare
          />
        </section>
        <div className="side-panels">
          <div className="panel panel-pad">
            <div className="block-head" style={{ marginBottom: 6 }}>
              <h2>{t("Serviços × arquivo")}</h2>
            </div>
            <div className="svc-list" id="drift-list">
              {!drift ? (
                <span className="muted small">{t("Comparando…")}</span>
              ) : drift.error ? (
                <span className="muted small">{t("Não foi possível comparar: {err}", { err: drift.error })}</span>
              ) : !drift.services.length ? (
                <span className="muted small">{t("Nenhum serviço no arquivo.")}</span>
              ) : (
                drift.services.map((s) => {
                  const [dot, label0] = DRIFT_LABELS[s.state] || ["", s.state];
                  const label = s.kind === "vm" ? (s.state === "ok" ? t("VM igual ao arquivo") : driftText(s)) : label0;
                  return (
                    <div key={s.service}>
                      <span className={"dot " + dot} />
                      <span className="name">{s.service}</span>
                      <span className={"small " + (s.state === "ok" ? "muted" : "")} style={s.state === "ok" ? undefined : { color: "var(--warn-text)" }}>
                        {label}
                      </span>
                      {s.state === "nix" ? <NixApplyButton host={hst.name} lab={lab} item={s} /> : null}
                    </div>
                  );
                })
              )}
            </div>
            <div style={{ marginTop: 10 }}>
              <AutoApplyToggle host={hst.name} lab={lab} />
            </div>
          </div>
          <div className="panel panel-pad">
            <h2 style={{ marginBottom: 6 }}>{t("Sincronização")}</h2>
            <div className="kv">
              <KV k={t("Pasta")}>{lab.workingDir || "—"}</KV>
              <KV k={t("Arquivo")}>{path ? path.split("/").pop() : "—"}</KV>
              <KV k={t("Observando")}>{watching ? t("na hora, pelo agente (inotify)") : hst.kind === "ssh" ? t("via SSH a cada 3 s") : t("a cada 3 s")}</KV>
              <KV k={t("Última leitura")}>{lastRead ? lastRead.toLocaleTimeString(LOCALE) : "—"}</KV>
            </div>
          </div>
          <GitPanel
            key={gitKey}
            base={base}
            hst={hst}
            lab={lab}
            dirty={dirty}
            after={() => {
              st.current.hash = "";
              load(true);
              setGitKey((k) => k + 1);
              gantry.loadLabs(hst.name);
            }}
          />
          <SecretsPanel base={base} onChange={() => name === ".env" && load(true)} />
          <p className="muted small" style={{ margin: 0 }}>
            {t("Salvar só grava o arquivo. Aplicar roda")} <code>compose up -d</code>{t(", que recria apenas os serviços que mudaram.")}
          </p>
        </div>
      </div>
    </div>
  );
}

type Git = { repo: boolean; url?: string; branch?: string; commit?: string; when?: string; subject?: string; dirty?: boolean; behind?: number; fetchError?: string };

function GitPanel({ base, hst, lab, dirty, after }: { base: string; hst: Host; lab: Lab; dirty: boolean; after: () => void }) {
  const [g, setG] = useState<Git | null>(null);
  const [up, setUp] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    api<Git>(base + "/git")
      .then((r) => {
        if (!live) return;
        setG(r);
        if (r.repo && r.url)
          api<Git>(base + "/git?remoto=1")
            .then((x) => live && setG(x))
            .catch(() => {});
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [base]);
  if (!g || !g.repo) return null;
  return (
    <div className="panel panel-pad">
      <h2 style={{ marginBottom: 6 }}>{t("Repositório git")}</h2>
      <div className="kv">
        {g.url ? <KV k={t("Origem")}>{g.url.replace(/^https?:\/\//, "")}</KV> : null}
        <KV k="Branch">{g.branch || "—"}</KV>
        <KV k="Commit">{(g.commit || "—") + (g.when ? " · " + g.when : "")}</KV>
        {g.subject ? <KV k={t("Mensagem")}>{g.subject}</KV> : null}
        {g.dirty ? <KV k={t("Alterações locais")}>{t("sim (o pull pode falhar)")}</KV> : null}
        {g.url ? (
          <KV k={t("No remoto")}>
            {(g.behind ?? -1) > 0 ? (
              <span className="badge warn">{tn(g.behind!, "{n} commit novo", "{n} commits novos")}</span>
            ) : g.fetchError ? (
              <span className="muted small">{g.fetchError}</span>
            ) : g.behind === 0 ? (
              t("em dia")
            ) : (
              <span className="muted small">{t("verificando…")}</span>
            )}
          </KV>
        ) : null}
      </div>
      <div className="git-actions">
        <label className="check">
          <input type="checkbox" checked={up} onChange={(e) => setUp(e.target.checked)} /> {t("subir de novo depois")}
        </label>
        <button
          className="btn small primary"
          disabled={busy}
          onClick={async () => {
            if (dirty && !(await confirm(t("Atualizar do git?"), t("Há alterações não salvas no editor; o arquivo será recarregado."), t("Atualizar|git")))) return;
            setBusy(true);
            try {
              const r = await api<{ ok: boolean; error?: string }>(base + "/git/pull", { method: "POST", body: { up } });
              gantry.showOutput(hst.name + ": " + lab.name + " · git", r);
              gantry.toast(r.ok ? (up ? t("Atualizado do git e aplicado") : t("Atualizado do git")) : r.error || t("falhou"), r.ok ? "ok" : "err");
            } catch (e) {
              gantry.toast((e as Error).message, "err");
            }
            setBusy(false);
            after();
          }}
        >
          {busy ? (
            <>
              <span className="spin" />
              {t("Atualizando…")}
            </>
          ) : (
            t("Atualizar do git")
          )}
        </button>
      </div>
    </div>
  );
}

// SecretsPanel moves passwords and tokens of the project's .env to the
// keychain of this computer, and back.
function SecretsPanel({ base, onChange }: { base: string; onChange: () => void }) {
  const url = base + "/segredos";
  const [d, setD] = useState<{ store: string; stored?: string[]; candidates?: string[]; error?: string } | null>(null);
  const [err, setErr] = useState("");
  const [picks, setPicks] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState(false);
  const load = async () => {
    try {
      const r = await api<{ store: string; stored?: string[]; candidates?: string[]; error?: string }>(url);
      setD(r);
      setPicks(Object.fromEntries((r.candidates || []).map((k) => [k, true])));
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  useEffect(() => {
    load();
  }, [url]); // eslint-disable-line react-hooks/exhaustive-deps
  const move = async (keys: string[], back: boolean) => {
    setBusy(true);
    try {
      const r = await api<{ moved?: string[]; store?: string }>(url, { method: "POST", body: { keys, back } });
      gantry.toast(back ? t("Devolvidos ao .env") : tn((r.moved || []).length, "{n} movido para o {store}", "{n} movidos para o {store}", { store: r.store || t("chaveiro") }), "ok");
      await load();
      onChange();
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
    setBusy(false);
  };
  const cands = d?.candidates || [];
  return (
    <div className="panel panel-pad">
      <h2 style={{ marginBottom: 6 }}>{t("Segredos")}</h2>
      {err ? (
        <p className="muted small">{err}</p>
      ) : !d ? (
        <span className="muted small">{t("carregando…")}</span>
      ) : (
        <>
          <p className="muted small" style={{ margin: "0 0 8px" }}>
            {t("Senhas e tokens podem ficar fora do .env, no {store}. O Gantry os entrega ao compose na hora de subir; não aparecem no arquivo nem na linha de comando do servidor.", { store: d.store })}
          </p>
          {d.error ? <p className="flash flash-err">{d.error}</p> : null}
          {(d.stored || []).length ? (
            <div style={{ marginBottom: 8 }}>
              <div className="muted small">{t("No chaveiro:")}</div>
              <div style={{ display: "flex", gap: 6, flexWrap: "wrap", margin: "4px 0" }}>
                {d.stored!.map((k) => (
                  <span key={k} className="badge accent mono">
                    {k}
                  </span>
                ))}
              </div>
              <button className="btn small" disabled={busy} onClick={() => move(d.stored!, true)}>
                {t("Devolver ao .env")}
              </button>
            </div>
          ) : null}
          {cands.length ? (
            <div>
              <div className="muted small">{t("No .env:")}</div>
              {cands.map((k) => (
                <label key={k} className="check" style={{ display: "flex", gap: 8, alignItems: "center" }}>
                  <input type="checkbox" checked={!!picks[k]} onChange={(e) => setPicks({ ...picks, [k]: e.target.checked })} />
                  <span className="mono">{k}</span>
                </label>
              ))}
              <button
                className="btn small"
                style={{ marginTop: 6 }}
                disabled={busy}
                onClick={() =>
                  move(
                    cands.filter((k) => picks[k]),
                    false,
                  )
                }
              >
                {t("Mover para o chaveiro")}
              </button>
            </div>
          ) : (d.stored || []).length ? null : (
            <p className="muted small" style={{ margin: 0 }}>
              {t("Nenhuma senha ou token no .env.")}

            </p>
          )}
        </>
      )}
    </div>
  );
}
