import { useEffect, useMemo, useRef } from "react";
import { localStore } from "@/shell/toasts";
import { gantry } from "@/lib/gantry";
import { enc } from "@/lib/api";
import { t } from "@/lib/i18n";
import { Banner, CopyCommand, ErrorBox, Loading } from "@/components/ui";
import { KubeMenu } from "./Menu";
import { NSPicker } from "./NSPicker";
import { Overview } from "./Overview";
import { HelmList, KubeList } from "./KubeList";
import { PermsView, SearchView, forbidden } from "./tools";
import { DetailPanel } from "./detail/Panel";
import { closeDetail } from "./detail/store";
import { applyDialog, forwardsDialog } from "./dialogs";
import { disconnectCluster } from "./actions";
import { clusterState, kubeNS, loadView, setKubeNS, useKube } from "./store";
import type { Cluster, KindInfo } from "./types";
import { kubeHref } from "./types";

export type KubePageProps = {
  cluster: Cluster | null; // null: not found
  loaded: boolean; // the list of clusters came
  kind: string;
  kinds: KindInfo[];
};

// kubeLabLink leads from a cluster made by Gantry to its project.
function LabLink({ c }: { c: Cluster }) {
  const [machine, lab] = (c.lab ?? "").split("|");
  const m = gantry.machines().find((x) => x.key === machine);
  if (!m || !m.hosts.length) return null;
  return (
    <div className="small" style={{ marginTop: 4 }}>
      {t("Criado pelo projeto")} <a href={gantry.labHref(m.hosts[0].name, lab)}>{lab}</a> {t("em {machine}", { machine: m.name })}
    </div>
  );
}

export function KubePage({ cluster: c, loaded, kind, kinds }: KubePageProps) {
  const layout = useRef<HTMLDivElement>(null);
  const id = c?.id ?? "";
  const st = useKube((s) => s.data[id]);
  const crds = useKube((s) => s.crds[id]?.kinds) ?? [];
  const forwards = useKube((s) => s.forwards[id]) ?? [];
  const nsState = useKube((s) => s.ns[id]);
  const ns = id ? (nsState ?? kubeNS(id)) : "";

  // Read the screen when it changes; then every 5 s (Helm and Secrets, every
  // minute), not while a dialog or a ⋯ menu is open (it would close them).
  useEffect(() => {
    if (!id) return;
    void loadView(id, kind);
    const timer = setInterval(() => {
      if (document.hidden || document.querySelector("dialog[open]") || document.querySelector("#kube-view details[open]")) return;
      const slow = kind === "helm" || kind === "secrets";
      const cur = useKube.getState().data[id];
      if (slow && cur && Date.now() - (cur.at || 0) < 60000) return;
      void loadView(id, kind, true);
    }, 5000);
    return () => clearInterval(timer);
  }, [id, kind]);
  // Another screen (back button, a link): the details panel goes.
  useEffect(() => {
    window.addEventListener("hashchange", closeDetail);
    return () => window.removeEventListener("hashchange", closeDetail);
  }, []);

  const kindOf = useMemo(() => (k: string) => kinds.find((x) => x.id === k) ?? crds.find((x) => x.id === k), [kinds, crds]);

  if (!loaded) return <div id="kube-view"><Loading /></div>;
  if (!c)
    return (
      <div id="kube-view">
        <div className="empty">
          <h1>{t("Cluster não encontrado")}</h1>
          <a className="btn" href="#/config/clusters">
            {t("Ver os clusters")}
          </a>
        </div>
      </div>
    );
  const namespaces = (st?.ov?.namespaces ? st.ov.namespaces.map((x) => x.name) : st?.namespaces) ?? [];
  const nsNames = [...new Set([...namespaces, ns].filter(Boolean))];
  const [dot] = clusterState(c.id);
  const special = kind === "helm" || kind === "busca" || kind === "permissoes";
  const data = kind && !special ? st?.lists[kind] : undefined;
  const hasData = kind ? (kind === "helm" ? st?.helm : data) : st?.ov;
  let body: React.ReactNode;
  if (kind === "busca") body = <SearchView c={c} />;
  else if (kind === "permissoes") body = <PermsView c={c} ns={ns} namespaces={nsNames} />;
  else if (st?.err && !hasData)
    body = (
      <ErrorBox>
        {st.err}
        {forbidden(st.err) ? (
          <>
            {" "}
            <a href={"#/k/" + enc(c.id) + "/permissoes?tipo=" + enc(kind)}>{t("Ver minhas permissões neste namespace")}</a>
          </>
        ) : null}
      </ErrorBox>
    );
  else if (!kind) body = <Overview c={c} ov={st?.ov} />;
  else if (kind === "helm") body = <HelmList c={c} list={st?.helm} ns={ns} />;
  else body = <KubeList key={kind} c={c} kind={kind} K={kindOf(kind)} data={data} ns={ns} />;

  return (
    <div id="kube-view">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs">
            <span>Cluster</span>
          </nav>
          <h1>
            <span className={"dot " + dot} /> {c.name}
            {c.mode === "leitura" ? (
              <span className="badge warn" style={{ marginLeft: 10 }} title={t("Muda em Configuração › Clusters")}>
                {t("só leitura")}
              </span>
            ) : null}
          </h1>
          <div className="sub">{[st?.ov?.server, st?.ov ? "Kubernetes " + st.ov.version : "", c.context].filter(Boolean).join(" · ")}</div>
          {c.source === "gantry" && c.lab ? <LabLink c={c} /> : null}
        </div>
        <div className="actions">
          <NSPicker
            clusterId={c.id}
            names={nsNames}
            cur={ns}
            pick={(v) => {
              setKubeNS(c.id, v);
              void loadView(c.id, kind);
            }}
          />
          {forwards.length ? (
            <button className="btn" type="button" title={t("Portas desta máquina que levam ao cluster")} onClick={() => forwardsDialog(c)}>
              {t("Portas encaminhadas")}<span className="count-badge">{forwards.length}</span>
            </button>
          ) : null}
          {c.mode === "leitura" ? null : (
            <button className="btn" type="button" onClick={() => applyDialog(c, kind)}>
              {t("Aplicar YAML…")}
            </button>
          )}
          <a className="btn" href={kubeHref(c.id, "permissoes")} title={t("O que a sua conta pode fazer em cada tipo, por namespace")}>
            {t("Minhas permissões")}
          </a>
          <button className="btn" type="button" onClick={() => void loadView(c.id, kind)}>
            {t("Atualizar")}
          </button>
          <button className="btn" type="button" title={t("Fecha a conexão com o cluster: as listas acompanhadas, o histórico de uso, as portas encaminhadas e as abas de logs e shell. Conecta de novo quando for aberto.")} onClick={() => void disconnectCluster(c)}>
            {t("Desconectar")}
          </button>
        </div>
      </div>
      {c.plugin ? (
        <Banner>
          {t("Este cluster entra com o {plugin}, que não está instalado neste computador. Sem ele, o login no cluster falha.", { plugin: c.plugin })}
          <CopyCommand cmd={c.pluginHint || t("instale o {plugin}", { plugin: c.plugin })} />
        </Banner>
      ) : null}
      <div className="kube-layout" ref={layout} style={{ "--kube-menu-w": menuW() + "px" } as React.CSSProperties}>
        <KubeMenu clusterId={c.id} kind={kind} crds={crds} />
        <MenuSplitter layout={layout} />
        <div className="kube-body">{body}</div>
      </div>
      <DetailPanel kinds={kindOf} routeCluster={c.id} />
    </div>
  );
}

