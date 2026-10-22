import { useEffect, useState, type ReactNode } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { formDialog } from "@/components/dialog";
import { Banner } from "@/components/ui";

// The images of the VM catalog that Gantry does not download (the NixOS
// base built from images/nixos): where their file is on each machine.

export type VMImage = { name: string; label: string; build?: string; source?: string; path: string };

export const vmImages = (host: string) => api<VMImage[]>("/hosts/" + enc(host) + "/imagens-vm");

export async function setVMImage(host: string, name: string, path: string) {
  await api("/hosts/" + enc(host) + "/imagens-vm", { method: "PUT", body: { name, path } });
}

// usesLocalImage tells which local images a compose text uses.
export function localImagesIn(yaml: string, list: VMImage[]): VMImage[] {
  return list.filter((im) => new RegExp("(^|[^\\w.-])" + im.name.replace(/[.]/g, "\\.") + "($|[^\\w.-])").test(yaml));
}

function ImageRow({ host, im, onSaved }: { host: string; im: VMImage; onSaved: (p: string) => void }) {
  const [path, setPath] = useState(im.path);
  const [busy, setBusy] = useState(false);
  const kind = gantry.hostByName(host)?.kind;
  const local = kind === "unix" || kind === "wsl";
  const save = async (p: string) => {
    setBusy(true);
    try {
      await setVMImage(host, im.name, p.trim());
      onSaved(p.trim());
      gantry.toast(p.trim() ? t("Imagem {name} configurada", { name: im.name }) : t("Imagem {name} removida", { name: im.name }), "ok");
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="field">
      <label>
        {im.label} <span className="muted small mono">{im.name}</span>
      </label>
      <div className="input-pick">
        <input placeholder={t("/caminho/da/imagem.qcow2")} spellCheck={false} value={path} onChange={(e) => setPath(e.target.value)} aria-label={t("Arquivo da imagem {name}", { name: im.name })} />
        {local ? (
          <button
            type="button"
            className="btn small"
            disabled={busy}
            onClick={async () => {
              const p = await gantry.pickPath({ kind: "abrir", title: t("Arquivo da imagem {name}", { name: im.name }), dir: "~" });
              if (p) {
                setPath(p);
                void save(p);
              }
            }}
          >
            {t("Escolher…")}
          </button>
        ) : null}
        <button type="button" className="btn small primary" disabled={busy || path.trim() === im.path} onClick={() => void save(path)}>
          {t("Salvar")}
        </button>
      </div>
      <p className="hint">
        {im.path ? t("Em uso: {path}", { path: im.path }) : im.build ? t("Ainda não configurada nesta máquina. O Gantry não baixa esta imagem: gere o arquivo e informe onde ele está.") : t("Ainda não configurada nesta máquina. A licença do fabricante não deixa o Gantry baixar esta imagem: obtenha o arquivo e informe onde ele está.")}{" "}
        {im.build ? (
          <>
            {t("Como gerar:")} <code>{im.build}</code>
          </>
        ) : (
          <>
            {t("Onde obter:")} {im.source}
          </>
        )}
      </p>
    </div>
  );
}

// VMImagesPanel lists the local images of a machine, to point to their file.
export function VMImagesPanel({ host, only }: { host: string; only?: string[] }) {
  const [list, setList] = useState<VMImage[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    vmImages(host)
      .then((l) => live && setList(l))
      .catch((e) => live && setErr((e as Error).message));
    return () => {
      live = false;
    };
  }, [host]);
  if (err) return <p className="muted small">{err}</p>;
  if (!list) return null;
  const shown = only ? list.filter((im) => only.includes(im.name)) : list;
  return (
    <>
      {shown.map((im) => (
        <ImageRow key={im.name} host={host} im={im} onSaved={(p) => setList((l) => (l ?? []).map((x) => (x.name === im.name ? { ...x, path: p } : x)))} />
      ))}
    </>
  );
}

