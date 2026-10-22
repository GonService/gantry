// The icons of the lab's topology: one per kind of device, drawn in a
// 32×32 box with the current color (Cisco-style symbols, simplified).
import type { ReactNode } from "react";
import { Modal, openDialog } from "@/components/dialog";
import { t } from "@/lib/i18n";

const arrow = (x1: number, y1: number, x2: number, y2: number) => {
  const a = Math.atan2(y2 - y1, x2 - x1);
  const h = 3.2;
  const p1 = [x2 - h * Math.cos(a - 0.6), y2 - h * Math.sin(a - 0.6)];
  const p2 = [x2 - h * Math.cos(a + 0.6), y2 - h * Math.sin(a + 0.6)];
  return `M${x1} ${y1}L${x2} ${y2}M${p1[0].toFixed(1)} ${p1[1].toFixed(1)}L${x2} ${y2}L${p2[0].toFixed(1)} ${p2[1].toFixed(1)}`;
};

export const ICONS: { id: string; label: string; draw: ReactNode }[] = [
  {
    id: "router",
    label: t("Roteador"),
    draw: (
      <>
        <ellipse cx="16" cy="16" rx="14" ry="13" className="ic-fill" />
        <path d={arrow(5, 12, 12, 12) + arrow(27, 20, 20, 20) + arrow(12, 22, 6, 22) + arrow(20, 10, 26, 10)} />
      </>
    ),
  },
  {
    id: "switch",
    label: t("Switch"),
    draw: (
      <>
        <rect x="2" y="8" width="28" height="16" rx="2" className="ic-fill" />
        <path d={arrow(6, 13, 20, 13) + arrow(26, 19, 12, 19)} />
      </>
    ),
  },
  {
    id: "l3switch",
    label: t("Switch L3"),
    draw: (
      <>
        <rect x="3" y="3" width="26" height="26" rx="3" className="ic-fill" />
        <path d={arrow(16, 16, 16, 6) + arrow(16, 16, 16, 26) + arrow(16, 16, 6, 16) + arrow(16, 16, 26, 16)} />
      </>
    ),
  },
  {
    id: "firewall",
    label: t("Firewall"),
    draw: (
      <>
        <rect x="3" y="5" width="26" height="22" rx="1.5" className="ic-fill" />
        <path d="M3 12.3h26M3 19.6h26M11 5v7.3M21 5v7.3M7 12.3v7.3M16 12.3v7.3M25 12.3v7.3M11 19.6v7.4M21 19.6v7.4" />
      </>
    ),
  },
  {
    id: "server",
    label: t("Servidor"),
    draw: (
      <>
        <rect x="8" y="2" width="16" height="28" rx="2" className="ic-fill" />
        <path d="M11 7h10M11 11h10M11 15h10" />
        <circle cx="16" cy="24" r="1.6" />
      </>
    ),
  },
  {
    id: "pc",
    label: t("Computador"),
    draw: (
      <>
        <rect x="3" y="4" width="26" height="18" rx="2" className="ic-fill" />
        <path d="M12 28h8M16 22v6" />
      </>
    ),
  },
  {
    id: "container",
    label: t("Container"),
    draw: (
      <>
        <path d="M16 3l12 6v14l-12 6-12-6V9z" className="ic-fill" />
        <path d="M4 9l12 6 12-6M16 15v14" />
      </>
    ),
  },
  {
    id: "cloud",
    label: t("Nuvem / Internet"),
    draw: <path d="M9 25h15a6 6 0 0 0 .6-12 8 8 0 0 0-15.2-1.6A6.8 6.8 0 0 0 9 25z" className="ic-fill" />,
  },
  {
    id: "appliance",
    label: t("Equipamento"),
    draw: (
      <>
        <rect x="2" y="9" width="28" height="14" rx="2" className="ic-fill" />
        <path d="M6 16h2M10 16h2M14 16h2" />
        <circle cx="25" cy="16" r="1.6" />
      </>
    ),
  },
];

export const iconOf = (id: string) => ICONS.find((i) => i.id === id) ?? ICONS[ICONS.length - 1];

// NodeIcon draws an icon at (x, y), size × size.
export function NodeIcon({ id, x, y, size }: { id: string; x: number; y: number; size: number }) {
  return (
    <g className={"cm-icon ic-" + id} transform={`translate(${x} ${y}) scale(${size / 32})`}>
      {iconOf(id).draw}
    </g>
  );
}

// pickIcon asks which icon a node gets ("" : the default of its kind).
export function pickIcon(node: string, cur: string, isSet: boolean, onPick: (id: string) => void) {
  openDialog((close) => (
    <Modal title={t("Ícone de {node}", { node })} onClose={close}>
      <div className="icon-grid">
        {ICONS.map((ic) => (
          <button
            key={ic.id}
            type="button"
            className={"icon-pick" + (ic.id === cur ? " on" : "")}
            onClick={() => {
              close();
              if (ic.id !== cur || !isSet) onPick(ic.id);
            }}
          >
            <svg width="40" height="40" viewBox="0 0 32 32" className="cm-icon-svg">
              <g className={"cm-icon ic-" + ic.id}>{ic.draw}</g>
            </svg>
            <span>{ic.label}</span>
          </button>
        ))}
      </div>
      {isSet ? (
        <button
          className="btn small btn-ghost"
          type="button"
          onClick={() => {
            close();
            onPick("");
          }}
        >
          {t("Voltar ao ícone automático")}
        </button>
      ) : null}
    </Modal>
  ));
}
