// Images, volumes and networks of an engine: list, filter, remove, prune
// and create.
import { useEffect } from "react";
import { create } from "zustand";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, fmtAgo, newCtrHref, rtQuery, type Host } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { confirm, formDialog } from "@/components/dialog";
import { ErrorBox, Loading } from "@/components/ui";
import { FilterInput, filterItems, useFilter, usePage } from "@/components/list";
import { imageHistoryDialog, imageLoadFrom, imagePushDialog, imageSaveAs, registriesDialog } from "./tools";
import { setPrefill } from "./form";
import { t, tn } from "@/lib/i18n";

type Res = { id: string; name: string; detail?: string; lab?: string; usedBy: string[]; system?: boolean; size?: number; created?: number };
type Entry = { items?: Res[]; err?: string | null; loading?: boolean };

export const useRes = create<Record<string, Entry>>(() => ({}));
const rkey = (host: string, kind: string, rt?: string) => host + "/" + kind + (rt ? "@" + rt : "");

export async function loadResources(host: string, kind: string, rt?: string) {
  const key = rkey(host, kind, rt);
  useRes.setState((s) => ({ [key]: { ...(s[key] ?? {}), loading: true } }));
  try {
    const items = await api<Res[]>("/hosts/" + enc(host) + "/res/" + kind + rtQuery(rt));
    useRes.setState({ [key]: { items, err: null, loading: false } });
  } catch (e) {
    useRes.setState((s) => ({ [key]: { ...(s[key] ?? {}), err: (e as Error).message, loading: false } }));
  }
}

const KINDS: Record<string, { label: string; none: string; prune: string; filter: string; pruneQ: string; removeQ: (name: string) => string; removed: (name: string) => string }> = {
  images: {
    label: t("Imagens"),
    none: t("Nenhuma imagem."),
    prune: t("Remove todas as imagens que nenhum container usa (não só as sem tag)."),
    filter: t("Filtrar imagens…"),
    pruneQ: t("Limpar imagens sem uso?"),
    removeQ: (name) => t("Remover imagem \"{name}\"?", { name }),
    removed: (name) => t("Imagem {name} removida", { name }),
  },
  volumes: {
    label: t("Volumes"),
    none: t("Nenhum volume."),
    prune: t("Remove todos os volumes que nenhum container usa. Os dados deles são apagados."),
    filter: t("Filtrar volumes…"),
    pruneQ: t("Limpar volumes sem uso?"),
    removeQ: (name) => t("Remover volume \"{name}\"?", { name }),
    removed: (name) => t("Volume {name} removido", { name }),
  },
  networks: {
    label: t("Redes"),
    none: t("Nenhuma rede."),
    prune: t("Remove as redes criadas que nenhum container usa."),
    filter: t("Filtrar redes…"),
    pruneQ: t("Limpar redes sem uso?"),
    removeQ: (name) => t("Remover rede \"{name}\"?", { name }),
    removed: (name) => t("Rede {name} removida", { name }),
  },
};

const lines = (s: string) =>
  (s || "")
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
const kvLines = (s: string) =>
  Object.fromEntries(
    lines(s).map((l) => {
      const i = l.indexOf("=");
      return i < 0 ? [l, ""] : [l.slice(0, i).trim(), l.slice(i + 1).trim()];
    }),
  );