// NeedImage warns, while making a project, when it uses a local image that
// is not set on the target machine yet, and asks where its file is.
export function NeedImage({ host, yaml }: { host: string; yaml: string }) {
  // The images missing when the step opened: they stay in view after being
  // set, with the path in use.
  const [missing, setMissing] = useState<VMImage[] | null>(null);
  useEffect(() => {
    let live = true;
    vmImages(host)
      .then((l) => live && setMissing(l.filter((im) => !im.path)))
      .catch(() => live && setMissing([]));
    return () => {
      live = false;
    };
  }, [host]);
  const used = missing ? localImagesIn(yaml, missing) : [];
  if (!used.length) return null;
  return (
    <div className="banner info np-img">
      <div className="grow">
        <p>{t("Este projeto usa uma imagem que o Gantry não baixa e que não está nas pastas de imagens de {host}. Copie o arquivo para uma delas (página da máquina › Configurações › Imagens) ou informe onde ele está:", { host })}</p>
        <VMImagesPanel host={host} only={used.map((im) => im.name)} />
      </div>
    </div>
  );
}

// The image folders of a machine: where Gantry finds the images on its own.
type Found = { path: string; name: string; catalog?: string; type: string };
type CatalogImage = VMImage & { auto?: string };
type Need = { id: string; name: string; why: string; install?: string; note?: string };
type Images = { dirs: string[]; defaults: string[]; found: Found[]; catalog: CatalogImage[]; error?: string; needs?: Need[] };

export const hostImages = (host: string, fresh?: boolean) => api<Images>("/hosts/" + enc(host) + "/imagens" + (fresh ? "?novo=1" : ""));

