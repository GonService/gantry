// Topology of a project: the published ports of the host, the services of
// each engine with their networks, the VMs, and links between engines.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { enc, type Host } from "@/lib/app";
import { t } from "@/lib/i18n";
import { ErrorBox, Loading } from "@/components/ui";
import { VM_STATE } from "@/views/shared/vm";
import type { Lab } from "./types";
import { CableMap } from "./cablemap";

/* eslint-disable @typescript-eslint/no-explicit-any */
type Port = { host: string; hostIp?: string; container: string; proto: string };
type Svc = { name: string; runtime: string; image?: string; build?: boolean; networkMode?: string; ports: Port[]; networks: string[]; dependsOn: string[]; profiles?: string[]; containers: { state: string; ips?: Record<string, string> }[] };
type Topo = {
  warnings: string[];
  gantry: boolean;
  default: string;
  networks: { key: string; external?: boolean }[];
  services: Svc[];
  runtimes: { runtime: string; available: boolean; socket?: string; host?: string }[];
  vms?: { name: string; state: string; image: string; cpus: number; memory: number; disk: string; accel?: string; ports: Port[]; sshPort: number; packages?: string[] }[];
  links: { from: string; fromRuntime: string; to: string; toRuntime: string; via: Port[] }[];
};

const RT_LABEL: Record<string, string> = { docker: "Docker", podman: "Podman" };

export function TopologyTab({ hst, lab }: { hst: Host; lab: Lab }) {
  const [topo, setTopo] = useState<Topo | null>(null);
  const [err, setErr] = useState("");
  const sig = useRef("");
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        const r = await api<Topo>("/hosts/" + enc(hst.name) + "/labs/" + enc(lab.id) + "/topology");
        if (!live) return;
        const s = JSON.stringify(r);
        if (s !== sig.current) {
          sig.current = s;
          setTopo(r);
        }
      } catch (e) {
        if (live) setErr((e as Error).message);
      }
    };
    load();
    const timer = setInterval(() => {
      if (!document.hidden) load();
    }, 10000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [hst.name, lab.id]);
  if (err && !topo) return <ErrorBox>{err}</ErrorBox>;
  if (!topo) return <Loading>{t("Lendo o compose…")}</Loading>;
  const states: Record<string, "on" | "warn" | ""> = {};
  for (const v of topo.vms ?? []) states[v.name] = (VM_STATE[v.state]?.[0] ?? "") as "on" | "warn" | "";
  for (const s of topo.services) {
    const run = s.containers.filter((c) => c.state === "running").length;
    states[s.name] = run && run === s.containers.length ? "on" : run ? "warn" : "";
  }
  return (
    <>
      {hst.files ? <CableMap key={hst.name + "/" + lab.id} host={hst.name} lab={lab} states={states} /> : null}
      <TopoView hst={hst} t={topo} />
    </>
  );
}