// The width of the resource menu: dragged on the bar beside it (the list
// takes what is left), kept on this computer.
const MENU_MIN = 150;
const MENU_DEFAULT = 184;
const menuMax = (layout: HTMLElement | null) => Math.max(MENU_MIN, Math.min(640, (layout?.clientWidth ?? window.innerWidth) * 0.5));
const menuW = () => localStore.get("kubeMenuW", MENU_DEFAULT);

function MenuSplitter({ layout }: { layout: React.RefObject<HTMLDivElement | null> }) {
  const set = (w: number, save = true) => {
    const el = layout.current;
    const v = Math.round(Math.max(MENU_MIN, Math.min(menuMax(el), w)));
    el?.style.setProperty("--kube-menu-w", v + "px");
    if (save) localStore.set("kubeMenuW", v);
    return v;
  };
  const cur = () => parseInt(layout.current?.style.getPropertyValue("--kube-menu-w") || "", 10) || menuW();
  return (
    <div
      className="kube-split"
      role="separator"
      aria-orientation="vertical"
      aria-label={t("Largura do menu de recursos")}
      aria-valuenow={cur()}
      tabIndex={0}
      title={t("Arraste para mudar a largura do menu (duplo clique volta ao padrão)")}
      onDoubleClick={() => set(MENU_DEFAULT)}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
          e.preventDefault();
          set(cur() + (e.key === "ArrowRight" ? 1 : -1) * (e.shiftKey ? 64 : 16));
        }
      }}
      onPointerDown={(e) => {
        e.preventDefault();
        const grip = e.currentTarget;
        grip.setPointerCapture(e.pointerId);
        const left = layout.current?.getBoundingClientRect().left ?? 0;
        document.body.classList.add("resizing-x");
        const move = (ev: PointerEvent) => set(ev.clientX - left - 4, false);
        const up = () => {
          grip.removeEventListener("pointermove", move);
          grip.removeEventListener("pointerup", up);
          document.body.classList.remove("resizing-x");
          set(cur());
        };
        grip.addEventListener("pointermove", move);
        grip.addEventListener("pointerup", up);
      }}
    />
  );
}
