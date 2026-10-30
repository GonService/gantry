// The small ⋯ menus (details.more-menu.mini) live inside tables that
// scroll sideways (.table-wrap), and a scrolling box clips what goes past
// its edge: the menu of the last rows was cut off. When one opens, its list
// is placed on the screen (position: fixed) under the button, or above it
// when there is no room below; scrolling or resizing closes it.

const GAP = 6;
const MARGIN = 8;

function place(d: HTMLDetailsElement) {
  const btn = d.querySelector(":scope > summary");
  const list = d.querySelector<HTMLElement>(":scope > .more-list");
  if (!btn || !list) return;
  const r = btn.getBoundingClientRect();
  list.style.position = "fixed";
  list.style.top = "0";
  list.style.left = "0";
  list.style.right = "auto";
  list.style.zIndex = "60";
  const w = list.offsetWidth;
  const h = list.offsetHeight;
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  let top = r.bottom + GAP;
  if (top + h > vh - MARGIN && r.top - GAP - h >= MARGIN) top = r.top - GAP - h;
  top = Math.max(MARGIN, Math.min(top, vh - MARGIN - h));
  const left = Math.max(MARGIN, Math.min(r.right - w, vw - MARGIN - w));
  list.style.top = top + "px";
  list.style.left = left + "px";
}

function closeAll() {
  document.querySelectorAll<HTMLDetailsElement>("details.more-menu.mini[open]").forEach((d) => (d.open = false));
}

let installed = false;

export function installMenus() {
  if (installed) return;
  installed = true;
  // toggle does not bubble; capture sees it for every details.
  document.addEventListener(
    "toggle",
    (e) => {
      const d = e.target;
      if (d instanceof HTMLDetailsElement && d.open && d.matches(".more-menu.mini")) place(d);
    },
    true,
  );
  // A click outside closes it, like the other menus.
  document.addEventListener("mousedown", (e) => {
    const t = e.target as Node | null;
    document.querySelectorAll<HTMLDetailsElement>("details.more-menu.mini[open]").forEach((d) => {
      if (!t || !d.contains(t)) d.open = false;
    });
  });
  // The list is fixed on the screen: it would float away from its button.
  window.addEventListener("scroll", (e) => {
    const t = e.target;
    if (t instanceof Node && document.querySelector("details.more-menu.mini[open] > .more-list")?.contains(t)) return;
    closeAll();
  }, true);
  window.addEventListener("resize", closeAll);
}