export function newResourceDialog(host: string, kind: string, rt?: string) {
  const done = () => loadResources(host, kind, rt);
  if (kind === "volumes") {
    formDialog({
      title: t("Criar volume"),
      submit: t("Criar"),
      fields: [
        { id: "name", label: t("Nome"), placeholder: t("dados") },
        { id: "driver", label: "Driver", placeholder: "local", optional: true },
        {
          id: "opts",
          label: t("Opções do driver"),
          type: "textarea",
          rows: 3,
          optional: true,
          placeholder: "type=nfs\no=addr=10.0.0.5,rw\ndevice=:/exports/dados",
          hint: t("Uma por linha, como chave=valor. Ex.: um volume NFS ou uma pasta do host (type=none, o=bind, device=/caminho)."),
        },
        { id: "labels", label: "Labels", type: "textarea", rows: 2, optional: true, placeholder: t("projeto=meu-app") },
      ],
      onSubmit: async (v) => {
        await api("/hosts/" + enc(host) + "/res/volumes" + rtQuery(rt), { method: "POST", body: { name: v.name, driver: v.driver, opts: kvLines(String(v.opts)), labels: kvLines(String(v.labels)) } });
        gantry.toast(t("Volume {name} criado", { name: String(v.name) }), "ok");
        done();
      },
    });
  } else if (kind === "networks") {
    formDialog({
      title: t("Criar rede"),
      submit: t("Criar"),
      fields: [
        { id: "name", label: t("Nome"), placeholder: "lab-net" },
        {
          id: "driver",
          label: "Driver",
          type: "select",
          value: "bridge",
          options: [
            ["bridge", "bridge"],
            ["macvlan", "macvlan"],
            ["ipvlan", "ipvlan"],
          ],
        },
        { id: "subnet", label: t("Sub-rede IPv4"), placeholder: "172.30.0.0/24", optional: true },
        { id: "gateway", label: "Gateway IPv4", placeholder: "172.30.0.1", optional: true },
        { id: "ipRange", label: t("Faixa para os containers"), placeholder: "172.30.0.128/25", optional: true, hint: t("Parte da sub-rede de onde saem os IPs automáticos; o resto fica para IPs fixos.") },
        { id: "ipv6", label: "IPv6", type: "checkbox" },
        { id: "subnet6", label: t("Sub-rede IPv6"), placeholder: "fd00:30::/64", optional: true },
        { id: "gateway6", label: "Gateway IPv6", placeholder: "fd00:30::1", optional: true },
        { id: "options", label: t("Opções do driver"), type: "textarea", rows: 2, optional: true, placeholder: "parent=eth0", hint: t("macvlan e ipvlan precisam de parent=<interface do host>.") },
        { id: "labels", label: "Labels", type: "textarea", rows: 2, optional: true, placeholder: t("projeto=meu-app") },
        { id: "internal", label: t("Interna (sem acesso à internet)"), type: "checkbox" },
        { id: "attachable", label: t("Anexável (para Swarm)"), type: "checkbox" },
      ],
      onSubmit: async (v) => {
        const body = { ...v, options: kvLines(String(v.options)), labels: kvLines(String(v.labels)) };
        await api("/hosts/" + enc(host) + "/res/networks" + rtQuery(rt), { method: "POST", body });
        gantry.toast(t("Rede {name} criada", { name: String(v.name) }), "ok");
        done();
      },
    });
  } else {
    formDialog({
      title: t("Baixar imagem"),
      submit: t("Baixar"),
      fields: [{ id: "image", label: t("Imagem"), placeholder: "postgres:16-alpine", hint: t("Sem registry, vem do Docker Hub (docker.io).") }],
      onSubmit: async (v) => {
        const r = await api<{ job: string }>("/hosts/" + enc(host) + "/res/images" + rtQuery(rt), { method: "POST", body: { image: v.image } });
        gantry.openJob(r.job, t("Baixar {image}", { image: String(v.image) }), async () => {
          done();
        });
      },
    });
  }
}

const closeMenu = (e: React.MouseEvent) => {
  const d = (e.target as HTMLElement).closest("details");
  if (d) d.open = false;
};

