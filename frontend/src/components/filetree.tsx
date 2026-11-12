// The files of a project as a tree beside the editor: folders that open and
// close, a filter, new file and new folder (with subfolders in the name:
// nginx/conf.d/default.conf), rename and delete by the right-click menu or
// the ⋯ of the line, and the binds of the compose that are not there yet.
// It serves the project page (the folder on disk) and Criar projeto (the
// files still in memory), so it only shows and asks; the caller does.
import { useEffect, useMemo, useState } from "react";
import { t, tn } from "@/lib/i18n";
import { copyText } from "@/lib/format";
import { confirm, formDialog } from "./dialog";
import { openMenu, type MenuItem } from "./contextmenu";
import { Chev } from "./icons";
import { gantry } from "@/lib/gantry";

export type TreeEntry = { path: string; dir?: boolean; name?: string };
export type TreeMissing = { path: string; dir: boolean; services?: string[] };
export type TreeKind = "arquivo" | "pasta";

type Node = { path: string; base: string; dir: boolean; entry?: TreeEntry; kids: Node[] };

// build nests the paths; the folders a file implies exist even when not
// listed (the files of Criar projeto have only their paths).
function build(items: TreeEntry[]): Node[] {
  const root: Node = { path: "", base: "", dir: true, kids: [] };
  const dirs = new Map<string, Node>([["", root]]);
  const dirOf = (p: string): Node => {
    const have = dirs.get(p);
    if (have) return have;
    const i = p.lastIndexOf("/");
    const parent = dirOf(i < 0 ? "" : p.slice(0, i));
    const n: Node = { path: p, base: p.slice(i + 1), dir: true, kids: [] };
    parent.kids.push(n);
    dirs.set(p, n);
    return n;
  };
  for (const e of items) {
    if (e.dir) {
      dirOf(e.path).entry = e;
      continue;
    }
    const i = e.path.lastIndexOf("/");
    dirOf(i < 0 ? "" : e.path.slice(0, i)).kids.push({ path: e.path, base: e.path.slice(i + 1), dir: false, entry: e, kids: [] });
  }
  const sort = (ns: Node[]) => {
    ns.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.base.localeCompare(b.base, undefined, { sensitivity: "base" })));
    ns.forEach((n) => sort(n.kids));
  };
  sort(root.kids);
  return root.kids;
}

const parentOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");

