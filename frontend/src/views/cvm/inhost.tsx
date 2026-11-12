// The Container VMs in the VMs tab of this computer: they are VMs of this
// Mac too, so they show up there with the project VMs, with their state and
// the way to their own page (processes, logs and terminal, resources).
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useStore } from "@/lib/store";
import { t } from "@/lib/i18n";
import { cvmHref, cvmState, engineNames, power, STATE, type CvmList, type CvmView } from "./cvm";

const osName = (v: CvmView, list: CvmList | null) => {
  const os = v.spec.os || "debian";
  if (os === "custom") return t("sistema próprio");
  return list?.labels?.[os] || (os === "alpine" ? "Alpine" : "Debian");
};

// useCvmList reads the machines once per change of the engines' state (the
// sidebar keeps that state fresh), so nothing polls here.
export function useCvmList(): CvmList | null {
  const local = useStore((s) => s.local);
  const [list, setList] = useState<CvmList | null>(null);
  const key = (local?.machines || []).join(",") + "|" + JSON.stringify(local?.engines?.map((e: { id: string; state: string }) => e.id + e.state) || []);
  useEffect(() => {
    if (!local?.cvm || !(local.machines || []).length) {
      setList(null);
      return;
    }
    api<CvmList>("/cvm")
      .then(setList)
      .catch(() => setList(null));
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return list;
}

export function CvmInHost({ list }: { list: CvmList | null }) {
  const local = useStore((s) => s.local);
  if (!list || !list.machines.length) return null;
  return (
    <div className="block" style={{ marginTop: 0, marginBottom: 20 }}>
      <div className="block-head">
        <h2 className="block-title">{t("Container VMs")}</h2>
        <span className="muted small">{t("A máquina que roda os containers deste Mac")}</span>
      </div>
      <div className="panel table-wrap">
        <table className="grid">
          <thead>
            <tr>
              <th>{t("Nome")}</th>
              <th>{t("Estado")}</th>
              <th>{t("Sistema")}</th>
              <th>{t("Engines")}</th>
              <th>{t("CPU e memória")}</th>
              <th className="act">
                <span className="sr">{t("Ações")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.machines.map((v) => {
              const st = cvmState(local, v.name) || v.state;
              const [dot, label] = STATE[st] || ["", st];
              const on = st === "running";
              return (
                <tr key={v.name}>
                  <td>
                    <div className="cname">
                      <a className="cname-link" href={cvmHref(v.name)}>
                        <span className={"dot " + dot} />
                        {v.name}
                      </a>
                    </div>
                    <div className="muted small">
                      Container VM · Apple Virtualization{on && v.ip ? <span className="mono"> · IP {v.ip}</span> : null}
                    </div>
                  </td>
                  <td className="small">{label}</td>
                  <td>{osName(v, list)}</td>
                  <td>{engineNames(v.spec) || "—"}</td>
                  <td className="mono">
                    {v.spec.cpus} CPU · {Math.round(v.spec.memoryMiB / 1024)} GiB
                  </td>
                  <td className="act">
                    <a className="btn small" href={cvmHref(v.name)}>
                      {t("Abrir")}
                    </a>
                    {on ? (
                      <a className="btn small" href={cvmHref(v.name, "logs")}>
                        {t("Terminal")}
                      </a>
                    ) : null}
                    <button className="btn small" type="button" disabled={st === "starting" || st === "stopping"} onClick={() => void power(v.name, on ? "desligar" : "ligar")}>
                      {on ? t("Desligar") : t("Ligar")}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
