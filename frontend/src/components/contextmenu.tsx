import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { create } from "zustand";
import { t } from "@/lib/i18n";

// The right-click menu of the items on screen (machines, clusters, projects,
// containers, VMs, objects of a cluster): the usual actions of the item,
// where the hand already is. One menu at a time, closed by a click outside,
// Esc, scrolling or leaving the window; arrows and Enter work too.

export type MenuItem = { label: string; onClick: () => void | Promise<void>; danger?: boolean; disabled?: boolean; hint?: string } | "-" | null | false | undefined;

type MenuState = { x: number; y: number; at: number; title?: string; items: Exclude<MenuItem, null | false | undefined>[] } | null;

const useMenu = create<{ menu: MenuState }>(() => ({ menu: null }));

// openMenu shows the menu at the mouse (separators at the ends or doubled
// are dropped, as are the items given as null or false).
export function openMenu(e: React.MouseEvent | MouseEvent, items: MenuItem[], title?: string) {
  const list: Exclude<MenuItem, null | false | undefined>[] = [];
  for (const it of items) {
    if (!it) continue;
    if (it === "-" && (!list.length || list[list.length - 1] === "-")) continue;
    list.push(it);
  }
  while (list.length && list[list.length - 1] === "-") list.pop();
  if (!list.length) return;
  e.preventDefault();
  e.stopPropagation();
  useMenu.setState({ menu: { x: e.clientX, y: e.clientY, at: Date.now(), title, items: list } });
}

export const closeMenu = () => useMenu.setState({ menu: null });

export function ContextMenuHost() {
  const menu = useMenu((s) => s.menu);
  const box = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ x: number; y: number } | null>(null);
  useLayoutEffect(() => {
    if (!menu || !box.current) return setPos(null);
    // Inside the window, as a native menu: flipped when it would not fit.
    const r = box.current.getBoundingClientRect();
    const x = menu.x + r.width > innerWidth - 4 ? Math.max(4, menu.x - r.width) : menu.x;
    const y = menu.y + r.height > innerHeight - 4 ? Math.max(4, menu.y - r.height) : menu.y;
    setPos({ x, y });
    box.current.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  }, [menu]);
  useEffect(() => {
    if (!menu) return;
    const down = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) closeMenu();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeMenu();
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const bs = [...(box.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? [])];
        const i = bs.indexOf(document.activeElement as HTMLButtonElement);
        bs[(i + (e.key === "ArrowDown" ? 1 : bs.length - 1)) % bs.length]?.focus();
      }
    };
    // A scroll that was already on its way (the wheel just before the
    // click) does not close the menu that just opened.
    const scroll = (e: Event) => {
      if (Date.now() - menu.at > 250 && !(box.current && box.current.contains(e.target as Node))) closeMenu();
    };
    addEventListener("mousedown", down, true);
    addEventListener("keydown", key, true);
    addEventListener("scroll", scroll, true);
    addEventListener("resize", closeMenu);
    addEventListener("blur", closeMenu);
    return () => {
      removeEventListener("mousedown", down, true);
      removeEventListener("keydown", key, true);
      removeEventListener("scroll", scroll, true);
      removeEventListener("resize", closeMenu);
      removeEventListener("blur", closeMenu);
    };
  }, [menu]);
  if (!menu) return null;
  return (
    <div
      ref={box}
      className="ctx-menu"
      role="menu"
      aria-label={menu.title || t("Ações")}
      style={{ left: (pos ?? menu).x, top: (pos ?? menu).y, visibility: pos ? "visible" : "hidden" }}
      onContextMenu={(e) => e.preventDefault()}
    >
      {menu.title ? <div className="ctx-title">{menu.title}</div> : null}
      {menu.items.map((it, i) =>
        it === "-" ? (
          <div key={i} className="ctx-sep" role="separator" />
        ) : (
          <button
            key={i}
            type="button"
            role="menuitem"
            className={"ctx-item" + (it.danger ? " danger" : "")}
            disabled={it.disabled}
            title={it.hint}
            onClick={() => {
              closeMenu();
              void it.onClick();
            }}
          >
            {it.label}
          </button>
        ),
      )}
    </div>
  );
}

// copyMenu: an item that copies a text (an address, a path, an id).
export function copyItem(label: string, text: string, done: () => void): MenuItem {
  return {
    label,
    onClick: async () => {
      try {
        await navigator.clipboard.writeText(text);
        done();
      } catch {
        /* no clipboard */
      }
    },
  };
}