export function ResourceTab({ hst, kind, rt }: { hst: Host; kind: string; rt?: string }) {
  const K = KINDS[kind];
  const key = rkey(hst.name, kind, rt);
  const st = useRes((s) => s[key]);
  const fk = "res:" + key;
  const [q] = useFilter(fk);
  useEffect(() => {
    if (!useRes.getState()[key]) loadResources(hst.name, kind, rt);
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  const items = st?.items ?? [];
  const unused = items.filter((x) => !x.usedBy.length && !x.system).length;
  const total = kind === "images" ? items.reduce((a, x) => a + (x.size || 0), 0) : 0;
  const txt = (x: Res) => x.name + " " + (x.detail || "") + " " + (x.lab || "") + " " + x.usedBy.join(" ") + " " + x.id;
  const matched = filterItems(q, items, txt);
  const pg = usePage(fk, matched);
  const reload = () => loadResources(hst.name, kind, rt);

  const head = (
    <div className="block-head">
      <h2 className="block-title">{K.label + (st?.items ? " · " + items.length : "")}</h2>
      {total ? <span className="muted small">{t("{size} no total", { size: fmtBytes(total) })}</span> : null}
      <span className="grow" />
      <FilterInput listKey={fk} placeholder={K.filter} />
      <button className="btn" onClick={reload}>
        {t("Atualizar")}
      </button>
      {kind === "images" ? (
        <details className="more-menu mini">
          <summary className="btn">{t("Mais ▾")}</summary>
          <div className="more-list">
            <a className="btn small" href={"#/h/" + enc(hst.name) + "/construir"}>
              {t("Construir imagem")}
            </a>
            <button
              className="btn small"
              title={t("Carregar um .tar feito com docker save")}
              onClick={(e) => {
                closeMenu(e);
                imageLoadFrom(hst.name, rt);
              }}
            >
              {t("Carregar .tar…")}
            </button>
            <button
              className="btn small"
              title={t("Logins de registries privados")}
              onClick={(e) => {
                closeMenu(e);
                registriesDialog(hst.name);
              }}
            >
              {t("Registros (logins)…")}
            </button>
          </div>
        </details>
      ) : null}
      <button className="btn primary" onClick={() => newResourceDialog(hst.name, kind, rt)}>
        {({ images: t("Baixar imagem"), volumes: t("Criar volume"), networks: t("Criar rede") } as Record<string, string>)[kind]}
      </button>
      <button
        className="btn danger"
        disabled={!unused}
        onClick={async (e) => {
          const b = e.currentTarget;
          if (!(await confirm(K.pruneQ, K.prune, t("Limpar|imagens"), true))) return;
          b.disabled = true;
          try {
            const r = await api<{ removed: number; freed?: number }>("/hosts/" + enc(hst.name) + "/prune/" + kind + rtQuery(rt), { method: "POST" });
            gantry.toast(tn(r.removed, "{n} removido", "{n} removidos") + (r.freed ? t(" · {size} liberados", { size: fmtBytes(r.freed) }) : ""), "ok");
          } catch (err) {
            gantry.toast((err as Error).message, "err");
          }
          await reload();
        }}
      >
        {t("Limpar sem uso")}
        {unused ? " (" + unused + ")" : ""}
      </button>
    </div>
  );
  let body: React.ReactNode;
  if (!st || (st.loading && !st.items)) body = <Loading />;
  else if (!items.length) body = <p className="muted">{K.none}</p>;
  else if (!matched.length) body = <p className="muted">{t("Nada encontrado para \"{q}\".", { q })}</p>;
  else {
    const cols = kind === "images" ? [t("Imagem"), "ID", t("Tamanho"), t("Criada"), t("Em uso por"), ""] : kind === "volumes" ? [t("Volume"), t("Projeto"), t("Criado"), t("Em uso por"), ""] : [t("Rede"), t("Driver / sub-rede"), t("Projeto"), t("Em uso por"), ""];
    const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 12);
    body = (
      <>
        <div className="panel table-wrap">
          <table className="grid">
            <thead>
              <tr>
                {cols.map((h, i) => (
                  <th key={i} className={i === cols.length - 1 ? "act" : undefined}>
                    {h || <span className="sr">{t("Ações")}</span>}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {pg.items.map((x) => {
                const used = x.usedBy.length ? (
                  <span className="small" title={x.usedBy.join(", ")}>
                    {x.usedBy.slice(0, 3).join(", ") + (x.usedBy.length > 3 ? " +" + (x.usedBy.length - 3) : "")}
                  </span>
                ) : (
                  <span className="muted small">{x.system ? t("padrão do engine") : t("sem uso")}</span>
                );
                const name = (
                  <td>
                    <div className="cname">
                      <b>
                        <span className={"dot " + (x.usedBy.length ? "on" : "")} />
                        {x.name}
                      </b>
                      {x.detail && kind === "volumes" ? <small>{x.detail}</small> : null}
                    </div>
                  </td>
                );
                const named = x.name && !x.name.startsWith("<");
                return (
                  <tr key={x.id + x.name}>
                    {name}
                    {kind === "images" ? (
                      <>
                        <td className="mono">{x.detail || shortId(x.id)}</td>
                        <td className="num">{fmtBytes(x.size)}</td>
                        <td className="small">{fmtAgo(x.created)}</td>
                      </>
                    ) : kind === "volumes" ? (
                      <>
                        <td className="small">{x.lab || "—"}</td>
                        <td className="small">{fmtAgo(x.created)}</td>
                      </>
                    ) : (
                      <>
                        <td className="mono">{x.detail || "—"}</td>
                        <td className="small">{x.lab || "—"}</td>
                      </>
                    )}
                    <td>{used}</td>
                    <td className="act">
                      {kind === "images" ? (
                        <>
                          {named ? (
                            <a className="btn small" href={newCtrHref(hst.name, rt)} title={t("Criar um container com esta imagem")} onClick={() => setPrefill({ image: x.name })}>
                              {t("Usar")}
                            </a>
                          ) : null}
                          <button
                            className="btn small"
                            title={t("Dar outro nome (tag) à imagem")}
                            onClick={() =>
                              formDialog({
                                title: t("Novo nome para {name}", { name: x.name }),
                                submit: t("Salvar"),
                                fields: [{ id: "ref", label: t("Nome"), placeholder: t("registry.exemplo.com/app:1.0"), value: named ? x.name : "" }],
                                onSubmit: async (v) => {
                                  await api("/hosts/" + enc(hst.name) + "/images/" + enc(x.id) + "/tag" + rtQuery(rt), { method: "POST", body: { ref: v.ref } });
                                  gantry.toast(t("Imagem agora também se chama {ref}", { ref: String(v.ref) }), "ok");
                                  loadResources(hst.name, "images", rt);
                                },
                              })
                            }
                          >
                            {t("Nome…")}
                          </button>
                          <details className="more-menu mini">
                            <summary className="btn small" aria-label={t("Mais ações")}>
                              ⋯
                            </summary>
                            <div className="more-list">
                              <button
                                className="btn small"
                                onClick={(e) => {
                                  closeMenu(e);
                                  imageHistoryDialog(hst.name, x, rt);
                                }}
                              >
                                {t("Camadas")}
                              </button>
                              <button
                                className="btn small"
                                onClick={(e) => {
                                  closeMenu(e);
                                  imagePushDialog(hst.name, x, rt);
                                }}
                              >
                                {t("Enviar ao registry…")}
                              </button>
                              <button
                                className="btn small"
                                onClick={(e) => {
                                  closeMenu(e);
                                  imageSaveAs(hst.name, x, rt);
                                }}
                              >
                                {t("Salvar .tar…")}
                              </button>
                            </div>
                          </details>
                        </>
                      ) : null}
                      {x.system ? null : (
                        <button
                          className="btn small danger"
                          onClick={async (e) => {
                            const b = e.currentTarget;
                            const inUse = x.usedBy.length ? t(" Em uso por {list}.", { list: x.usedBy.join(", ") }) : "";
                            const msg =
                              kind === "images"
                                ? x.usedBy.length
                                  ? t("Os containers que usam esta imagem precisam ser removidos antes.")
                                  : /tags$/.test(x.detail || "")
                                    ? t("Só esta tag é removida; a imagem continua com as outras.")
                                    : t("A imagem será apagada deste host.")
                                : kind === "volumes"
                                  ? t("Os dados do volume são apagados para sempre.") + inUse
                                  : t("A rede será removida.") + inUse;
                            if (!(await confirm(K.removeQ(x.name), msg, t("Remover"), true))) return;
                            b.disabled = true;
                            try {
                              await api("/hosts/" + enc(hst.name) + "/res/" + kind + "/" + enc(x.id) + rtQuery(rt), { method: "DELETE" });
                              gantry.toast(K.removed(x.name), "ok");
                            } catch (err) {
                              gantry.toast((err as Error).message, "err");
                            }
                            reload();
                          }}
                        >
                          {t("Remover")}
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {pg.bar}
      </>
    );
  }
  return (
    <div className="block" id="res-box">
      {head}
      {st?.err ? <ErrorBox>{st.err}</ErrorBox> : null}
      {body}
    </div>
  );
}
