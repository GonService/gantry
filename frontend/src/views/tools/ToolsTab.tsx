import { useEffect } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { AsyncButton, Badge, Banner, CopyCommand, ErrorBox, Loading } from "@/components/ui";
import { needsLoad, toolsURL, useTools, type Tool } from "./store";
import { t } from "@/lib/i18n";

export type ToolsTabProps = { mkey: string; local: boolean; host?: string };

function stateBadge(x: Tool) {
  if (x.have) return <Badge kind="ok">{t("instalado")}</Badge>;
  if (x.need === "opcional") return <Badge>{t("opcional")}</Badge>;
  return <Badge kind={x.need === "essencial" ? "err" : "warn"}>{t("falta")}</Badge>;
}

export function ToolsTab({ mkey, local, host }: ToolsTabProps) {
  const url = toolsURL(local, host);
  const entry = useTools((s) => s.byKey[mkey]);
  const load = useTools((s) => s.load);
  useEffect(() => {
    if (needsLoad(entry)) void load(mkey, url);
  }, [mkey, url]); // eslint-disable-line react-hooks/exhaustive-deps

  const head = (
    <Banner
      kind="info"
      actions={
        <button className="btn small" type="button" onClick={() => load(mkey, url, true)}>
          {t("Verificar de novo")}
        </button>
      }
    >
      {local
        ? t("O que o Gantry usa neste computador. Para os clusters não é preciso instalar nada (nem o kubectl): o Gantry fala direto com a API.")
        : t("O que o Gantry usa neste servidor. Os comandos de instalação precisam de sudo: rode-os no terminal do servidor.")}
    </Banner>
  );
  if (!entry?.data) {
    return (
      <div>
        {head}
        {entry?.err ? <ErrorBox>{entry.err}</ErrorBox> : <Loading>{t("Verificando…")}</Loading>}
      </div>
    );
  }
  const r = entry.data;
  const areas = [...new Set(r.tools.map((x) => x.area))];
  const install = async (tool: Tool) => {
    const j = await api<{ id: string }>("/local/ferramentas/" + enc(tool.id), { method: "POST" });
    gantry.openJob(j.id, t("Instalar {name}", { name: tool.name }), () => void load(mkey, url, true));
  };
  return (
    <div>
      {head}
      {areas.map((area) => (
        <div className="panel" key={area}>
          <h2 className="tools-area">{area}</h2>
          <table className="table tools-table">
            <tbody>
              {r.tools
                .filter((x) => x.area === area)
                .map((x) => (
                  <tr key={x.id}>
                    <td className="tool-state">{stateBadge(x)}</td>
                    <td>
                      <div className="tool-name">
                        {x.name}
                        {x.have && x.found ? <span className="muted small"> · {x.found}</span> : null}
                      </div>
                      <div className="muted small">{x.why}</div>
                      {!x.have && x.note ? <div className="small">{x.note}</div> : null}
                      {!x.have && x.install ? <CopyCommand cmd={x.install} /> : null}
                    </td>
                    <td className="row-actions">
                      {!x.have && x.brew && r.local ? (
                        <AsyncButton className="btn small primary" onClick={() => install(x)}>
                          {t("Instalar")}
                        </AsyncButton>
                      ) : null}
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      ))}
    </div>
  );
}

export type ToolWarningProps = { mkey: string; local: boolean; host?: string; needs: string[]; href: string };

// ToolWarning: a banner when the machine misses what a project needs.
export function ToolWarning({ mkey, local, host, needs, href }: ToolWarningProps) {
  const url = toolsURL(local, host);
  const entry = useTools((s) => s.byKey[mkey]);
  const load = useTools((s) => s.load);
  useEffect(() => {
    if (needsLoad(entry)) void load(mkey, url);
  }, [mkey, url]); // eslint-disable-line react-hooks/exhaustive-deps
  const miss = entry?.data?.tools.filter((x) => needs.includes(x.id) && !x.have) ?? [];
  if (!miss.length) return null;
  return (
    <Banner
      actions={
        <a className="btn small" href={href}>
          {t("Ver ferramentas")}
        </a>
      }
    >
      {t("Falta nesta máquina: {names}.", { names: miss.map((x) => x.name).join(", ") })} {miss.map((x) => x.why).join(" ")}
    </Banner>
  );
}

// The count of missing tools, for the tab of the machine page.
export function useMissingTools(mkey: string): number | null {
  return useTools((s) => s.byKey[mkey]?.data?.missing ?? null);
}