function FolderIcon({ open }: { open?: boolean }) {
  return (
    <svg className="ftree-ico" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {open ? <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v1H7.5a2 2 0 0 0-1.9 1.4L3 19z M3 19l2.6-7.6A2 2 0 0 1 7.5 10H21l-2.6 7.6a2 2 0 0 1-1.9 1.4H3z" /> : <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />}
    </svg>
  );
}

function FileIcon() {
  return (
    <svg className="ftree-ico" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z M14 3v5h5" />
    </svg>
  );
}

export function FileTree({
  title,
  hint,
  items,
  selected,
  onOpen,
  onCreate,
  onRename,
  onDelete,
  locked,
  missing,
  cut,
  error,
  memory,
}: {
  title: string;
  hint?: string;
  items: TreeEntry[];
  selected: string | null;
  onOpen: (e: TreeEntry) => void;
  onCreate?: (kind: TreeKind, path: string) => Promise<void>;
  onRename?: (from: string, to: string, dir: boolean) => Promise<void>;
  onDelete?: (path: string, dir: boolean) => Promise<void>;
  locked?: (path: string) => boolean;
  missing?: TreeMissing[];
  cut?: boolean;
  error?: string;
  // memory: the files are not on disk yet (Criar projeto); deleting asks
  // only for a folder with files.
  memory?: boolean;
}) {
  const nodes = useMemo(() => build(items), [items]);
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const [inited, setInited] = useState(false);
  const [filter, setFilter] = useState("");
  // The folder where New file/New folder go: the one selected, or that of
  // the file selected.
  const [cwd, setCwd] = useState("");

  // First list: a small project opens all its folders.
  useEffect(() => {
    if (inited || !items.length) return;
    setInited(true);
    const dirs = new Set<string>();
    const walk = (ns: Node[]) =>
      ns.forEach((n) => {
        if (n.dir) {
          dirs.add(n.path);
          walk(n.kids);
        }
      });
    if (items.length <= 60) walk(nodes);
    else nodes.forEach((n) => n.dir && dirs.add(n.path));
    setOpen(dirs);
  }, [items, nodes, inited]);

  // The file being edited shows: its folders open.
  useEffect(() => {
    if (!selected) return;
    setOpen((o) => {
      const n = new Set(o);
      for (let p = parentOf(selected); p; p = parentOf(p)) n.add(p);
      return n.size === o.size ? o : n;
    });
  }, [selected]);

  const toggle = (p: string) =>
    setOpen((o) => {
      const n = new Set(o);
      if (n.has(p)) n.delete(p);
      else n.add(p);
      return n;
    });

  // reveal opens the folders down to what was just made, and makes its
  // folder the one where the next New file goes.
  const reveal = (kind: TreeKind, p: string) => {
    setOpen((o) => {
      const n = new Set(o);
      for (let d = kind === "pasta" ? p : parentOf(p); d; d = parentOf(d)) n.add(d);
      return n;
    });
    setCwd(kind === "pasta" ? p : parentOf(p));
  };

  const createNow = (kind: TreeKind, p: string) =>
    onCreate!(kind, p).then(
      () => reveal(kind, p),
      (x) => gantry.toast((x as Error).message, "err"),
    );

  const create = (kind: TreeKind, inDir: string) => {
    if (!onCreate) return;
    const base = inDir ? inDir + "/" : "";
    formDialog({
      title: kind === "arquivo" ? t("Novo arquivo") : t("Nova pasta"),
      intro: kind === "arquivo" ? t("O caminho é dentro da pasta do projeto; as pastas que ainda não existem são criadas junto (nginx/conf.d/default.conf).") : t("O caminho é dentro da pasta do projeto; pode ter subpastas (dados/postgres)."),
      submit: t("Criar"),
      fields: [{ id: "p", label: t("Caminho"), value: base, placeholder: kind === "arquivo" ? "config/app.yml" : "config" }],
      onSubmit: async (v) => {
        const p = String(v.p || "")
          .trim()
          .replace(/^\.?\/+/, "")
          .replace(/\/+$/, "");
        if (!p || p === base.replace(/\/$/, "")) throw new Error(t("Informe o nome."));
        if (p.split("/").some((x) => x === ".." || x === ".")) throw new Error(t("O caminho fica dentro da pasta do projeto: sem .. nem /."));
        await onCreate(kind, p);
        reveal(kind, p);
      },
    });
  };

  const rename = (n: Node) => {
    if (!onRename) return;
    formDialog({
      title: t("Renomear {name}", { name: n.base }),
      intro: t("Mudar a pasta no caminho move o item para ela."),
      submit: t("Renomear"),
      fields: [{ id: "p", label: t("Caminho"), value: n.path }],
      onSubmit: async (v) => {
        const p = String(v.p || "")
          .trim()
          .replace(/^\.?\/+/, "")
          .replace(/\/+$/, "");
        if (!p) throw new Error(t("Informe o nome."));
        if (p.split("/").some((x) => x === ".." || x === ".")) throw new Error(t("O caminho fica dentro da pasta do projeto: sem .. nem /."));
        if (p !== n.path) await onRename(n.path, p, n.dir);
      },
    });
  };

  const remove = async (n: Node) => {
    if (!onDelete) return;
    const count = n.dir ? items.filter((x) => x.path.startsWith(n.path + "/") && !x.dir).length : 0;
    if (memory) {
      if (count && !(await confirm(t("Tirar a pasta do projeto?"), tn(count, "A pasta {path} e o arquivo dentro dela saem do projeto.", "A pasta {path} e os {n} arquivos dentro dela saem do projeto.", { path: n.path }), t("Tirar|do projeto"), true))) return;
      await onDelete(n.path, n.dir);
      return;
    }
    const msg = n.dir ? (count ? tn(count, "A pasta {path} e o arquivo dentro dela serão apagados do disco.", "A pasta {path} e os {n} arquivos dentro dela serão apagados do disco.", { path: n.path }) : t("A pasta {path} (vazia) será apagada do disco.", { path: n.path })) : t("{path} será apagado do disco.", { path: n.path });
    if (!(await confirm(n.dir ? t("Excluir a pasta?") : t("Excluir o arquivo?"), msg + " " + t("Não dá para desfazer."), t("Excluir"), true))) return;
    await onDelete(n.path, n.dir);
  };

  const menu = (e: React.MouseEvent, n: Node) => {
    const here = n.dir ? n.path : parentOf(n.path);
    const fixed = locked?.(n.path);
    const items: MenuItem[] = [
      !n.dir && { label: t("Abrir"), onClick: () => n.entry && onOpen(n.entry) },
      onCreate && { label: here ? t("Novo arquivo em {dir}", { dir: here }) : t("Novo arquivo"), onClick: () => create("arquivo", here) },
      onCreate && { label: here ? t("Nova pasta em {dir}", { dir: here }) : t("Nova pasta"), onClick: () => create("pasta", here) },
      "-",
      onRename && { label: t("Renomear…"), onClick: () => rename(n), disabled: fixed, hint: fixed ? t("é o compose do projeto") : undefined },
      { label: t("Copiar caminho"), onClick: () => void copyText(n.path) },
      "-",
      onDelete && { label: memory ? t("Tirar do projeto") : t("Excluir…"), onClick: () => void remove(n).catch((x) => gantry.toast((x as Error).message, "err")), danger: true, disabled: fixed },
    ];
    openMenu(e, items, n.path);
  };

  const q = filter.trim().toLowerCase();
  const found = q ? items.filter((x) => !x.dir && x.path.toLowerCase().includes(q)).slice(0, 300) : null;
  const pending = (missing || []).filter((m) => !items.some((x) => x.path === m.path));

  const row = (n: Node, depth: number) => {
    const sel = !n.dir && selected === n.path;
    return (
      <div key={n.path} role="treeitem" aria-expanded={n.dir ? open.has(n.path) : undefined} aria-selected={sel}>
        <div className={"ftree-row" + (sel ? " sel" : "") + (n.dir && cwd === n.path ? " cwd" : "")} style={{ paddingLeft: 6 + depth * 14 }} onContextMenu={(e) => menu(e, n)}>
          <button
            type="button"
            className="ftree-main"
            title={n.path}
            onClick={() => {
              if (n.dir) {
                toggle(n.path);
                setCwd(n.path);
              } else {
                setCwd(parentOf(n.path));
                if (n.entry) onOpen(n.entry);
              }
            }}
          >
            {n.dir ? (
              <span className={"ftree-chev" + (open.has(n.path) ? " open" : "")}>
                <Chev size={11} />
              </span>
            ) : (
              <span className="ftree-chev" />
            )}
            {n.dir ? <FolderIcon open={open.has(n.path)} /> : <FileIcon />}
            <span className="ftree-name">{n.base}</span>
          </button>
          <button type="button" className="ftree-more" aria-label={t("Ações de {name}", { name: n.base })} onClick={(e) => menu(e, n)}>
            ⋯
          </button>
        </div>
        {n.dir && open.has(n.path) ? (n.kids.length ? n.kids.map((k) => row(k, depth + 1)) : <div className="ftree-empty" style={{ paddingLeft: 26 + (depth + 1) * 14 }}>{t("vazia")}</div>) : null}
      </div>
    );
  };

  return (
    <aside className="panel ftree" aria-label={t("Arquivos do projeto")}>
      <div className="ftree-head">
        <span className="ftree-title" title={hint}>
          {title}
        </span>
        {onCreate ? (
          <span className="ftree-acts">
            <button type="button" className="btn small ghost" title={cwd ? t("Novo arquivo em {dir}", { dir: cwd }) : t("Novo arquivo")} onClick={() => create("arquivo", cwd)}>
              {t("+ Arquivo")}
            </button>
            <button type="button" className="btn small ghost" title={cwd ? t("Nova pasta em {dir}", { dir: cwd }) : t("Nova pasta")} onClick={() => create("pasta", cwd)}>
              {t("+ Pasta")}
            </button>
          </span>
        ) : null}
      </div>
      {items.length > 12 ? (
        <input className="ftree-filter" value={filter} onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.key === "Escape" && setFilter("")} placeholder={t("Filtrar arquivos")} spellCheck={false} aria-label={t("Filtrar arquivos")} />
      ) : null}
      {error ? <p className="flash flash-err small">{error}</p> : null}
      <div className="ftree-list" role="tree">
        {found ? (
          found.length ? (
            found.map((x) => (
              <div key={x.path} className={"ftree-row" + (selected === x.path ? " sel" : "")} style={{ paddingLeft: 6 }}>
                <button type="button" className="ftree-main" title={x.path} onClick={() => onOpen(x)}>
                  <FileIcon />
                  <span className="ftree-name">{x.path}</span>
                </button>
              </div>
            ))
          ) : (
            <p className="muted small ftree-note">{t("Nenhum arquivo com “{q}”.", { q: filter.trim() })}</p>
          )
        ) : nodes.length ? (
          <div onClick={(e) => e.target === e.currentTarget && setCwd("")}>{nodes.map((n) => row(n, 0))}</div>
        ) : (
          <p className="muted small ftree-note">{t("Pasta vazia.")}</p>
        )}
        {cut ? <p className="muted small ftree-note">{t("A pasta tem arquivos demais; parte ficou de fora da árvore.")}</p> : null}
      </div>
      {pending.length && onCreate ? (
        <div className="ftree-missing">
          <h3 className="sub">{t("O compose monta e ainda não existe")}</h3>
          {pending.map((m) => (
            <div key={m.path} className="ftree-miss">
              <span className="mono small" title={(m.services || []).join(", ")}>
                {m.path}
                {m.dir ? "/" : ""}
              </span>
              <span className="ftree-miss-acts">
                <button type="button" className={"btn small" + (m.dir ? "" : " primary")} onClick={() => void createNow("arquivo", m.path)}>
                  {t("criar arquivo")}
                </button>
                <button type="button" className={"btn small" + (m.dir ? " primary" : "")} onClick={() => void createNow("pasta", m.path)}>
                  {t("criar pasta")}
                </button>
              </span>
              {m.services?.length ? <span className="muted small">{tn(m.services.length, "serviço {list}", "serviços {list}", { list: m.services.join(", ") })}</span> : null}
            </div>
          ))}
        </div>
      ) : null}
    </aside>
  );
}
