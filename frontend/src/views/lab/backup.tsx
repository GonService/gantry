// Backups of a project: now, on a schedule, and restoring.
import { useEffect, useState } from "react";
import { create } from "zustand";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, type Host } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { LOCALE, t, tn } from "@/lib/i18n";
import { confirm, Modal, openDialog } from "@/components/dialog";
import { ErrorBox, Loading } from "@/components/ui";
import type { Lab } from "./types";

type Manifest = { created?: number; volumes?: string[]; dumps?: string[]; vms?: string[]; files?: boolean; env?: boolean; stopped?: boolean };
type Item = { name: string; size: number; created: number; manifest?: Manifest; path?: string; label?: string };
type Schedule = { every?: number; keep?: number; stop?: boolean; env?: boolean; last?: number; lastErr?: string; dir?: string };
type Data = { dir: string; chosen: boolean; schedule?: Schedule; items: Item[] };
type Entry = { data?: Data; err?: string; at: number; loading?: boolean };

const useBK = create<Record<string, Entry>>(() => ({}));

async function loadBackups(host: string, lab: string) {
  const k = host + "/" + lab;
  useBK.setState((s) => ({ [k]: { ...(s[k] ?? { at: 0 }), loading: true } }));
  try {
    useBK.setState({ [k]: { data: await api<Data>("/hosts/" + enc(host) + "/labs/" + enc(lab) + "/backups"), at: Date.now() } });
  } catch (e) {
    useBK.setState({ [k]: { err: (e as Error).message, at: Date.now() } });
  }
}

const KV = ({ k, children }: { k: string; children: React.ReactNode }) => (
  <div>
    <span className="k">{k}</span>
    <span className="v">{children}</span>
  </div>
);

const Check = ({ checked, onChange, children, top }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode; top?: boolean }) => (
  <label className="check" style={{ display: "flex", gap: 8, alignItems: "center", marginTop: top ? 4 : undefined }}>
    <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
    {children}
  </label>
);

function RestoreDialog({ it, onOK, close }: { it: Item; onOK: (files: boolean) => void; close: () => void }) {
  const [files, setFiles] = useState(false);
  return (
    <Modal
      title={t("Restaurar {name}?", { name: it.label || it.name })}
      onClose={close}
      actions={
        <>
          <button className="btn btn-ghost" onClick={close}>
            {t("Cancelar")}
          </button>
          <button
            className="btn danger-solid"
            onClick={() => {
              close();
              onOK(files);
            }}
          >
            {t("Restaurar")}
          </button>
        </>
      }
    >
      <p>{t("Os containers do projeto são removidos e criados de novo, e os volumes voltam ao conteúdo do backup. O que foi gravado depois dele é perdido.")}</p>
      <Check checked={files} onChange={setFiles}>
        {t("Restaurar também os arquivos do projeto (compose e configs{env})", { env: it.manifest?.env ? ", .env" : "" })}
      </Check>
    </Modal>
  );
}