function TopoView({ hst, t: topo }: { hst: Host; t: Topo }) {
  const root = useRef<HTMLDivElement>(null);
  const [hl, setHl] = useState("");
  const netSlot: Record<string, number> = {};
  topo.networks.forEach((n, i) => {
    netSlot[n.key] = (i % 8) + 1;
  });
  const published: [{ name: string; runtime: string }, Port][] = [];
  for (const s of topo.services) for (const p of s.ports) if (p.host) published.push([s, p]);
  for (const v of topo.vms || []) for (const p of v.ports) published.push([{ name: v.name, runtime: "vm" }, p]);
  published.sort((a, b) => Number(a[1].host) - Number(b[1].host) || a[1].host.localeCompare(b[1].host));

  useLayoutEffect(() => {
    const el = root.current;
    if (!el) return;
    const draw = () => drawTopoLines(el, netSlot, topo.links);
    draw();
    const ro = new ResizeObserver(draw);
    ro.observe(el);
    return () => ro.disconnect();
  }, [topo]); // eslint-disable-line react-hooks/exhaustive-deps

  const svcAttr = (k: string) => ({ "data-svc": k, className: hl === k ? "hl" : undefined });
  return (
    <div ref={root}>
      <div
        className="topo"
        onMouseOver={(e) => {
          const el = (e.target as HTMLElement).closest("[data-svc]") as HTMLElement | null;
          setHl(el ? el.dataset.svc! : "");
        }}
      >
        {topo.warnings.length ? (
          <div className="banner topo-warn">
            <div>
              {topo.warnings.map((w, i) => (
                <div key={i}>{w}</div>
              ))}
            </div>
          </div>
        ) : null}
        {!topo.gantry && !(topo.vms && topo.vms.length) ? (
          <p className="muted small" style={{ margin: "0 0 12px" }}>
            {t("Projeto de um engine só. Para dividir serviços entre Docker e Podman, use")} <code>{"x-gantry: { runtime: podman }"}</code> {t("no topo do compose ou dentro de um serviço.")}
          </p>
        ) : null}
        <div className="topo-host panel">
          <div className="topo-host-title">
            <b>Host</b>
            <span className="muted small">{hst.name + (hst.info && hst.info.Name ? " · " + hst.info.Name : "")}</span>
          </div>
          <div className="topo-ports">
            {published.length ? (
              published.map(([s, p], i) => {
                const a = svcAttr(s.runtime + "/" + s.name);
                return (
                  <span key={i} {...a} className={"port-chip" + (a.className ? " hl" : "")} title={(p.hostIp || "0.0.0.0") + ":" + p.host + " → " + s.name + ":" + p.container + "/" + p.proto}>
                    <b>{(p.hostIp && p.hostIp !== "0.0.0.0" ? p.hostIp + ":" : ":") + p.host}</b> → {s.name}
                    <span className="muted">{" :" + p.container + (p.proto !== "tcp" ? "/" + p.proto : "")}</span>
                  </span>
                );
              })
            ) : (
              <span className="muted small">{t("Nenhuma porta publicada no host.")}</span>
            )}
          </div>
        </div>
        <div className="topo-cols">
          {topo.runtimes.map((rt) => {
            const svcs = topo.services.filter((s) => s.runtime === rt.runtime);
            const nets = topo.networks.filter((n) => svcs.some((s) => s.networks.includes(n.key)));
            return (
              <section key={rt.runtime} className={"topo-col panel" + (rt.available ? "" : " off")} data-rt={rt.runtime}>
                <header className="topo-col-head">
                  <b>{RT_LABEL[rt.runtime] || rt.runtime}</b>
                  {rt.runtime === topo.default ? <span className="badge">{t("engine do host")}</span> : null}
                  {rt.available ? (
                    <span className="muted small mono" title={rt.socket}>
                      {rt.host && rt.host !== hst.name ? t("via {host}", { host: rt.host }) : ""}
                    </span>
                  ) : (
                    <span className="badge err">{t("não encontrado")}</span>
                  )}
                </header>
                {nets.length ? (
                  <div className="legend topo-nets">
                    {nets.map((n) => (
                      <span className="legend-item" key={n.key}>
                        <i className="swatch" style={{ background: "var(--s" + netSlot[n.key] + ")" }} />
                        {n.key + (n.external ? t(" (externa)") : "")}
                      </span>
                    ))}
                  </div>
                ) : null}
                <div className="topo-body" style={{ paddingLeft: nets.length * 16 + 18 }} data-nets={JSON.stringify(nets.map((n) => n.key))}>
                  {svcs.map((s) => {
                    const running = s.containers.filter((c) => c.state === "running").length;
                    const state = !s.containers.length
                      ? ["", t("não criado")]
                      : running === s.containers.length
                        ? ["on", t("rodando")]
                        : running
                          ? ["warn", t("{running} de {total}", { running, total: s.containers.length })]
                          : ["", t("parado")];
                    const sameDeps = s.dependsOn.filter((d) => topo.services.some((x) => x.name === d && x.runtime === s.runtime));
                    const ips: string[] = [];
                    for (const c of s.containers) for (const [n, ip] of Object.entries(c.ips || {})) ips.push(n + " " + ip);
                    const a = svcAttr(s.runtime + "/" + s.name);
                    return (
                      <div key={s.name} {...a} className={"topo-card panel" + (a.className ? " hl" : "")} data-nets={JSON.stringify(s.networks)}>
                        <div className="topo-card-top">
                          <b>{s.name}</b>
                          <span className="state small">
                            <span className={"dot " + state[0]} />
                            {state[1]}
                          </span>
                        </div>
                        <div className="mono small muted topo-img">{s.image || (s.build ? t("build local") : "—")}</div>
                        {s.networkMode ? (
                          <div className="small">
                            {t("rede:")} <code>{s.networkMode}</code>
                          </div>
                        ) : null}
                        {s.ports.length ? (
                          <div className="topo-card-ports">
                            {s.ports.map((p, i) => (
                              <span key={i} className={"mini-chip" + (p.host ? " pub" : "")}>
                                {p.host ? ":" + p.host + "→" + p.container : t("{port} interna", { port: p.container })}
                              </span>
                            ))}
                          </div>
                        ) : null}
                        {ips.length ? <div className="mono small muted">{ips.join(" · ")}</div> : null}
                        {sameDeps.length ? <div className="small muted">{t("depende de {list}", { list: sameDeps.join(", ") })}</div> : null}
                        {s.profiles && s.profiles.length ? <div className="small muted">{t("perfil: {list}", { list: s.profiles.join(", ") })}</div> : null}
                      </div>
                    );
                  })}
                </div>
              </section>
            );
          })}
          {topo.vms && topo.vms.length ? (
            <section className="topo-col panel" data-rt="vm">
              <header className="topo-col-head">
                <b>VMs</b>
                <span className="badge">QEMU</span>
              </header>
              <p className="muted small" style={{ margin: "0 0 10px" }}>
                {t("Cada VM tem a própria rede (NAT do QEMU). Ela alcança o host em 10.0.2.2, e os containers alcançam a VM pelas portas publicadas.")}
              </p>
              <div className="topo-body" style={{ paddingLeft: 0 }} data-nets="[]">
                {topo.vms.map((v) => {
                  const st = VM_STATE[v.state] || ["", v.state];
                  const a = svcAttr("vm/" + v.name);
                  return (
                    <div key={v.name} {...a} className={"topo-card panel" + (a.className ? " hl" : "")} data-nets="[]">
                      <div className="topo-card-top">
                        <b>{v.name}</b>
                        <span className="state small">
                          <span className={"dot " + st[0]} />
                          {st[1]}
                        </span>
                      </div>
                      <div className="mono small muted topo-img">{v.image}</div>
                      <div className="small muted">{t("{cpus} CPU · {mem} · disco {disk}", { cpus: v.cpus, mem: v.memory >= 1024 ? v.memory / 1024 + " GB" : v.memory + " MB", disk: v.disk }) + (v.accel ? " · " + v.accel : "")}</div>
                      <div className="topo-card-ports">
                        {v.ports.map((p, i) => (
                          <span key={i} className="mini-chip pub">
                            {":" + p.host + "→" + p.container + (p.proto !== "tcp" ? "/" + p.proto : "")}
                          </span>
                        ))}
                        <span className="mini-chip">ssh :{v.sshPort}</span>
                      </div>
                      {v.packages && v.packages.length ? <div className="small muted">{t("pacotes: {list}", { list: v.packages.join(", ") })}</div> : null}
                    </div>
                  );
                })}
              </div>
            </section>
          ) : null}
        </div>
        {topo.links.length ? (
          <div className="panel panel-pad topo-links">
            <h2 className="chart-title">{t("Dependências entre engines")}</h2>
            <p className="muted small" style={{ margin: "2px 0 8px" }}>
              {t("Containers de engines diferentes não compartilham redes; um alcança o outro pela porta publicada no host (em Linux, pelo IP do host; no Mac, por host.docker.internal ou host.containers.internal).")}
            </p>
            {topo.links.map((l, i) => (
              <div className="topo-link-row" key={i}>
                <span>
                  <b>{l.from}</b>
                  <span className={"rt-chip rt-" + l.fromRuntime}>{l.fromRuntime}</span>
                </span>
                <span className="muted"> → </span>
                <span>
                  <b>{l.to}</b>
                  <span className={"rt-chip rt-" + l.toRuntime}>{l.toRuntime}</span>
                </span>
                {l.via.length ? <span className="small">{t(" via porta {ports}", { ports: l.via.map((p) => ":" + p.host).join(", ") })}</span> : <span className="small topo-bad">{t(" sem porta publicada")}</span>}
              </div>
            ))}
          </div>
        ) : null}
        <svg className="topo-svg" aria-hidden="true" />
      </div>
    </div>
  );
}

