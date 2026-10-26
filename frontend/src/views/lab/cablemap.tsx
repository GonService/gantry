// The drawing of a network lab: the nodes (VMs, IOL, containers on cables)
// with their ports, the cables between them and the lab networks (x-redes)
// as clouds the ports plug into. Dragging a node or a cloud moves it (the
// position goes to the compose); dragging from one port to another plugs a
// cable, onto a cloud puts the port on that network (live, as on the Cables
// panel); clicking a cable or a cloud unplugs, captures, edits.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { openMenu, type MenuItem } from "@/components/contextmenu";
import { confirm, formDialog } from "@/components/dialog";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { NodeIcon, pickIcon } from "./icons";
import { addrText, attach, captureItems, fileError, CABLES_CHANGED, cablesChanged, connect, dynPort, ifaceBody, peerOf, portName, redeCaptureItems, setAddr, takesAddr, unplug, type CVM, type Lease, type Rede, type Redes } from "./cables";
import { loadDrift } from "./drift";
import type { Lab } from "./types";
import { NOTE_CORES, NOTE_H, NOTE_W, newNoteID, noteHTML, type Nota } from "./notes";

const NODE_H = 72;
const GAP = 30; // between ports
const PAD = 18; // from the node's side to its first port
const MIN_W = 140;
const CLOUD_W = 150;
const CLOUD_H = 58;
const CK = "\u0001"; // the key of a cloud in the positions (a network may have a node's name… not allowed, but keys stay apart)

type State = "on" | "warn" | "";
export type Drawing = { vms: CVM[]; redes: Rede[]; notas?: Nota[] };
type Side = "t" | "b" | "l" | "r";
type Port = { x: number; y: number; side: Side };
// The way out of each side of a node.
const NORMAL: Record<Side, [number, number]> = { t: [0, -1], b: [0, 1], l: [-1, 0], r: [1, 0] };
type End = { node: string; port: number };
type Drag =
  | { kind: "node"; node: string; dx: number; dy: number; moved: boolean }
  | { kind: "cloud"; rede: string; dx: number; dy: number; moved: boolean }
  | { kind: "port"; from: End; x: number; y: number }
  | { kind: "note"; id: string; dx: number; dy: number; moved: boolean }
  | { kind: "note-size"; id: string; x0: number; y0: number; w0: number; h0: number; moved: boolean };

const nodeW = (v: CVM) => Math.max(MIN_W, PAD * 2 + Math.max(0, v.interfaces.length - 1) * GAP);

// The short name of a port, drawn next to it.
const shortPort = (v: CVM, i: number) => (v.kind === "vpcs" ? "eth0" : v.kind === "dyn" ? v.ports?.[i] ?? dynPort(v.platform, i) : v.kind === "iol" ? "e" + Math.floor(i / 4) + "/" + (i % 4) : v.kind === "ctr" ? "eth" + (i + 1) : "n" + (i + (v.management ? 1 : 0)));

const KIND_LABEL: Record<string, string> = { iol: "IOL", ctr: "container", vpcs: "VPCS" };

// layout: the position of each node; the ones never placed go on a grid
// after the placed ones; the clouds never placed, in a column to the right.
export function layout(nodes: CVM[], redes: Rede[] = []): Record<string, [number, number]> {
  const out: Record<string, [number, number]> = {};
  const right = Math.max(0, ...nodes.filter((n) => n.pos).map((n) => n.pos![0] + nodeW(n)));
  let c = 0;
  for (const r of redes) {
    if (r.pos) out[CK + r.name] = [r.pos[0], r.pos[1]];
    else out[CK + r.name] = [right + 120, 40 + c++ * (CLOUD_H + 60)];
  }
  let k = 0;
  const free = nodes.filter((n) => !n.pos);
  const cols = Math.max(1, Math.ceil(Math.sqrt(free.length)));
  const colW = Math.max(260, ...free.map((n) => nodeW(n) + 90));
  const placed = nodes.filter((n) => n.pos);
  const placedClouds = redes.filter((r) => r.pos);
  const bottoms = [...placed.map((n) => n.pos![1] + NODE_H + 60), ...placedClouds.map((r) => r.pos![1] + CLOUD_H + 60)];
  const bottom = bottoms.length ? Math.max(...bottoms) : 0;
  for (const n of nodes) {
    if (n.pos) out[n.name] = [n.pos[0], n.pos[1]];
    else {
      out[n.name] = [40 + (k % cols) * colW, bottom + 50 + Math.floor(k / cols) * 210];
      k++;
    }
  }
  if (!nodes.some((n) => n.pos) && free.length) {
    // nothing placed yet: the clouds go to the right of the grid
    const gridRight = 40 + Math.min(cols, free.length) * colW;
    c = 0;
    for (const r of redes) if (!r.pos) out[CK + r.name] = [gridRight + 40, 40 + c++ * (CLOUD_H + 60)];
  }
  // Positions above or left of the drawing (a file written by hand, pos:
  // [-120, -300]) would stay out of reach: the whole drawing moves in.
  const all = Object.values(out);
  if (all.length) {
    const dx = Math.max(0, 20 - Math.min(...all.map((p) => p[0])));
    const dy = Math.max(0, 20 - Math.min(...all.map((p) => p[1])));
    if (dx || dy) for (const k of Object.keys(out)) out[k] = [out[k][0] + dx, out[k][1] + dy];
  }
  return out;
}