export function BackupTab({ hst, lab, busy }: { hst: Host; lab: Lab; busy?: string }) {
  const k = hst.name + "/" + lab.id;
  const st = useBK((s) => s[k]);
  const [stop, setStop] = useState(false);
  const [env, setEnv] = useState(false);
  const [vm, setVm] = useState(true);
  const [starting, setStarting] = useState(false);
  useEffect(() => {
    if (!st || (!st.loading && Date.now() - (st.at || 0) > 15000)) loadBackups(hst.name, lab.id);
  }, [k]); // eslint-disable-line react-hooks/exhaustive-deps
  const base = "/hosts/" + enc(hst.name) + "/labs/" + enc(lab.id) + "/backups";
  const isBusy = !!busy;
  const hasVMs = (lab.vms || []).some((v) => v.state !== "missing");
  const d = st?.data;
  const chooseDir = () => gantry.pickPath({ kind: "pasta", title: t("Onde guardar os backups de {name}", { name: lab.name }), dir: d ? d.dir : "~" });
  const reload = () => loadBackups(hst.name, lab.id);
  const now = async () => {
    setStarting(true);
    let dir = "";
    if (!d?.chosen) {
      dir = (await chooseDir()) || "";
      if (!dir) {
        setStarting(false);
        return;
      }
    }
    try {
      const r = await api<{ id: string }>(base, { method: "POST", body: { stop, env, noVms: hasVMs && !vm, dir } });
      gantry.markBusy(k, "backup");
      gantry.openJob(r.id, "Backup · " + lab.name, async () => {
        gantry.markBusy(k, null);
        await reload();
      });
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
    setStarting(false);
  };
  const restore = (it: Item) =>
    openDialog((close) => (
      <RestoreDialog
        it={it}
        close={close}
        onOK={async (files) => {
          try {
            const r = await api<{ id: string }>(base + "/" + enc(it.name) + "/restore" + (it.path ? "?path=" + enc(it.path) : ""), { method: "POST", body: { files } });
            gantry.markBusy(k, "restore");
            gantry.openJob(r.id, t("Restaurar · {name}", { name: lab.name }), async () => {
              gantry.markBusy(k, null);
              await gantry.loadLabs(hst.name);
              await reload();
            });
          } catch (e) {
            gantry.toast((e as Error).message, "err");
          }
        }}
      />
    ));
  const del = async (it: Item) => {
    if (!(await confirm(t("Apagar {name}?", { name: it.name }), t("O arquivo do backup é apagado deste computador."), t("Apagar"), true))) return;
    try {
      await api(base + "/" + enc(it.name), { method: "DELETE" });
      reload();
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  return (
    <div id="backup-box">
      <div className="block">
        <div className="block-head">
          <h2 className="block-title">{t("Fazer backup")}</h2>
        </div>
        <div className="panel panel-pad">
          <p style={{ margin: "0 0 8px" }}>
            {t("Copia os volumes do projeto, um dump de cada banco que ele roda (PostgreSQL, MySQL/MariaDB, MongoDB) e a pasta do projeto num .tar.gz, na pasta que você escolher deste computador.")}
          </p>
          {d ? (
            <div className="kv" style={{ marginBottom: 8 }}>
              <KV k={t("Pasta dos backups")}>
                <span className="mono small">{d.chosen ? d.dir : t("ainda não escolhida")}</span>{" "}
                <button
                  className="btn small"
                  onClick={async () => {
                    const dir = await chooseDir();
                    if (!dir) return;
                    try {
                      await api(base + "/agenda", { method: "PUT", body: { ...(d.schedule || {}), dir } });
                      reload();
                    } catch (e) {
                      gantry.toast((e as Error).message, "err");
                    }
                  }}
                >
                  {d.chosen ? t("Trocar…") : t("Escolher…")}
                </button>
              </KV>
            </div>
          ) : null}
          <Check checked={stop} onChange={setStop}>
            {t("Parar os containers durante a cópia dos volumes (cópia consistente; o projeto fica fora do ar alguns segundos)")}
          </Check>
          <Check checked={env} onChange={setEnv} top>
            {t("Incluir o .env (tem as senhas do projeto)")}
          </Check>
          {hasVMs ? (
            <Check checked={vm} onChange={setVm} top>
              {t("Incluir os discos das VMs (copiados de um snapshot, sem parar as VMs; podem ser grandes)")}
            </Check>
          ) : null}
          <div className="actions" style={{ marginTop: 10 }}>
            <button className="btn primary" disabled={isBusy || starting} onClick={now}>
              {isBusy ? (
                <>
                  <span className="spin" />
                  {t("Em andamento…")}
                </>
              ) : (
                t("Fazer backup agora")
              )}
            </button>
            <button
              className="btn"
              onClick={async () => {
                try {
                  await api(base + "/pasta", { method: "POST" });
                } catch (e) {
                  gantry.toast((e as Error).message, "err");
                }
              }}
            >
              {t("Abrir a pasta dos backups")}
            </button>
            <button
              className="btn"
              title={t("Um backup de outra pasta, de outro computador ou de outro projeto com os mesmos volumes")}
              onClick={async () => {
                const p = await gantry.pickPath({ kind: "abrir", title: t("Backup para restaurar em {name}", { name: lab.name }), dir: d ? d.dir : "~" });
                if (p) restore({ name: "arquivo", path: p, label: p.split("/").pop(), size: 0, created: 0 });
              }}
            >
              {t("Restaurar de um arquivo…")}
            </button>
          </div>
        </div>
      </div>
      {!d ? st?.err ? <ErrorBox>{st.err}</ErrorBox> : <Loading /> : <Schedule base={base} d={d} reload={reload} />}
      {d ? (
        <div className="block">
          <div className="block-head">
            <h2 className="block-title">Backups</h2>
            <span className="muted small mono">{d.dir}</span>
          </div>
          {d.items.length ? (
            <div className="panel">
              <table className="table">
                <thead>
                  <tr>
                    {[t("Quando"), t("Tamanho"), t("Conteúdo"), ""].map((h, i) => (
                      <th key={i}>{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {d.items.map((it) => {
                    const m = it.manifest || {};
                    const what = [
                      (m.volumes || []).length ? tn(m.volumes!.length, "{n} volume", "{n} volumes") : null,
                      (m.dumps || []).length ? tn(m.dumps!.length, "{n} dump", "{n} dumps") : null,
                      (m.vms || []).length ? tn(m.vms!.length, "{n} disco de VM", "{n} discos de VM") : null,
                      m.files ? t("arquivos") + (m.env ? " + .env" : "") : null,
                      m.stopped ? t("parado na cópia") : null,
                    ]
                      .filter(Boolean)
                      .join(" · ");
                    return (
                      <tr key={it.name}>
                        <td>
                          {new Date((m.created || it.created) * 1000).toLocaleString(LOCALE, { dateStyle: "short", timeStyle: "short" })}
                          <div className="muted small mono">{it.name}</div>
                        </td>
                        <td className="mono">{fmtBytes(it.size)}</td>
                        <td>{what || "—"}</td>
                        <td className="row-actions">
                          <button className="btn small" disabled={isBusy || (!(m.volumes || []).length && !(m.vms || []).length && !m.files)} onClick={() => restore(it)}>
                            {t("Restaurar")}
                          </button>
                          <button className="btn small danger" onClick={() => del(it)}>
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
            <div className="panel panel-pad">
              <p className="muted" style={{ margin: 0 }}>
                {t("Nenhum backup ainda.")}
              </p>
            </div>
          )}
        </div>
      ) : null}
    </div>
  );
}

function Schedule({ base, d, reload }: { base: string; d: Data; reload: () => void }) {
  const sch = d.schedule || {};
  const [every, setEvery] = useState(sch.every || 0);
  const [keep, setKeep] = useState(sch.keep || 7);
  const [stop, setStop] = useState(!!sch.stop);
  const [env, setEnv] = useState(!!sch.env);
  return (
    <div className="block">
      <div className="block-head">
        <h2 className="block-title">{t("Backup automático")}</h2>
      </div>
      <div className="panel panel-pad">
        <div className="grid2">
          <div className="field">
            <label>{t("Frequência")}</label>
            <select value={every} onChange={(e) => setEvery(+e.target.value)}>
              {(
                [
                  [0, t("Desligado")],
                  [6, t("A cada 6 h")],
                  [12, t("A cada 12 h")],
                  [24, t("Todo dia")],
                  [168, t("Toda semana")],
                ] as [number, string][]
              ).map(([v, l]) => (
                <option key={v} value={v}>
                  {l}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label>{t("Manter os últimos")}</label>
            <input type="number" min={0} max={1000} value={keep} style={{ width: 90 }} onChange={(e) => setKeep(+e.target.value)} />
            <p className="hint">{t("0 = guardar todos")}</p>
          </div>
        </div>
        <Check checked={stop} onChange={setStop}>
          {t("Parar os containers durante a cópia")}
        </Check>
        <Check checked={env} onChange={setEnv} top>
          {t("Incluir o .env")}
        </Check>
        <p className="hint">
          {sch.last
            ? t("Roda enquanto o Gantry estiver aberto neste computador. Último automático: {when}.", { when: new Date(sch.last * 1000).toLocaleString(LOCALE, { dateStyle: "short", timeStyle: "short" }) })
            : t("Roda enquanto o Gantry estiver aberto neste computador.")}
        </p>
        {sch.lastErr ? <p className="flash flash-err">{t("O último backup automático falhou: {err}. Tenta de novo em até 1 h.", { err: sch.lastErr })}</p> : null}
        <div className="actions">
          <button
            className="btn"
            onClick={async () => {
              try {
                await api(base + "/agenda", { method: "PUT", body: { every, keep, stop, env } });
                gantry.toast(t("Agenda salva"), "ok");
                reload();
              } catch (e) {
                gantry.toast((e as Error).message, "err");
              }
            }}
          >
            {t("Salvar agenda")}

          </button>
        </div>
      </div>
    </div>
  );
}
