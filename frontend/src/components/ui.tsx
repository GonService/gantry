import { useState, type ReactNode } from "react";
import { copyText } from "@/lib/format";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";

export function Loading({ children = t("Carregando…") }: { children?: ReactNode }) {
  return (
    <div className="loading">
      <span className="spin" />
      {children}
    </div>
  );
}

export function ErrorBox({ children }: { children: ReactNode }) {
  return <div className="error-box">{children}</div>;
}

type BannerKind = "info" | "warn" | "err" | "drift";

export function Banner({ kind = "warn", children, actions }: { kind?: BannerKind; children: ReactNode; actions?: ReactNode }) {
  return (
    <div className={"banner " + kind}>
      <span className="grow">{children}</span>
      {actions}
    </div>
  );
}

export function Badge({ kind, children, title }: { kind?: "ok" | "warn" | "err" | "mixed" | ""; children: ReactNode; title?: string }) {
  return (
    <span className={"badge" + (kind ? " " + kind : "")} title={title}>
      {children}
    </span>
  );
}

export function Panel({ pad, className, children }: { pad?: boolean; className?: string; children: ReactNode }) {
  return <div className={["panel", pad ? "panel-pad" : "", className ?? ""].filter(Boolean).join(" ")}>{children}</div>;
}

// CopyCommand: a command to run by hand, with Copiar.
export function CopyCommand({ cmd }: { cmd: string }) {
  const copy = async () => {
    if (await copyText(cmd)) gantry.toast(t("Comando copiado."), "ok");
    else gantry.toast(t("Não deu para copiar; selecione o comando."), "err");
  };
  return (
    <div className="brew-cmd">
      <code>{cmd}</code>
      <button className="btn small" type="button" onClick={copy}>
        {t("Copiar")}
      </button>
    </div>
  );
}

// Button that disables itself while its action runs.
export function AsyncButton({ onClick, className = "btn", children, title }: { onClick: () => Promise<unknown>; className?: string; children: ReactNode; title?: string }) {
  const [busy, setBusy] = useState(false);
  return (
    <button
      type="button"
      className={className}
      title={title}
      disabled={busy}
      onClick={async () => {
        setBusy(true);
        try {
          await onClick();
        } catch (e) {
          gantry.toast(e instanceof Error ? e.message : String(e), "err");
        } finally {
          setBusy(false);
        }
      }}
    >
      {children}
    </button>
  );
}