export function CableMap({ host, lab, states, drawing }: { host: string; lab: Lab; states: Record<string, State>; drawing?: Drawing }) {
  const base = "/hosts/" + enc(host) + "/labs/" + enc(lab.id);
  const [nodes, setNodes] = useState<CVM[] | null>(null);
  const [net, setNet] = useState<Redes>({ redes: [], leases: [] });
  const [selRede, setSelRede] = useState(""); // the network whose panel is open
  const [pos, setPos] = useState<Record<string, [number, number]>>({});
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [drag, setDrag] = useState<Drag | null>(null);
  const [notas, setNotas] = useState<Nota[]>([]);
  const [hover, setHover] = useState("");
  const [sel, setSel] = useState(""); // the node whose panel is open
  const svg = useRef<SVGSVGElement>(null);
  const wrap = useRef<HTMLDivElement>(null);
  // Zoom of the drawing, kept per lab in this browser; null: not chosen yet
  // (a drawing wider than the panel starts fitted to it).
  const zkey = "gantry.zoom." + host + "/" + lab.id;
  const [zoom, setZoomRaw] = useState<number | null>(() => {
    try {
      const v = Number(localStorage.getItem(zkey));
      return v > 0 ? v : null;
    } catch {
      return null;
    }
  });
  const setZoom = (z: number) => {
    const v = Math.min(2, Math.max(0.25, Math.round(z * 100) / 100));
    setZoomRaw(v);
    try {
      localStorage.setItem(zkey, String(v));
    } catch {
      /* nothing */
    }
  };
  // Straight cables (as in Packet Tracer) or curves, kept in this browser.
  const lkey = "gantry.cabos.linhas";
  const [lines, setLinesRaw] = useState<"retas" | "curvas">(() => {
    try {
      return localStorage.getItem(lkey) === "curvas" ? "curvas" : "retas";
    } catch {
      return "retas";
    }
  });
  const setLines = (v: "retas" | "curvas") => {
    setLinesRaw(v);
    try {
      localStorage.setItem(lkey, v);
    } catch {
      /* nothing */
    }
  };
  // The mouse over a node, a network or a cable: its cables stand out and
  // the others fade.
  const [focus, setFocus] = useState("");
  const dragging = useRef(false);
  dragging.current = !!drag || busy;
  // No zoom chosen: a drawing wider than the panel starts fitted to it.
  useLayoutEffect(() => {
    if (zoom !== null || !wrap.current || !svg.current) return;
    const w = svg.current.viewBox.baseVal?.width;
    if (w) setZoomRaw(Math.max(0.25, Math.min(1, (wrap.current.clientWidth - 4) / w)));
  }, [nodes, zoom]);
  // Moving around the drawing: drag the empty background, or anywhere with
  // Space held or the middle button; Ctrl/⌘ + wheel zooms at the mouse.
  const [space, setSpace] = useState(false);
  const [panning, setPanning] = useState(false);
  const pan = useRef<{ x: number; y: number; sl: number; st: number; id: number } | null>(null);
  const over = useRef(false);
  const anchor = useRef<{ px: number; py: number; cx: number; cy: number } | null>(null);
  useEffect(() => {
    const typing = (el: EventTarget | null) => el instanceof HTMLElement && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
    const down = (e: KeyboardEvent) => {
      if (e.code !== "Space" || !over.current || typing(e.target) || e.ctrlKey || e.metaKey || e.altKey) return;
      e.preventDefault(); // not the page scrolling
      setSpace(true);
    };
    const up = (e: KeyboardEvent) => e.code === "Space" && setSpace(false);
    const off = () => setSpace(false);
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    window.addEventListener("blur", off);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
      window.removeEventListener("blur", off);
    };
  }, []);
  useLayoutEffect(() => {
    const a = anchor.current;
    const w = wrap.current;
    if (!a || !w || zoom === null) return;
    anchor.current = null;
    w.scrollLeft = a.px * zoom - a.cx;
    w.scrollTop = a.py * zoom - a.cy;
  }, [zoom]);
  const panStart = (e: React.PointerEvent) => {
    const bg = e.button === 0 && (e.target === svg.current || (e.target as Element).classList?.contains("cm-bg"));
    if (!(e.button === 1 || (e.button === 0 && space) || bg) || !wrap.current) return;
    e.preventDefault();
    e.stopPropagation();
    wrap.current.setPointerCapture?.(e.pointerId);
    pan.current = { x: e.clientX, y: e.clientY, sl: wrap.current.scrollLeft, st: wrap.current.scrollTop, id: e.pointerId };
    setPanning(true);
  };
  const panMove = (e: React.PointerEvent) => {
    const p = pan.current;
    if (!p || !wrap.current) return;
    e.stopPropagation();
    wrap.current.scrollLeft = p.sl - (e.clientX - p.x);
    wrap.current.scrollTop = p.st - (e.clientY - p.y);
  };
  const panEnd = (e: React.PointerEvent) => {
    if (!pan.current) return;
    e.stopPropagation();
    wrap.current?.releasePointerCapture?.(pan.current.id);
    pan.current = null;
    setPanning(false);
  };

  // The drawing (nodes, cables, networks): it comes with the topology the
  // page already reads every few seconds, and is read here only after a
  // change; what changes by the second (lights, DHCP leases) comes apart,
  // below.
  const apply = (r: Drawing) => {
    if (dragging.current) return;
    setNodes(r.vms);
    setNet((n) => ({ redes: r.redes, leases: n.leases }));
    setNotas(r.notas ?? []);
    setPos(layout(r.vms, r.redes));
    setErr("");
  };
  const load = () =>
    api<Drawing>(base + "/desenho")
      .then(apply)
      .catch((e) => setErr((e as Error).message));
  useEffect(() => {
    if (drawing) apply(drawing);
  }, [drawing]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!drawing) void load();
    const on = (e: Event) => (e as CustomEvent).detail === lab.id && void load();
    window.addEventListener(CABLES_CHANGED, on);
    return () => window.removeEventListener(CABLES_CHANGED, on);
  }, [base]); // eslint-disable-line react-hooks/exhaustive-deps

  // The link lights, as in Packet Tracer: when each port last sent a frame
  // through the cables (seconds ago). null: not known (the relay of an
  // older Gantry, or the lab is down).
  const [seen, setSeen] = useState<Record<string, number> | null>(null);
  const anyOn = Object.values(states).some((x) => x === "on");
  useEffect(() => {
    let live = true;
    const get = () =>
      !document.hidden &&
      api<{ ports: { node: string; port: number; ago: number }[]; leases: Lease[] }>(base + "/vivo")
        .then((r) => {
          if (!live) return;
          const m: Record<string, number> = {};
          for (const x of r.ports) m[x.node + "\u0000" + x.port] = x.ago;
          setSeen(anyOn ? m : null);
          setNet((n) => ({ ...n, leases: r.leases }));
        })
        .catch(() => live && setSeen(null));
    void get();
    // Every 5 s while something runs; a lab that is down changes little.
    const timer = setInterval(() => void get(), anyOn ? 5000 : 30000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [base, anyOn]);
  // light: the state of a port. A Cisco interface that is up sends
  // keepalives (CDP, spanning tree on a switch) every few seconds, and one in
  // shutdown sends nothing: it follows the traffic. VPCS, containers and VMs
  // have no shutdown seen from outside: lit while the machine runs.
  const LIGHT_AGE = 30;
  const light = (name: string, i: number): { st: "up" | "down" | "off"; why: string } | null => {
    const v = nodes?.find((x) => x.name === name);
    if (!v) return null;
    if (states[name] !== "on") return { st: "off", why: t("equipamento parado") };
    if (v.kind !== "iol" && v.kind !== "dyn") return { st: "up", why: t("ligado") };
    if (!seen) return null;
    const ago = seen[name + "\u0000" + i];
    if (ago === undefined) return { st: "down", why: t("nada saiu desta porta: interface em shutdown, ou o equipamento ainda está iniciando") };
    return ago <= LIGHT_AGE ? { st: "up", why: t("ativa (tráfego há {s} s)", { s: ago }) } : { st: "down", why: t("sem tráfego há {s} s: interface em shutdown?", { s: ago }) };
  };

  // Where each port is drawn: on the side of the node that faces what it
  // is wired to (a node, a network), in the order of those along the side,
  // so the cables leave toward their peers instead of crossing the node.
  // A free port stays at its place on the bottom.
  const ports = useMemo(() => {
    const m: Record<string, Port> = {};
    if (!nodes) return m;
    const center = (name: string): [number, number] | null => {
      const v = nodes.find((x) => x.name === name);
      const p = pos[name];
      return v && p ? [p[0] + nodeW(v) / 2, p[1] + NODE_H / 2] : null;
    };
    for (const v of nodes) {
      const [x, y] = pos[v.name] ?? [0, 0];
      const w = nodeW(v);
      const cx = x + w / 2;
      const cy = y + NODE_H / 2;
      const by: Record<Side, { i: number; k: number; d: number }[]> = { t: [], b: [], l: [], r: [] };
      v.interfaces.forEach((f, i) => {
        const peer = f.link ? peerOf(nodes, f.link, [v.name, i]) : null;
        const cl = f.rede ? pos[CK + f.rede] : undefined;
        const tgt = peer && peer[0] !== v.name ? center(peer[0]) : cl ? ([cl[0] + CLOUD_W / 2, cl[1] + CLOUD_H / 2] as [number, number]) : null;
        if (!tgt) {
          by.b.push({ i, k: x + PAD + i * GAP, d: 0 });
          return;
        }
        const dx = tgt[0] - cx;
        const dy = tgt[1] - cy;
        const side: Side = Math.abs(dx) * NODE_H > Math.abs(dy) * w ? (dx > 0 ? "r" : "l") : dy > 0 ? "b" : "t";
        by[side].push({ i, k: side === "l" || side === "r" ? tgt[1] : tgt[0], d: dy });
      });
      // A side of the node has room for two ports: the others go up or down.
      for (const sd of ["l", "r"] as Side[]) {
        by[sd].sort((a, b) => Math.abs(a.d) - Math.abs(b.d));
        while (by[sd].length > 2) {
          const e = by[sd].pop()!;
          by[e.d > 0 ? "b" : "t"].push({ ...e, k: sd === "l" ? x - 1 : x + w + 1 });
        }
      }
      for (const sd of ["t", "b", "l", "r"] as Side[]) {
        const list = by[sd].sort((a, b) => a.k - b.k || a.i - b.i);
        list.forEach((e, n) => {
          const f = (n + 1) / (list.length + 1);
          m[v.name + "\u0000" + e.i] =
            sd === "t" ? { x: x + w * f, y, side: sd } : sd === "b" ? { x: x + w * f, y: y + NODE_H, side: sd } : sd === "l" ? { x, y: y + NODE_H * f, side: sd } : { x: x + w, y: y + NODE_H * f, side: sd };
        });
      }
    }
    return m;
  }, [nodes, pos]);
  const redes = net.redes;

  if (err && !nodes) return null;
  if (!nodes || (!nodes.length && !redes.length)) return null;

  const at = (e: { clientX: number; clientY: number }) => {
    const r = svg.current?.getBoundingClientRect();
    if (!r) return { x: 0, y: 0 };
    return { x: (e.clientX - r.left) / z, y: (e.clientY - r.top) / z };
  };
  const portAt = (e: { clientX: number; clientY: number }): End | null => {
    const el = document.elementFromPoint(e.clientX, e.clientY)?.closest("[data-port]") as SVGElement | null;
    if (!el) return null;
    const v = el.dataset.port ?? "";
    const k = v.lastIndexOf("|");
    return { node: v.slice(0, k), port: Number(v.slice(k + 1)) };
  };
  const cloudAt = (e: { clientX: number; clientY: number }): string => (document.elementFromPoint(e.clientX, e.clientY)?.closest("[data-rede]") as SVGElement | null)?.dataset.rede ?? "";

  const saveCables = async (next: CVM[]) => {
    const body: Record<string, ReturnType<typeof ifaceBody>[]> = {};
    for (const v of next) {
      const o = nodes.find((x) => x.name === v.name);
      if (JSON.stringify(o?.interfaces) !== JSON.stringify(v.interfaces)) body[v.name] = v.interfaces.map(ifaceBody);
    }
    if (!Object.keys(body).length) return;
    setBusy(true);
    setNodes(next);
    try {
      const r = await api<{ live?: boolean; error?: string }>(base + "/cabos", { method: "PUT", body: { vms: body } });
      if (r.error) gantry.toast(r.error, "err");
      else gantry.toast(r.live ? t("Cabos trocados ao vivo, sem reiniciar as VMs.") : t("Cabos salvos no compose. Valem quando as VMs subirem."), "ok");
      loadDrift(host, lab.id, true);
    } catch (e) {
      gantry.toast(fileError((e as Error).message), "err");
    } finally {
      setBusy(false);
      cablesChanged(lab.id);
    }
  };

  // setIcon writes the icon of a node in the compose.
  const setIcon = async (v: CVM, icon: string) => {
    setNodes(nodes.map((x) => (x.name === v.name ? { ...x, icon: icon || x.icon, iconSet: !!icon } : x)));
    try {
      await api(base + "/icones", { method: "PUT", body: { icons: { [v.name]: icon } } });
    } catch (e) {
      gantry.toast(fileError((e as Error).message), "err");
    }
    cablesChanged(lab.id);
  };

  // join puts a port on a network (out of its cable or bridge first).
  const join = async (a: End, rede: string) => {
    const va = nodes.find((v) => v.name === a.node)!;
    const fa = va.interfaces[a.port];
    if (fa.rede === rede) return;
    const pa = fa.link ? peerOf(nodes, fa.link, [a.node, a.port]) : null;
    if (pa || fa.bridge || fa.rede) {
      const from = pa ? pa[0] + " · " + portName(nodes.find((v) => v.name === pa[0])!, pa[1]) : fa.bridge ? "bridge " + fa.bridge : t("rede {name}", { name: fa.rede! });
      if (!(await confirm(t("Mudar a porta de lugar?"), t("{a} sai de {b} e entra na rede {rede}.", { a: va.name + " · " + portName(va, a.port), b: from, rede }), t("Mudar")))) return;
    }
    await saveCables(attach(nodes, a.node, a.port, rede));
  };

  // editAddr asks the addresses of a port and saves them.
  const editAddr = (v: CVM, i: number) => {
    const f = v.interfaces[i];
    const r = f.rede ? redes.find((x) => x.name === f.rede) : undefined;
    formDialog({
      title: t("Endereços de {vm} · {port}", { vm: v.name, port: portName(v, i) }),
      intro: r
        ? t("Na rede {rede}{sub}. Um ou mais IP/máscara separados por vírgula, dhcp para pedir à rede, ou vazio para configurar dentro da máquina. Vale quando a VM é criada (o container, quando sobe).", { rede: r.name, sub: r.subnet ? " (" + r.subnet + (r.dhcp ? ", DHCP " + r.dhcpFrom + "–" + r.dhcpTo : "") + ")" : "" })
        : t("Um ou mais IP/máscara separados por vírgula, ou vazio para configurar dentro da máquina. Vale quando a VM é criada (o container, quando sobe)."),
      submit: t("Salvar"),
      fields: [{ id: "a", label: t("Endereços"), value: addrText(f), placeholder: r?.subnet ? r.subnet.replace(/\.0\/(\d+)$/, ".10/$1") : "10.0.0.1/30", optional: true }],
      onSubmit: async (vals) => {
        await saveCables(setAddr(nodes, v.name, i, String(vals.a ?? "")));
      },
    });
  };

  // Networks: create or change one (the dialog), remove one.
  const redeDialog = (r?: Rede) =>
    formDialog({
      title: r ? t("Rede {name}", { name: r.name }) : t("Nova rede do lab"),
      intro: t("Uma rede do lab é um switch: várias portas entram nela. A subnet e o DHCP são opcionais; sem eles, os IPs se configuram dentro das máquinas, como num roteador."),
      submit: r ? t("Salvar") : t("Criar"),
      fields: [
        { id: "name", label: t("Nome"), value: r?.name ?? "lan-" + String.fromCharCode(97 + redes.length), placeholder: "lan-a" },
        { id: "subnet", label: t("Subnet"), value: r?.subnet ?? "", placeholder: "10.0." + (redes.length + 1) + ".0/24", optional: true },
        { id: "gateway", label: t("Gateway"), value: r?.gateway ?? "", placeholder: "10.0." + (redes.length + 1) + ".1", optional: true },
        { id: "dhcp", label: t("DHCP: a rede entrega endereços às portas com ip: dhcp"), type: "checkbox", value: !!r?.dhcp },
        { id: "range", label: t("Faixa do DHCP"), value: r?.dhcp && r.dhcpFrom ? r.dhcpFrom + "-" + r.dhcpTo : "", placeholder: t("padrão: .100 a .199"), optional: true },
        { id: "dns", label: t("DNS"), value: (r?.dns ?? []).join(", "), placeholder: "1.1.1.1", optional: true },
      ],
      onSubmit: async (vals) => {
        const name = String(vals.name ?? "").trim();
        if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(name)) throw new Error(t("Nome inválido: use letras, números, ponto, - e _."));
        const resp = await api<{ live?: boolean; error?: string }>(base + "/redes/" + enc(r?.name ?? name), {
          method: "PUT",
          body: { name, subnet: vals.subnet, gateway: vals.gateway, dhcp: !!vals.dhcp, range: vals.range, dns: String(vals.dns ?? "").split(/[\s,;]+/).filter(Boolean) },
        });
        if (resp.error) gantry.toast(resp.error, "err");
        else gantry.toast(r ? t("Rede {name} salva.", { name }) : t("Rede {name} criada. Arraste uma porta até a nuvem para ligá-la.", { name }), "ok");
        if (r && selRede === r.name) setSelRede(name);
        cablesChanged(lab.id);
        loadDrift(host, lab.id, true);
      },
    });
  const removeRede = async (r: Rede) => {
    const n = r.ports?.length ?? 0;
    if (!(await confirm(t("Remover a rede {name}?", { name: r.name }), n ? t("As {n} porta(s) nela ficam sem cabo.", { n }) : t("Nenhuma porta está nela."), t("Remover")))) return;
    try {
      const resp = await api<{ error?: string }>(base + "/redes/" + enc(r.name), { method: "DELETE" });
      if (resp.error) gantry.toast(resp.error, "err");
      setSelRede("");
      cablesChanged(lab.id);
      loadDrift(host, lab.id, true);
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  const redeMenu = (e: React.MouseEvent, r: Rede) => {
    e.preventDefault();
    if (busy) return;
    openMenu(e, [...redeCaptureItems(host, lab.id, r.name), "-", { label: t("Portas e endereços…"), onClick: () => setSelRede(r.name) }, { label: t("Editar a rede…"), onClick: () => redeDialog(r) }, { label: t("Remover a rede"), danger: true, onClick: () => void removeRede(r) }], t("rede {name}", { name: r.name }) + (r.subnet ? " · " + r.subnet : ""));
  };
  const leaseOf = (node: string, port: number): Lease | undefined => net.leases.find((l) => l.node === node && l.port === String(port));
  const memberMenu = (e: React.MouseEvent, a: End, rede: string) => {
    if (busy) return;
    const va = nodes.find((v) => v.name === a.node)!;
    openMenu(
      e,
      [
        { label: t("Tirar da rede"), danger: true, onClick: () => void saveCables(unplug(nodes, a.node, a.port)) },
        ...(takesAddr(va, va.interfaces[a.port]) ? [{ label: t("Endereços…"), onClick: () => editAddr(va, a.port) }] : []),
        "-",
        ...captureItems(host, lab.id, va, a.port, portName(va, a.port)),
      ],
      va.name + " · " + portName(va, a.port) + "  ⟶  " + t("rede {name}", { name: rede }),
    );
  };

  const plug = async (a: End, b: End) => {
    if (a.node === b.node && a.port === b.port) return;
    const nb = nodes.find((v) => v.name === b.node)!;
    const na = nodes.find((v) => v.name === a.node)!;
    const fb = nb.interfaces[b.port];
    const fa = na.interfaces[a.port];
    const pa = fa.link ? peerOf(nodes, fa.link, [a.node, a.port]) : null;
    if (pa && pa[0] === b.node && pa[1] === b.port) return; // already this cable
    const busyB = fb.link ? peerOf(nodes, fb.link, [b.node, b.port]) : null;
    if (busyB || fb.bridge || pa) {
      const what = [pa ? t("{a} sai de {b}", { a: na.name + " · " + portName(na, a.port), b: pa[0] + " · " + portName(nodes.find((v) => v.name === pa[0])!, pa[1]) }) : "", busyB ? t("{a} sai de {b}", { a: nb.name + " · " + portName(nb, b.port), b: busyB[0] + " · " + portName(nodes.find((v) => v.name === busyB[0])!, busyB[1]) }) : "", fb.bridge ? t("{a} sai da bridge {br}", { a: nb.name + " · " + portName(nb, b.port), br: fb.bridge }) : ""].filter(Boolean).join("; ");
      if (!(await confirm(t("Trocar os cabos?"), what, t("Trocar")))) return;
    }
    await saveCables(connect(nodes, [a.node, a.port], [b.node, b.port]));
  };

  const cableMenu = (e: React.MouseEvent, a: End, b: End) => {
    if (busy) return;
    const va = nodes.find((v) => v.name === a.node)!;
    const vb = nodes.find((v) => v.name === b.node)!;
    openMenu(
      e,
      [
        { label: t("Desligar o cabo"), danger: true, onClick: () => void saveCables(unplug(nodes, a.node, a.port)) },
        "-",
        ...captureItems(host, lab.id, va, a.port, portName(va, a.port)),
      ],
      va.name + " · " + portName(va, a.port) + "  ⟷  " + vb.name + " · " + portName(vb, b.port),
    );
  };

  // The notes: kept in the compose (x-notas), all of them at each change.
  const saveNotas = async (next: Nota[]) => {
    setNotas(next);
    try {
      await api(base + "/notas", { method: "PUT", body: { notas: next } });
    } catch (err) {
      gantry.toast(fileError((err as Error).message), "err");
    }
  };
  const corName: Record<string, string> = { "": t("sem fundo"), amarela: t("amarela"), azul: t("azul"), verde: t("verde"), rosa: t("rosa"), cinza: t("cinza") };
  const editNote = (n: Nota | null, where?: { x: number; y: number }) =>
    formDialog({
      title: n ? t("Editar a nota") : t("Nova nota"),
      intro: t("Texto simples, com **negrito**, *itálico*, # títulos, - listas, tabelas (| a | b |) e imagens da pasta do projeto: ![](notas/foto.png)."),
      submit: t("Salvar"),
      fields: [
        { id: "text", label: t("Texto"), type: "textarea", rows: 8, value: n?.text ?? "" },
        { id: "cor", label: t("Cor"), type: "select", value: n ? (n.cor ?? "") : "amarela", options: NOTE_CORES.map((c) => [c, corName[c]] as [string, string]) },
      ],
      onSubmit: async (v) => {
        const text = String(v.text ?? "");
        const cor = String(v.cor ?? "");
        if (n) await saveNotas(notas.map((x) => (x.id === n.id ? { ...x, text, cor } : x)));
        else await saveNotas([...notas, { id: newNoteID(notas), text, cor, pos: [Math.round(where?.x ?? 40), Math.round(where?.y ?? 40)], size: [NOTE_W, NOTE_H] }]);
      },
    });
  const noteMenu = (e: React.MouseEvent, n: Nota) =>
    openMenu(e, [{ label: t("Editar…"), onClick: () => editNote(n) }, "-", { label: t("Remover a nota"), danger: true, onClick: () => void saveNotas(notas.filter((x) => x.id !== n.id)) }], t("nota"));
  const bgMenu = (e: React.MouseEvent) => {
    if (e.target !== svg.current) return;
    const p = at(e);
    openMenu(e, [{ label: t("Nova nota aqui…"), onClick: () => editNote(null, p) }, { label: t("Nova rede aqui…"), onClick: () => redeDialog() }]);
  };

  const onMove = (e: React.PointerEvent) => {
    if (!drag) return;
    const p = at(e);
    if (drag.kind === "note") {
      setNotas((l) => l.map((n) => (n.id === drag.id ? { ...n, pos: [Math.max(0, Math.round(p.x - drag.dx)), Math.max(0, Math.round(p.y - drag.dy))] } : n)));
      if (!drag.moved) setDrag({ ...drag, moved: true });
    } else if (drag.kind === "note-size") {
      const w = Math.max(60, Math.round(drag.w0 + p.x - drag.x0));
      const h = Math.max(30, Math.round(drag.h0 + p.y - drag.y0));
      setNotas((l) => l.map((n) => (n.id === drag.id ? { ...n, size: [w, h] } : n)));
      if (!drag.moved) setDrag({ ...drag, moved: true });
    } else if (drag.kind === "node" || drag.kind === "cloud") {
      const key = drag.kind === "node" ? drag.node : CK + drag.rede;
      setPos((m) => ({ ...m, [key]: [Math.round(p.x - drag.dx), Math.round(p.y - drag.dy)] }));
      if (!drag.moved) setDrag({ ...drag, moved: true });
    } else {
      setDrag({ ...drag, x: p.x, y: p.y });
      const over = portAt(e);
      const cl = over ? "" : cloudAt(e);
      setHover(over ? over.node + "|" + over.port : cl ? CK + cl : "");
    }
  };
  const onUp = (e: React.PointerEvent) => {
    const d = drag;
    setDrag(null);
    setHover("");
    if (!d) return;
    if (d.kind === "node" && !d.moved) (setSel((cur) => (cur === d.node ? "" : d.node)), setSelRede(""));
    if (d.kind === "cloud" && !d.moved) (setSelRede((cur) => (cur === d.rede ? "" : d.rede)), setSel(""));
    if ((d.kind === "node" || d.kind === "cloud") && d.moved) {
      // The first move keeps the whole drawing (the nodes never placed
      // would go elsewhere once one has a position).
      const all: Record<string, [number, number]> = {};
      const clamp = (p: [number, number]): [number, number] => [Math.max(0, p[0]), Math.max(0, p[1])];
      for (const v of nodes) {
        const p = pos[v.name];
        if ((d.kind === "node" && v.name === d.node) || !v.pos || v.pos[0] !== p[0] || v.pos[1] !== p[1]) all[v.name] = clamp(p);
      }
      for (const r of redes) {
        const p = pos[CK + r.name];
        if ((d.kind === "cloud" && r.name === d.rede) || !r.pos || r.pos[0] !== p[0] || r.pos[1] !== p[1]) all[r.name] = clamp(p);
      }
      setPos((m) => {
        const out = { ...m };
        for (const [k, p] of Object.entries(all)) out[redes.some((r) => r.name === k) ? CK + k : k] = p;
        return out;
      });
      setNodes(nodes.map((v) => (all[v.name] ? { ...v, pos: all[v.name] } : v)));
      setNet((n) => ({ ...n, redes: n.redes.map((r) => (all[r.name] ? { ...r, pos: all[r.name] } : r)) }));
      api(base + "/posicoes", { method: "PUT", body: { pos: all } }).catch((err) => gantry.toast(fileError((err as Error).message), "err"));
    }
    if ((d.kind === "note" || d.kind === "note-size") && d.moved) void saveNotas(notas);
    if (d.kind === "port") {
      const to = portAt(e);
      const cl = to ? "" : cloudAt(e);
      if (to) void plug(d.from, to);
      else if (cl) void join(d.from, cl);
    }
  };

  // What a node can do: console, shell, logs, start and stop, its panel and
  // the capture of each port with a cable.
  const vmOf = (name: string) => lab.vms?.find((x) => x.name === name);
  const ctrOf = (name: string) => lab.containers.find((x) => x.service === name);
  const vmBase = "/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/vms/";
  const vmAction = async (name: string, op: string) => {
    try {
      const r = await api<{ ok: boolean; error?: string; output?: string }>(vmBase + enc(name) + "/" + op, { method: "POST" });
      if (!r.ok) {
        gantry.showOutput(name, r);
        gantry.toast(r.error || t("falhou"), "err");
      } else gantry.toast(name + ": " + (r.output || "ok").trim().split("\n").pop(), "ok");
    } catch (err) {
      gantry.toast((err as Error).message, "err");
    }
    await gantry.loadLabs(host);
  };
  const ctrAction = async (c: { id: string; runtime?: string; name: string }, op: string) => {
    try {
      await api("/hosts/" + enc(host) + "/containers/" + enc(c.id) + "/" + op + (c.runtime ? "?rt=" + enc(c.runtime) : ""), { method: "POST" });
      gantry.toast(c.name + ": " + op, "ok");
    } catch (err) {
      gantry.toast((err as Error).message, "err");
    }
    await gantry.loadLabs(host);
  };
  const term = (name: string, kind: "shell" | "logs" | "serial") => ({
    id: "vm-" + lab.id + "-" + name + (kind === "shell" ? "" : "-" + kind),
    name: "VM " + name + (kind === "shell" ? "" : kind === "logs" ? " · logs" : " · console"),
    service: (kind === "serial" ? "" : "VM ") + name + (kind === "shell" ? "" : kind === "logs" ? " · logs" : " · console"),
    termURL: vmBase + enc(name) + "/term" + (kind === "logs" ? "?logs=1" : kind === "serial" ? "?serial=1" : ""),
  });
  const nodeActions = (v: CVM): MenuItem[] => {
    const out: MenuItem[] = [];
    if (v.kind === "ctr") {
      const c = ctrOf(v.name);
      if (!c) return [{ label: t("Container ainda não criado (suba o lab)"), onClick: () => {}, disabled: true }];
      const running = c.state === "running";
      if (running) out.push({ label: t("Terminal"), onClick: () => gantry.openTerminal(host, c) });
      out.push({ label: "Logs", onClick: () => gantry.openLogs(host, c) });
      out.push("-");
      out.push(running ? { label: t("Parar"), onClick: () => void ctrAction(c, "stop") } : { label: t("Iniciar"), onClick: () => void ctrAction(c, "start") });
      if (running) out.push({ label: t("Reiniciar"), onClick: () => void ctrAction(c, "restart") });
      return out;
    }
    const vm = vmOf(v.name);
    const running = vm?.state === "running";
    if (running && vm?.serial) out.push({ label: t("Console"), hint: t("serial"), onClick: () => gantry.openTerminal(host, term(v.name, "serial")) });
    if (running && vm && !vm.noCloudInit && !vm.vnc) {
      out.push({ label: "Shell", onClick: () => gantry.openTerminal(host, term(v.name, "shell")) });
      out.push({ label: t("Logs do sistema"), onClick: () => gantry.openTerminal(host, term(v.name, "logs")) });
    }
    if (out.length) out.push("-");
    if (running) {
      out.push({ label: t("Parar"), onClick: () => void vmAction(v.name, "stop") });
      out.push({ label: t("Reiniciar"), onClick: () => void vmAction(v.name, "restart") });
    } else out.push({ label: !vm || vm.state === "missing" ? t("Criar e iniciar") : t("Iniciar"), onClick: () => void vmAction(v.name, "start") });
    return out;
  };
  const captures = (v: CVM): MenuItem[] =>
    v.interfaces.flatMap((f, i) => {
      if (!f.link && !f.rede) return [];
      const peer = f.link ? peerOf(nodes, f.link, [v.name, i]) : null;
      const pv = peer ? nodes.find((x) => x.name === peer[0]) : undefined;
      const to = peer && pv ? " ↔ " + pv.name + " " + portName(pv, peer[1]) : f.rede ? " ⟶ " + t("rede {name}", { name: f.rede }) : "";
      const [live] = captureItems(host, lab.id, v, i, portName(v, i)) as { label: string; onClick: () => void }[];
      return [{ label: t("Capturar {port}", { port: portName(v, i) }) + to, onClick: live.onClick }];
    });
  // openConsole: the double click on a node, as in GNS3 and EVE-NG: the
  // console of a router, switch or PC, the terminal of a container, the
  // shell of a VM without a serial console.
  const openConsole = (v: CVM) => {
    if (v.kind === "ctr") {
      const c = ctrOf(v.name);
      if (c?.state === "running") gantry.openTerminal(host, c);
      else gantry.toast(t("{name} está parado: inicie para abrir o terminal.", { name: v.name }), "err");
      return;
    }
    const vm = vmOf(v.name);
    if (vm?.state !== "running") {
      gantry.toast(t("{name} está parado: inicie para abrir o console.", { name: v.name }), "err");
      return;
    }
    if (vm.serial) gantry.openTerminal(host, term(v.name, "serial"));
    else if (!vm.noCloudInit && !vm.vnc) gantry.openTerminal(host, term(v.name, "shell"));
  };
  const nodeMenu = (e: React.MouseEvent, v: CVM) => {
    e.preventDefault();
    openMenu(e, [...nodeActions(v), "-", { label: t("Interfaces e conexões…"), onClick: () => setSel(v.name) }, { label: t("Ícone…"), onClick: () => pickIcon(v.name, v.icon ?? "", !!v.iconSet, (ic) => void setIcon(v, ic)) }, ...(captures(v).length ? ["-" as const, ...captures(v)] : [])], v.name);
  };

  // Cables, once each.
  const cables: { a: End; b: End; link: string }[] = [];
  for (const v of nodes)
    v.interfaces.forEach((f, i) => {
      if (!f.link) return;
      const p = peerOf(nodes, f.link, [v.name, i]);
      if (p && (p[0] > v.name || (p[0] === v.name && p[1] > i))) cables.push({ a: { node: v.name, port: i }, b: { node: p[0], port: p[1] }, link: f.link });
    });
  // spread: the drawing opened up, the same arrangement with more room,
  // until no two boxes touch (a lab from PNETLab or EVE-NG has small icons
  // close together); saved like a move.
  const spread = () => {
    const boxes = [...nodes.map((v) => ({ k: v.name, w: nodeW(v), h: NODE_H })), ...redes.map((r) => ({ k: CK + r.name, w: CLOUD_W, h: CLOUD_H }))].filter((b) => pos[b.k]);
    if (boxes.length < 2) return;
    const x0 = Math.min(...boxes.map((b) => pos[b.k][0]));
    const y0 = Math.min(...boxes.map((b) => pos[b.k][1]));
    const place = (b: (typeof boxes)[0], f: number): [number, number] => [Math.round(x0 + (pos[b.k][0] + b.w / 2 - x0) * f - b.w / 2), Math.round(y0 + (pos[b.k][1] + b.h / 2 - y0) * f - b.h / 2)];
    const clear = (f: number) =>
      boxes.every((a, i) =>
        boxes.every((b, j) => {
          if (j <= i) return true;
          const [ax, ay] = place(a, f);
          const [bx, by] = place(b, f);
          return ax + a.w + 50 <= bx || bx + b.w + 50 <= ax || ay + a.h + 60 <= by || by + b.h + 60 <= ay;
        }),
      );
    let f = 1;
    while (f < 5 && !clear(f)) f += 0.1;
    if (f === 1) {
      gantry.toast(t("O desenho já tem espaço entre os equipamentos."), "ok");
      return;
    }
    const all: Record<string, [number, number]> = {};
    for (const b of boxes) {
      const [x, y] = place(b, f);
      all[b.k.startsWith(CK) ? b.k.slice(CK.length) : b.k] = [Math.max(0, x), Math.max(0, y)];
    }
    setPos((m) => {
      const out = { ...m };
      for (const [k, p] of Object.entries(all)) out[redes.some((r) => r.name === k) ? CK + k : k] = p;
      return out;
    });
    setNodes(nodes.map((v) => (all[v.name] ? { ...v, pos: all[v.name] } : v)));
    setNet((n) => ({ ...n, redes: n.redes.map((r) => (all[r.name] ? { ...r, pos: all[r.name] } : r)) }));
    api(base + "/posicoes", { method: "PUT", body: { pos: all } })
      .then(() => {
        // the notes go with the drawing, in the same proportion
        if (notas.some((n) => n.pos)) void saveNotas(notas.map((n) => (n.pos ? { ...n, pos: [Math.round(x0 + (n.pos[0] - x0) * f), Math.round(y0 + (n.pos[1] - y0) * f)] as [number, number] } : n)));
      })
      .catch((err) => gantry.toast(fileError((err as Error).message), "err"));
  };
  // A cable: straight, as in Packet Tracer, or a curve that leaves each
  // port square to its side.
  const pathOf = (a: Port, b: Port) => {
    if (lines === "retas") return `M${a.x} ${a.y} L${b.x} ${b.y}`;
    const bend = Math.max(30, Math.min(120, Math.hypot(a.x - b.x, a.y - b.y) / 3));
    const [ax, ay] = NORMAL[a.side];
    const [bx, by] = NORMAL[b.side];
    return `M${a.x} ${a.y} C${a.x + ax * bend} ${a.y + ay * bend} ${b.x + bx * bend} ${b.y + by * bend} ${b.x} ${b.y}`;
  };
  // The ports on networks: a line from each to its cloud.
  const members: { a: End; rede: string }[] = [];
  for (const v of nodes) v.interfaces.forEach((f, i) => f.rede && pos[CK + f.rede] && members.push({ a: { node: v.name, port: i }, rede: f.rede }));
  // Where each line meets its cloud: spread along the side it comes from,
  // in the order of the ports, so the lines do not pile up.
  const cloudEnd = (rede: string, from: { x: number; y: number }) => {
    const [cx, cy] = pos[CK + rede];
    const above = from.y < cy + CLOUD_H / 2;
    const side = members
      .filter((m) => m.rede === rede)
      .map((m) => ports[m.a.node + "\u0000" + m.a.port])
      .filter((p) => p && p.y < cy + CLOUD_H / 2 === above)
      .map((p) => p.x)
      .sort((a, b) => a - b);
    const k = Math.max(0, side.indexOf(from.x));
    const inset = CLOUD_H / 2;
    const x = side.length > 1 ? cx + inset + ((CLOUD_W - 2 * inset) * k) / (side.length - 1) : cx + CLOUD_W / 2;
    return { x, y: above ? cy : cy + CLOUD_H, side: (above ? "t" : "b") as Side };
  };
  const xs = [...nodes.map((v) => (pos[v.name]?.[0] ?? 0) + nodeW(v)), ...redes.map((r) => (pos[CK + r.name]?.[0] ?? 0) + CLOUD_W), ...notas.map((n) => (n.pos?.[0] ?? 0) + (n.size?.[0] ?? NOTE_W))];
  const ys = [...nodes.map((v) => (pos[v.name]?.[1] ?? 0) + NODE_H), ...redes.map((r) => (pos[CK + r.name]?.[1] ?? 0) + CLOUD_H), ...notas.map((n) => (n.pos?.[1] ?? 0) + (n.size?.[1] ?? NOTE_H))];
  const W = Math.max(600, ...xs) + 80;
  const H = Math.max(260, ...ys) + 110;
  const fit = () => Math.min(1, ((wrap.current?.clientWidth ?? W) - 4) / W);
  const z = zoom ?? 1;
  const onWheel = (e: React.WheelEvent) => {
    if (!e.ctrlKey && !e.metaKey) return; // a pinch on the trackpad, or Ctrl/⌘ + wheel
    e.preventDefault();
    const w = wrap.current;
    if (w) {
      // Keep the point under the mouse where it is.
      const r = w.getBoundingClientRect();
      const cx = e.clientX - r.left;
      const cy = e.clientY - r.top;
      anchor.current = { px: (w.scrollLeft + cx) / z, py: (w.scrollTop + cy) / z, cx, cy };
    }
    setZoom(z * (e.deltaY < 0 ? 1.1 : 1 / 1.1));
  };

  return (
    <div className="block cablemap">
      <div className="block-head">
        <h2 className="block-title">{t("Rede do lab")}</h2>
        <span className="muted small">{t("dois cliques num equipamento abrem o console; botão direito num equipamento, numa rede ou num cabo para as ações, e no fundo para uma nota; arraste de uma porta a outra para ligar um cabo, ou até uma nuvem para pôr a porta naquela rede; arraste o fundo (ou segure Espaço) para mover o desenho, Ctrl ou ⌘ + roda para o zoom")}</span>
        <span className="grow" />
        {busy ? <span className="spin" /> : null}
        <button className="btn small" type="button" disabled={busy} onClick={spread} title={t("Afasta os equipamentos que estão encostados, mantendo a mesma disposição")}>
          {t("Espaçar")}
        </button>
        <span className="seg cm-zoom" role="group" aria-label={t("Forma dos cabos")}>
          <button className="seg-btn" type="button" aria-pressed={lines === "retas"} title={t("Cabos em linha reta, como no Packet Tracer")} onClick={() => setLines("retas")}>
            {t("Retas")}
          </button>
          <button className="seg-btn" type="button" aria-pressed={lines === "curvas"} title={t("Cabos em curva")} onClick={() => setLines("curvas")}>
            {t("Curvas")}
          </button>
        </span>
        <span className="seg cm-zoom" role="group" aria-label={t("Zoom do desenho")}>
          <button className="seg-btn" type="button" title={t("Afastar (Ctrl ou ⌘ + roda)")} onClick={() => setZoom(z / 1.25)}>
            −
          </button>
          <button className="seg-btn" type="button" title={t("Tamanho real")} onClick={() => setZoom(1)}>
            {Math.round(z * 100)}%
          </button>
          <button className="seg-btn" type="button" title={t("Aproximar (Ctrl ou ⌘ + roda)")} onClick={() => setZoom(z * 1.25)}>
            +
          </button>
          <button className="seg-btn" type="button" title={t("Caber no painel")} onClick={() => setZoom(fit())}>
            {t("Ajustar")}
          </button>
        </span>
        <button className="btn small" type="button" disabled={busy} onClick={() => redeDialog()} title={t("Uma rede do lab é um switch: várias portas entram nela, com subnet e DHCP opcionais")}>
          {t("+ Rede")}
        </button>
      </div>
      <div
        className={"panel cablemap-wrap" + (panning ? " panning" : space ? " pan-ready" : "")}
        ref={wrap}
        onWheel={onWheel}
        onPointerDownCapture={panStart}
        onPointerMoveCapture={panMove}
        onPointerUpCapture={panEnd}
        onPointerCancelCapture={panEnd}
        onPointerEnter={() => (over.current = true)}
        onPointerLeave={() => (over.current = false)}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && e.preventDefault()}
      >
        <svg ref={svg} width={W * z} height={H * z} viewBox={`0 0 ${W} ${H}`} className={"cablemap-svg" + (drag ? " dragging" : "") + (focus && !drag ? " focus" : "")} onPointerMove={onMove} onPointerUp={onUp} onContextMenu={bgMenu} onPointerLeave={() => (setDrag(null), setHover(""))} role="img" aria-label={t("Desenho dos cabos de {lab}", { lab: lab.name })}>
          {notas.map((n, i) => {
            const [x, y] = n.pos ?? [40 + i * 30, 40 + i * 30];
            const [w, h] = n.size ?? [NOTE_W, NOTE_H];
            return (
              <g key={"nota:" + n.id} className="cm-note" data-nota={n.id} onContextMenu={(e) => noteMenu(e, n)} onDoubleClick={() => editNote(n)}>
                <foreignObject x={x} y={y} width={w} height={h}>
                  <div
                    className={"cm-note-box" + (n.cor ? " cor-" + n.cor : "")}
                    title={t("Arraste para mover; dois cliques para editar")}
                    onPointerDown={(e) => {
                      if (e.button !== 0 || busy || space) return;
                      e.preventDefault();
                      (e.currentTarget as Element).setPointerCapture?.(e.pointerId);
                      const q = at(e);
                      setDrag({ kind: "note", id: n.id, dx: q.x - x, dy: q.y - y, moved: false });
                    }}
                    dangerouslySetInnerHTML={{ __html: noteHTML(n.text, host, lab.id) || '<div class="muted">' + t("(nota vazia)") + "</div>" }}
                  />
                </foreignObject>
                <rect
                  x={x + w - 12}
                  y={y + h - 12}
                  width={12}
                  height={12}
                  className="cm-note-grip"
                  onPointerDown={(e) => {
                    if (e.button !== 0 || busy) return;
                    e.preventDefault();
                    e.stopPropagation();
                    (e.target as Element).setPointerCapture?.(e.pointerId);
                    const q = at(e);
                    setDrag({ kind: "note-size", id: n.id, x0: q.x, y0: q.y, w0: w, h0: h, moved: false });
                  }}
                />
              </g>
            );
          })}
          {cables.map((c) => {
            const pa = ports[c.a.node + "\u0000" + c.a.port];
            const pb = ports[c.b.node + "\u0000" + c.b.port];
            if (!pa || !pb) return null;
            const la = light(c.a.node, c.a.port);
            const lb = light(c.b.node, c.b.port);
            const up = la && lb ? la.st === "up" && lb.st === "up" : states[c.a.node] === "on" && states[c.b.node] === "on";
            const d = pathOf(pa, pb);
            return (
              <g
                key={c.link}
                className={"cm-cable" + (up ? " up" : " down") + (focus === "link:" + c.link || focus === c.a.node || focus === c.b.node ? " hl" : "")}
                onContextMenu={(e) => cableMenu(e, c.a, c.b)}
                onPointerEnter={() => setFocus("link:" + c.link)}
                onPointerLeave={() => setFocus("")}
                data-link={c.link}
              >
                <title>{c.link + (up ? "" : " · " + (la && lb ? t("um dos lados está desligado") : t("um dos lados está parado")))}</title>
                <path d={d} className="cm-hit" />
                <path d={d} className="cm-line" />
              </g>
            );
          })}
          {members.map((m) => {
            const pa = ports[m.a.node + "\u0000" + m.a.port];
            if (!pa) return null;
            const la = light(m.a.node, m.a.port);
            const up = la ? la.st === "up" : states[m.a.node] === "on";
            const d = pathOf(pa, cloudEnd(m.rede, pa));
            return (
              <g
                key={m.a.node + "|" + m.a.port}
                className={"cm-cable cm-member" + (up ? " up" : " down") + (focus === "m:" + m.a.node + "|" + m.a.port || focus === m.a.node || focus === CK + m.rede ? " hl" : "")}
                onContextMenu={(e) => memberMenu(e, m.a, m.rede)}
                onPointerEnter={() => setFocus("m:" + m.a.node + "|" + m.a.port)}
                onPointerLeave={() => setFocus("")}
                data-member={m.rede}
              >
                <title>{m.a.node + " · " + portName(nodes.find((v) => v.name === m.a.node)!, m.a.port) + " ⟶ " + t("rede {name}", { name: m.rede })}</title>
                <path d={d} className="cm-hit" />
                <path d={d} className="cm-line" />
              </g>
            );
          })}
          {redes.map((r) => {
            const p = pos[CK + r.name];
            if (!p) return null;
            const [x, y] = p;
            return (
              <g key={"rede:" + r.name} className={"cm-cloud" + (selRede === r.name ? " sel" : "") + (hover === CK + r.name ? " over" : "")} data-rede={r.name} onContextMenu={(e) => redeMenu(e, r)} onPointerEnter={() => setFocus(CK + r.name)} onPointerLeave={() => setFocus("")}>
                <rect
                  x={x}
                  y={y}
                  width={CLOUD_W}
                  height={CLOUD_H}
                  rx={CLOUD_H / 2}
                  className="cm-cloud-box"
                  onPointerDown={(e) => {
                    if (e.button !== 0 || busy) return;
                    e.preventDefault();
                    (e.target as Element).setPointerCapture?.(e.pointerId);
                    const q = at(e);
                    setDrag({ kind: "cloud", rede: r.name, dx: q.x - x, dy: q.y - y, moved: false });
                  }}
                />
                <text x={x + CLOUD_W / 2} y={y + 25} className="cm-name" textAnchor="middle">
                  {r.name}
                </text>
                <text x={x + CLOUD_W / 2} y={y + 41} className="cm-kind" textAnchor="middle">
                  {(r.subnet || t("switch")) + (r.dhcp ? " · DHCP" : "")}
                </text>
                <title>{t("rede {name}", { name: r.name }) + (r.subnet ? " · " + r.subnet : "") + (r.gateway ? " · gw " + r.gateway : "") + (r.dhcp ? " · DHCP " + r.dhcpFrom + "–" + r.dhcpTo : "")}</title>
              </g>
            );
          })}
          {drag?.kind === "port" ? (() => {
            const p = ports[drag.from.node + "\u0000" + drag.from.port];
            return p ? <line x1={p.x} y1={p.y} x2={drag.x} y2={drag.y} className="cm-rubber" /> : null;
          })() : null}
          {nodes.map((v) => {
            const [x, y] = pos[v.name] ?? [0, 0];
            const w = nodeW(v);
            const st = states[v.name] ?? "";
            return (
              <g key={v.name} className={"cm-node" + (st ? " " + st : "") + (sel === v.name ? " sel" : "")} data-node={v.name} onDoubleClick={() => openConsole(v)} onContextMenu={(e) => nodeMenu(e, v)} onPointerEnter={() => setFocus(v.name)} onPointerLeave={() => setFocus("")}>
                <rect
                  x={x}
                  y={y}
                  width={w}
                  height={NODE_H}
                  rx={8}
                  className="cm-box"
                  onPointerDown={(e) => {
                    if (e.button !== 0 || busy) return;
                    e.preventDefault();
                    (e.target as Element).setPointerCapture?.(e.pointerId);
                    const p = at(e);
                    setDrag({ kind: "node", node: v.name, dx: p.x - x, dy: p.y - y, moved: false });
                  }}
                />
                <NodeIcon id={v.icon || "server"} x={x + 10} y={y + 19} size={34} />
                <circle cx={x + w - 12} cy={y + 12} r={4} className={"cm-dot " + st} />
                <text x={x + 52} y={y + 34} className="cm-name">
                  {v.name}
                </text>
                <text x={x + 52} y={y + 50} className="cm-kind">
                  {v.kind === "dyn" ? "c" + (v.platform ?? "") : (KIND_LABEL[v.kind ?? ""] ?? (v.nic || "VM"))}
                </text>
                {v.interfaces.map((f, i) => {
                  const p = ports[v.name + "\u0000" + i];
                  const key = v.name + "|" + i;
                  return (
                    <g key={i}>
                      <text x={p.side === "l" ? p.x - 8 : p.side === "r" ? p.x + 8 : p.x} y={p.side === "t" ? p.y + 17 : p.side === "b" ? p.y - 9 : p.y - 9} className="cm-port-label" textAnchor={p.side === "l" ? "end" : p.side === "r" ? "start" : "middle"}>
                        {shortPort(v, i)}
                      </text>
                      <circle
                        cx={p.x}
                        cy={p.y}
                        r={hover === key ? 8 : 6}
                        data-port={key}
                        className={"cm-port" + (f.link || f.rede ? " linked" : f.bridge ? " bridge" : "") + (hover === key ? " over" : "")}
                        onPointerDown={(e) => {
                          if (e.button !== 0 || busy || f.bridge) return;
                          e.preventDefault();
                          e.stopPropagation();
                          svg.current?.setPointerCapture?.(e.pointerId);
                          const q = at(e);
                          setDrag({ kind: "port", from: { node: v.name, port: i }, x: q.x, y: q.y });
                        }}
                      >
                        <title>{v.name + " · " + portName(v, i) + (f.bridge ? " · bridge " + f.bridge : f.rede ? " · " + t("rede {name}", { name: f.rede }) : f.link ? "" : " · " + t("livre")) + (f.ips?.length ? " · " + f.ips.join(", ") : f.dhcp ? " · DHCP" + (leaseOf(v.name, i) ? " " + leaseOf(v.name, i)!.ip : "") : "")}</title>
                      </circle>
                    </g>
                  );
                })}
              </g>
            );
          })}
          {nodes.flatMap((v) =>
            v.interfaces.map((f, i) => {
              if (!f.link && !f.rede) return null;
              const p = ports[v.name + "\u0000" + i];
              const l = p && light(v.name, i);
              if (!p || !l) return null;
              return (
                <circle key={"luz:" + v.name + "|" + i} cx={p.x + NORMAL[p.side][0] * 16} cy={p.y + NORMAL[p.side][1] * 16} r={5} className={"cm-light " + l.st}>
                  <title>{v.name + " · " + portName(v, i) + ": " + l.why}</title>
                </circle>
              );
            }),
          )}
        </svg>
      </div>
      {sel && nodes.some((v) => v.name === sel) ? <NodePanel v={nodes.find((x) => x.name === sel)!} nodes={nodes} leases={net.leases} state={states[sel] ?? ""} actions={nodeActions(nodes.find((x) => x.name === sel)!)} host={host} lab={lab} onAddr={editAddr} onClose={() => setSel("")} /> : null}
      {selRede && redes.some((r) => r.name === selRede) ? <RedePanel r={redes.find((x) => x.name === selRede)!} nodes={nodes} leases={net.leases} host={host} lab={lab} onEdit={redeDialog} onRemove={(r) => void removeRede(r)} onAddr={editAddr} onClose={() => setSelRede("")} /> : null}
    </div>
  );
}

// NodePanel: a node with its ports, what each one is plugged into, its
// address (containers) and the capture of each cable.
function NodePanel({ v, nodes, leases, state, actions, host, lab, onAddr, onClose }: { v: CVM; nodes: CVM[]; leases: Lease[]; state: State; actions: MenuItem[]; host: string; lab: Lab; onAddr: (v: CVM, i: number) => void; onClose: () => void }) {
  const kind = v.kind === "ctr" ? "container" : v.kind === "iol" ? "IOL" : v.kind === "dyn" ? "Dynamips c" + (v.platform ?? "") : v.kind === "vpcs" ? "VPCS" : t("VM");
  const ctr = v.kind === "ctr" ? lab.containers.find((x) => x.service === v.name) : undefined;
  return (
    <div className="panel panel-pad cm-panel">
      <div className="cm-panel-head">
        <span className={"dot " + state} />
        <svg width="22" height="22" viewBox="0 0 32 32" className="cm-icon-svg">
          <NodeIcon id={v.icon || "server"} x={0} y={0} size={32} />
        </svg>
        <b>{v.name}</b>
        <span className="muted small">{kind + (ctr ? " · " + ctr.image : "")}</span>
        <span className="grow" />
        {actions
          .filter((a): a is Exclude<MenuItem, "-" | null | false | undefined> => !!a && a !== "-")
          .map((a) => (
            <button key={a.label} className="btn small" type="button" disabled={a.disabled} onClick={() => void a.onClick()}>
              {a.label}
            </button>
          ))}
        <button className="icon-btn" type="button" aria-label={t("Fechar")} onClick={onClose}>
          ✕
        </button>
      </div>
      {v.interfaces.length ? (
        <table className="grid compact cm-ports">
          <thead>
            <tr>
              <th>{t("Porta")}</th>
              <th>{t("Ligada em")}</th>
              <th>{t("Endereços")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {v.interfaces.map((f, i) => {
              const peer = f.link ? peerOf(nodes, f.link, [v.name, i]) : null;
              const pv = peer ? nodes.find((x) => x.name === peer[0]) : undefined;
              const caps = f.link || f.rede ? (captureItems(host, lab.id, v, i, portName(v, i)) as { label: string; onClick: () => void }[]) : [];
              return (
                <tr key={i}>
                  <td className="mono">{portName(v, i)}</td>
                  <td>{peer && pv ? pv.name + " · " + portName(pv, peer[1]) : f.rede ? t("rede {name}", { name: f.rede }) : f.bridge ? t("bridge {name}", { name: f.bridge }) : <span className="muted">{t("— sem cabo —")}</span>}</td>
                  <td>
                    <Addr v={v} i={i} leases={leases} />
                  </td>
                  <td className="row-actions">
                    {takesAddr(v, f) ? (
                      <button className="btn small" type="button" onClick={() => onAddr(v, i)}>
                        {t("Endereços…")}
                      </button>
                    ) : null}
                    {caps.map((c) => (
                      <button key={c.label} className="btn small" type="button" onClick={() => void c.onClick()}>
                        {c.label}
                      </button>
                    ))}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <p className="muted small">{t("Sem portas de rede no lab. Aumente o número de portas em Cabos.")}</p>
      )}
    </div>
  );
}

// Addr: the addresses of a port that Gantry knows for real: the fixed ones
// it gives and the one its DHCP gave. What is configured inside a device
// (a router, a VPCS, a VM) is not guessed from files: empty.
function Addr({ v, i, leases }: { v: CVM; i: number; leases: Lease[] }) {
  const f = v.interfaces[i];
  if (f.ips?.length) return <span className="mono small">{f.ips.join(", ")}</span>;
  const l = f.rede ? leases.find((x) => x.node === v.name && x.port === String(i) && x.rede === f.rede) : undefined;
  if (l) return <span className="mono small" title={l.mac}>{l.ip + " (DHCP)"}</span>;
  if (f.dhcp) return <span className="muted small">{t("DHCP · ainda sem endereço")}</span>;
  return null;
}

// RedePanel: a lab network: its subnet and DHCP, every port on it with its
// addresses, and the capture of the whole network.
function RedePanel({ r, nodes, leases, host, lab, onEdit, onRemove, onAddr, onClose }: { r: Rede; nodes: CVM[]; leases: Lease[]; host: string; lab: Lab; onEdit: (r: Rede) => void; onRemove: (r: Rede) => void; onAddr: (v: CVM, i: number) => void; onClose: () => void }) {
  const caps = redeCaptureItems(host, lab.id, r.name) as { label: string; onClick: () => void }[];
  const members = (r.ports ?? []).map((p) => ({ p, v: nodes.find((x) => x.name === p.node) })).filter((m): m is { p: (typeof m)["p"]; v: CVM } => !!m.v && !!m.v.interfaces[m.p.port]);
  return (
    <div className="panel panel-pad cm-panel">
      <div className="cm-panel-head">
        <b>{t("rede {name}", { name: r.name })}</b>
        <span className="muted small">{[r.subnet || t("só o switch, sem subnet"), r.gateway ? "gateway " + r.gateway : "", r.dhcp ? t("DHCP {a}–{b} (servidor {s})", { a: r.dhcpFrom ?? "", b: r.dhcpTo ?? "", s: r.server ?? "" }) : "", r.dns?.length ? "DNS " + r.dns.join(", ") : ""].filter(Boolean).join(" · ")}</span>
        <span className="grow" />
        {caps.map((c) => (
          <button key={c.label} className="btn small" type="button" onClick={() => void c.onClick()} title={t("Captura todo o tráfego da rede (todas as portas nela)")}>
            {c.label}
          </button>
        ))}
        <button className="btn small" type="button" onClick={() => onEdit(r)}>
          {t("Editar")}
        </button>
        <button className="btn small danger" type="button" onClick={() => onRemove(r)}>
          {t("Remover")}
        </button>
        <button className="icon-btn" type="button" aria-label={t("Fechar")} onClick={onClose}>
          ✕
        </button>
      </div>
      {members.length ? (
        <table className="grid compact cm-ports">
          <thead>
            <tr>
              <th>{t("Equipamento")}</th>
              <th>{t("Porta")}</th>
              <th>{t("Endereços")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {members.map(({ p, v }) => (
              <tr key={p.node + "|" + p.port}>
                <td>{v.name}</td>
                <td className="mono">{portName(v, p.port)}</td>
                <td>
                  <Addr v={v} i={p.port} leases={leases} />
                </td>
                <td className="row-actions">
                  {takesAddr(v, v.interfaces[p.port]) ? (
                    <button className="btn small" type="button" onClick={() => onAddr(v, p.port)}>
                      {t("Endereços…")}
                    </button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="muted small">{t("Nenhuma porta nesta rede ainda. Arraste uma porta de um equipamento até a nuvem.")}</p>
      )}
    </div>
  );
}