// ImagesBlock: the folders of the machine, what is in them, and the catalog
// images it has (found in the folders or set by hand).
export function ImagesBlock({ host }: { host: string }) {
  const [im, setIm] = useState<Images | null>(null);
  const [dirs, setDirs] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const load = (fresh?: boolean) =>
    hostImages(host, fresh)
      .then((r) => {
        setIm(r);
        setDirs(r.dirs);
        setErr("");
      })
      .catch((e) => setErr((e as Error).message));
  useEffect(() => {
    void load();
  }, [host]); // eslint-disable-line react-hooks/exhaustive-deps
  const saveDirs = async (list: string[] | null) => {
    setBusy(true);
    try {
      await api("/hosts/" + enc(host) + "/imagens/pastas", { method: "PUT", body: { dirs: list === null ? null : list.map((d) => d.trim()).filter(Boolean) } });
      await load(true);
      gantry.toast(t("Pastas de imagens salvas"), "ok");
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    } finally {
      setBusy(false);
    }
  };
  const setPath = (c: CatalogImage) =>
    formDialog({
      title: t("Arquivo de {label}", { label: c.label }),
      intro: t("Só precisa quando o arquivo está fora das pastas, ou para escolher entre vários. Vazio volta a usar o que está nas pastas."),
      submit: t("Salvar"),
      fields: [{ id: "p", label: t("Caminho em {host}", { host }), value: c.path, placeholder: c.auto || "/srv/imagens/arquivo", optional: true }],
      onSubmit: async (v) => {
        await setVMImage(host, c.name, String(v.p ?? "").trim());
        await load();
      },
    });
  if (err && !im) return <p className="muted small">{err}</p>;
  if (!im) return null;
  const dirty = JSON.stringify(dirs.map((d) => d.trim()).filter(Boolean)) !== JSON.stringify(im.dirs);
  const catOf = (name?: string) => im.catalog.find((c) => c.name === name);
  // One list of what the machine has: the files in the folders and the
  // catalog images pointed to by hand; what the catalog knows and the
  // machine lacks goes folded below.
  type Row = { key: string; name: string; what: ReactNode; path: string; cat?: CatalogImage };
  const rows: Row[] = [];
  for (const c of im.catalog.filter((c) => c.path))
    rows.push({ key: "set:" + c.name, name: c.name, what: <>{c.label} <span className="muted small">{t("(escolhida à mão)")}</span></>, path: c.path, cat: c });
  for (const f of im.found) {
    const c = catOf(f.catalog);
    if (c?.path && c.path === f.path) continue;
    rows.push({
      key: f.path,
      name: f.catalog && f.catalog !== "cisco-iourc" ? f.catalog + " · " + f.name : f.name,
      what: f.catalog ? c?.label || f.catalog : <span className="muted">{t("outra imagem ({type})", { type: f.type })}</span>,
      path: f.path,
      cat: c && c.auto === f.path && !c.path ? c : undefined,
    });
  }
  const absent = im.catalog.filter((c) => !c.path && !c.auto);
  return (
    <div className="block images-block">
      <div className="block-head">
        <h2 className="block-title">{t("Imagens")}</h2>
        <span className="muted small">{t("os .qcow2 e ISOs das VMs, os .bin do IOL e do Dynamips e o iourc; o tipo de cada arquivo vem do nome")}</span>
        <span className="grow" />
        <button className="btn small" type="button" disabled={busy} onClick={() => void load(true)}>
          {t("Procurar de novo")}
        </button>
      </div>
      <div className="panel panel-pad">
        <h3 className="img-sub">{t("Pastas")}</h3>
        {dirs.map((d, i) => (
          <div key={i} className="img-dir">
            <input className="input mono" spellCheck={false} value={d} placeholder="~/gantry/imagens" aria-label={t("Pasta de imagens {n}", { n: i + 1 })} onChange={(e) => setDirs(dirs.map((x, k) => (k === i ? e.target.value : x)))} />
            <button className="icon-btn" type="button" aria-label={t("Tirar a pasta")} onClick={() => setDirs(dirs.filter((_, k) => k !== i))}>
              ✕
            </button>
          </div>
        ))}
        <div className="img-dir-actions">
          <button className="btn small" type="button" onClick={() => setDirs([...dirs, ""])}>
            {t("+ Pasta")}
          </button>
          <button className="btn small primary" type="button" disabled={!dirty || busy} onClick={() => void saveDirs(dirs)}>
            {t("Salvar pastas")}
          </button>
          {JSON.stringify(im.dirs) !== JSON.stringify(im.defaults) ? (
            <button className="btn small btn-ghost" type="button" disabled={busy} onClick={() => void saveDirs(null)} title={im.defaults.join(", ")}>
              {t("Voltar às padrão")}
            </button>
          ) : null}
        </div>
        {im.error ? <p className="muted small">{im.error}</p> : null}
        {(im.needs ?? []).map((n) => (
          <Banner key={n.id} kind="warn">
            {t("Falta o {name} nesta máquina para rodar as imagens encontradas: {why}", { name: n.name, why: n.why })}
            {n.install ? (
              <>
                {" "}
                {t("Instale com")} <code className="mono">{n.install}</code>
              </>
            ) : n.note ? (
              " " + n.note
            ) : null}
          </Banner>
        ))}
        <h3 className="img-sub">{t("Nesta máquina ({n})", { n: rows.length })}</h3>
        {rows.length ? (
          <table className="grid compact">
            <thead>
              <tr>
                <th>{t("Nome no compose")}</th>
                <th>{t("É")}</th>
                <th>{t("Arquivo")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key}>
                  <td className="mono small">{r.name}</td>
                  <td>{r.what}</td>
                  <td className="mono small muted">{r.path}</td>
                  <td className="row-actions">
                    {r.cat ? (
                      <button className="btn small btn-ghost" type="button" onClick={() => setPath(r.cat!)} title={t("Usar outro arquivo para esta imagem")}>
                        {t("Trocar…")}
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="muted small">{t("Nenhuma imagem nas pastas. Copie os arquivos para uma delas (por exemplo ~/gantry/imagens) e clique em Procurar de novo.")}</p>
        )}
        {absent.length ? (
          <details className="img-absent">
            <summary>{t("Imagens conhecidas que não estão aqui ({n})", { n: absent.length })}</summary>
            <p className="muted small">{t("O Gantry não baixa estas imagens. Copie o arquivo para uma das pastas acima, ou aponte onde ele está.")}</p>
            <table className="grid compact">
              <tbody>
                {absent.map((c) => (
                  <tr key={c.name}>
                    <td>
                      {c.label} <span className="muted small mono">{c.name}</span>
                    </td>
                    <td className="small muted">{c.build ? <>{t("Como gerar:")} <code>{c.build}</code></> : c.source ? <>{t("Onde obter:")} {c.source}</> : null}</td>
                    <td className="row-actions">
                      <button className="btn small btn-ghost" type="button" onClick={() => setPath(c)}>
                        {t("Apontar arquivo…")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        ) : null}
      </div>
    </div>
  );
}