const SVGNS = "http://www.w3.org/2000/svg";
function svgEl(tag: string, attrs: Record<string, string | number> = {}) {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
}

// Network buses on the left of each column and arrows between engines,
// drawn over the laid-out cards.
function drawTopoLines(root: HTMLElement, slots: Record<string, number>, links: Topo["links"]) {
  const topo = root.querySelector(".topo") as HTMLElement | null;
  const svg = root.querySelector(".topo-svg") as SVGSVGElement | null;
  if (!topo || !svg) return;
  // Stacked columns (narrow window) need room on the right for the arrows.
  const colsEl = topo.querySelector(".topo-cols") as HTMLElement | null;
  const colEls = colsEl ? (colsEl.querySelectorAll(".topo-col") as NodeListOf<HTMLElement>) : ([] as unknown as NodeListOf<HTMLElement>);
  if (colsEl) colsEl.classList.toggle("stacked", colEls.length > 1 && colEls[1].offsetTop > colEls[0].offsetTop);
  const base = topo.getBoundingClientRect();
  svg.setAttribute("width", String(base.width));
  svg.setAttribute("height", String(base.height));
  svg.replaceChildren();
  const rel = (r: DOMRect) => ({ x: r.left - base.left, y: r.top - base.top, w: r.width, h: r.height });

  for (const body of topo.querySelectorAll(".topo-body") as NodeListOf<HTMLElement>) {
    const nets: string[] = JSON.parse(body.dataset.nets || "[]");
    const b = rel(body.getBoundingClientRect());
    const cards = [...(body.querySelectorAll(".topo-card") as NodeListOf<HTMLElement>)].map((c) => ({ r: rel(c.getBoundingClientRect()), nets: JSON.parse(c.dataset.nets || "[]") as string[] }));
    nets.forEach((n, k) => {
      const x = b.x + 9 + k * 16;
      const on = cards.filter((c) => c.nets.includes(n));
      if (!on.length) return;
      const ys = on.map((c) => c.r.y + 22);
      const color = "var(--s" + slots[n] + ")";
      svg.append(svgEl("line", { x1: x, x2: x, y1: Math.min(...ys) - 8, y2: Math.max(...ys) + 8, class: "bus", style: "stroke:" + color }));
      for (const c of on) {
        const y = c.r.y + 22;
        svg.append(svgEl("line", { x1: x, x2: c.r.x, y1: y, y2: y, class: "stub", style: "stroke:" + color }));
        svg.append(svgEl("circle", { cx: x, cy: y, r: 4, class: "tap", style: "fill:" + color }));
      }
    });
  }

  const outCount: Record<string, number> = {};
  for (const l of links) {
    const a = topo.querySelector('.topo-card[data-svc="' + l.fromRuntime + "/" + l.from + '"]');
    const z = topo.querySelector('.topo-card[data-svc="' + l.toRuntime + "/" + l.to + '"]');
    if (!a || !z) continue;
    const ra = rel(a.getBoundingClientRect()),
      rz = rel(z.getBoundingClientRect());
    // Spread several arrows leaving the same card.
    const n = (outCount[l.from] = (outCount[l.from] || 0) + 1);
    const off = (n - 1) * 12 - 6;
    let d: string,
      lx: number,
      ly: number,
      anchor = "middle";
    if (Math.abs(ra.x - rz.x) < 40) {
      // Columns stacked (narrow window): loop around the right side.
      const x1 = ra.x + ra.w,
        y1 = ra.y + ra.h / 2 + off,
        x2 = rz.x + rz.w + 2,
        y2 = rz.y + rz.h / 2;
      const bulge = x1 + 18 + n * 10;
      d = `M${x1} ${y1} C${bulge} ${y1} ${bulge} ${y2} ${x2} ${y2}`;
      lx = bulge - 8;
      ly = (y1 + y2) / 2;
      anchor = "end";
    } else {
      const leftToRight = ra.x < rz.x;
      const x1 = leftToRight ? ra.x + ra.w : ra.x,
        y1 = ra.y + ra.h / 2 + off;
      const x2 = leftToRight ? rz.x - 2 : rz.x + rz.w + 2,
        y2 = rz.y + rz.h / 2;
      const dx = Math.max(40, Math.abs(x2 - x1) / 2) * (leftToRight ? 1 : -1);
      d = `M${x1} ${y1} C${x1 + dx} ${y1} ${x2 - dx} ${y2} ${x2} ${y2}`;
      lx = (x1 + x2) / 2;
      ly = (y1 + y2) / 2 - 6;
    }
    const ok = l.via.length > 0;
    svg.append(svgEl("path", { d, class: "xlink" + (ok ? "" : " bad"), "marker-end": "url(#topo-arrow" + (ok ? "" : "-bad") + ")" }));
    const label = svgEl("text", { x: lx, y: ly, "text-anchor": anchor, class: "xlabel" + (ok ? "" : " bad") });
    label.textContent = ok ? "via :" + l.via[0].host : t("sem porta");

    svg.append(label);
  }
  const defs = svgEl("defs");
  for (const [id, cls] of [
    ["topo-arrow", ""],
    ["topo-arrow-bad", " bad"],
  ]) {
    const m = svgEl("marker", { id, viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, orient: "auto-start-reverse" });
    m.append(svgEl("path", { d: "M0 0 L10 5 L0 10 z", class: "arrowhead" + cls }));
    defs.append(m);
  }
  svg.prepend(defs);
}
