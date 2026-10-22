import { t } from "@/lib/i18n";
import type { Check } from "./store";

// What a compose file creates, or why it is invalid.
export function CheckSummary({ r }: { r: Check | null }) {
  if (!r) return <span className="muted small">{t("Lendo…")}</span>;
  return (
    <div className="check-sum">
      {r.error ? (
        <div className="flash flash-err" style={{ margin: "0 0 10px" }}>
          {r.error}
        </div>
      ) : null}
      {r.services?.length ? (
        <>
          <h3 className="sub">Containers</h3>
          <div className="svc-list">
            {r.services.map((s) => (
              <div key={s.name}>
                <span className="name">{s.name}</span>
                <span className="mono small muted">{s.image}</span>
                {s.runtime ? <span className={"rt-chip rt-" + s.runtime}>{s.runtime}</span> : null}
                {s.ports.length ? <span className="mono small">{s.ports.join(" ")}</span> : null}
              </div>
            ))}
          </div>
        </>
      ) : null}
      {r.vms?.length ? (
        <>
          <h3 className="sub">VMs</h3>
          <div className="svc-list">
            {r.vms.map((v) => (
              <div key={v.name}>
                <span className="name">{v.name}</span>
                <span className="mono small muted">{v.image}</span>
              </div>
            ))}
          </div>
        </>
      ) : null}
      {r.networks?.length ? (
        <>
          <h3 className="sub">{t("Redes")}</h3>
          <p className="mono small" style={{ margin: 0 }}>
            {r.networks.join(", ")}
          </p>
        </>
      ) : null}
      {r.vars?.length ? (
        <>
          <h3 className="sub">{t("Variáveis")}</h3>
          <div className="svc-list">
            {r.vars.map((v) => (
              <div key={v.name}>
                <span className="name mono">{v.name}</span>
                {v.required ? <span className="badge warn">{t("obrigatória")}</span> : v.default ? <span className="mono small muted">{t("padrão {value}", { value: v.default })}</span> : <span className="small muted">{t("sem padrão")}</span>}
              </div>
            ))}
          </div>
        </>
      ) : null}
    </div>
  );
}
