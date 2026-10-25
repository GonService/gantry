// The cables of a network lab: the ports (extra NICs) of each VM and what
// each one plugs into: a port of another VM, a bridge of the machine, or
// nothing. Saving rewrites the interfaces: of the VMs in the compose and
// changes the cables live (the relay of the machine), without restarting
// the VMs; only a port moved to or from a bridge needs a restart.
import { useEffect, useState } from "react";
import { api, enc } from "@/lib/api";
import { type MenuItem } from "@/components/contextmenu";
import { gantry } from "@/lib/gantry";
import { t } from "@/lib/i18n";
import { formDialog } from "@/components/dialog";
import { loadDrift } from "./drift";

export type Iface = { link?: string; bridge?: string; rede?: string; ips?: string[]; dhcp?: boolean };

// A lab network (x-redes) as the API gives it: the switch, its subnet and
// DHCP, the ports on it; Lease: an address its DHCP handed out.
export type Rede = { name: string; subnet?: string; gateway?: string; dhcp?: boolean; dhcpFrom?: string; dhcpTo?: string; dns?: string[]; pos?: [number, number]; server?: string; ports?: { node: string; port: number; ips?: string[]; dhcp?: boolean }[] };
export type Lease = { rede: string; mac: string; ip: string; node: string; port: string };
export type Redes = { redes: Rede[]; leases: Lease[] };

// ifaceBody is a port as the API takes it (only what it has).
export const ifaceBody = (f: Iface): Iface => {
  const o: Iface = {};
  if (f.link) o.link = f.link;
  else if (f.rede) o.rede = f.rede;
  else if (f.bridge) o.bridge = f.bridge;
  if (!f.bridge && f.ips?.length) o.ips = f.ips;
  if (f.rede && f.dhcp) o.dhcp = true;
  return o;
};

// attach puts a port on a lab network (out of its cable first); the
// addresses stay when it was already on that network.
export function attach(vms: CVM[], vm: string, i: number, rede: string): CVM[] {
  const cur = vms.find((v) => v.name === vm)?.interfaces[i];
  return unplug(vms, vm, i).map((v) => (v.name === vm ? { ...v, interfaces: v.interfaces.map((f, k) => (k === i ? (cur?.rede === rede ? { ...cur } : { rede }) : f)) } : v));
}

// setAddr sets the addresses of a port: "dhcp", or a list split by commas
// or spaces ("" : none, configured inside the machine).
export function setAddr(vms: CVM[], vm: string, i: number, text: string): CVM[] {
  const s = text.trim();
  const dhcp = s.toLowerCase() === "dhcp";
  const ips = dhcp ? [] : s.split(/[\s,;]+/).filter(Boolean);
  return vms.map((v) => (v.name === vm ? { ...v, interfaces: v.interfaces.map((f, k) => (k === i ? { ...f, ips: ips.length ? ips : undefined, dhcp: dhcp || undefined } : f)) } : v));
}

// addrText is the addresses of a port as typed (and shown).
export const addrText = (f: Iface) => (f.dhcp ? "dhcp" : (f.ips ?? []).join(", "));

// takesAddr: the ports Gantry can give an address (not IOL, Dynamips, a
// bridge or a free port).
export const takesAddr = (v: CVM, f: Iface) => v.kind !== "iol" && v.kind !== "dyn" && v.kind !== "vpcs" && !f.bridge && !!(f.link || f.rede);
export type CVM = { name: string; interfaces: Iface[]; nic?: string; management: boolean; kind?: string; pos?: [number, number]; platform?: string; ports?: string[]; icon?: string; iconSet?: boolean; vpcsAddr?: string };

// fileError makes an error of the compose (as YAML reports it) say where
// to fix it.
export function fileError(msg: string): string {
  const m = /arquivo (\d+): \[(\d+):(\d+)\] (.*)/.exec(msg) || /file (\d+): \[(\d+):(\d+)\] (.*)/.exec(msg);
  return m ? t("O compose do lab tem um erro na linha {line} ({what}); nada foi mudado. Corrija na aba Arquivo.", { line: m[2], what: m[4] }) : msg;
}

// CABLES_CHANGED: the cables of a lab changed (on the panel or on the
// drawing); the other one reloads. detail: the lab id.
export const CABLES_CHANGED = "gantry:cabos";
export const cablesChanged = (lab: string) => window.dispatchEvent(new CustomEvent(CABLES_CHANGED, { detail: lab }));

// The other end of a link: [vm, port index].
export function peerOf(vms: CVM[], link: string, not: [string, number]): [string, number] | null {
  for (const v of vms)
    for (let i = 0; i < v.interfaces.length; i++) if (v.interfaces[i].link === link && !(v.name === not[0] && i === not[1])) return [v.name, i];
  return null;
}

// unplug takes the cable out of a port; the port at the other end stays
// without a cable too.
export function unplug(vms: CVM[], vm: string, i: number): CVM[] {
  const cur = vms.find((v) => v.name === vm)?.interfaces[i];
  const out = vms.map((v) => ({ ...v, interfaces: v.interfaces.map((f) => ({ ...f })) }));
  const set = (n: string, k: number) => {
    const v = out.find((x) => x.name === n);
    if (v) v.interfaces[k] = {};
  };
  if (cur?.link) {
    const p = peerOf(vms, cur.link, [vm, i]);
    if (p) set(p[0], p[1]);
  }
  set(vm, i);
  return out;
}

export function connect(vms: CVM[], a: [string, number], b: [string, number]): CVM[] {
  let out = unplug(unplug(vms, a[0], a[1]), b[0], b[1]);
  const used = new Set(out.flatMap((v) => v.interfaces.map((f) => f.link).filter(Boolean)));
  const base = a[0] === b[0] ? a[0] + "-loop" : [a[0], b[0]].sort().join("-");
  let name = base;
  for (let n = 2; used.has(name); n++) name = base + "-" + n;
  out = out.map((v) => ({
    ...v,
    interfaces: v.interfaces.map((f, i) => ((v.name === a[0] && i === a[1]) || (v.name === b[0] && i === b[1]) ? { link: name } : f)),
  }));
  return out;
}

// The name of a port as the VM sees it: its NIC number (the management NIC,
// when there is one, comes first); on an IOL node, its Ethernet slot/port.
// dynPort: FastEthernet<slot>/<port> of a Dynamips router (the 7200 has one
// port on the I/O card and two per adapter; the 3725 and 2691 two built in,
// then 16 per switch module).
export const dynPort = (platform: string | undefined, i: number) =>
  platform === "7200" ? (i === 0 ? "f0/0" : "f" + (1 + Math.floor((i - 1) / 2)) + "/" + ((i - 1) % 2)) : i < 2 ? "f0/" + i : "f" + (1 + Math.floor((i - 2) / 16)) + "/" + ((i - 2) % 16);

export const portName = (v: CVM, i: number) =>
  v.kind === "vpcs"
    ? "eth0"
    : v.kind === "dyn"
    ? v.ports?.[i] ?? dynPort(v.platform, i)
    : v.kind === "iol" ? "e" + Math.floor(i / 4) + "/" + (i % 4) : v.kind === "ctr" ? "eth" + (i + 1) : t("placa {n}", { n: i + (v.management ? 1 : 0) });

// quiet: a project without VMs (only containers on cables show; no error).
export function CablesPanel({ host, lab, quiet }: { host: string; lab: { id: string; name: string }; quiet?: boolean }) {
  const base = "/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/cabos";
  const [orig, setOrig] = useState<CVM[] | null>(null);
  const [vms, setVMs] = useState<CVM[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [redes, setRedes] = useState<Rede[]>([]);
  const load = () =>
    api<Redes>(base.replace(/cabos$/, "redes"))
      .then((r) => setRedes(r.redes))
      .catch(() => {})
      .then(() => api<CVM[]>(base))
      .then((l) => {
        setOrig(l);
        setVMs(l);
        setErr("");
      })
      .catch((e) => setErr((e as Error).message));
  useEffect(() => {
    void load();
    const on = (e: Event) => (e as CustomEvent).detail === lab.id && void load();
    window.addEventListener(CABLES_CHANGED, on);
    return () => window.removeEventListener(CABLES_CHANGED, on);
  }, [host, lab.id]); // eslint-disable-line react-hooks/exhaustive-deps
  if (err && !vms) return quiet ? null : <p className="muted small">{err}</p>;
  if (!vms) return quiet ? null : <p className="muted small">{t("Carregando…")}</p>;
  if (!vms.length) return null;
  const dirty = JSON.stringify(vms) !== JSON.stringify(orig);
  const setPortCount = (vm: string, n: number) =>
    setVMs((cur) => {
      let out = cur!;
      const v = out.find((x) => x.name === vm)!;
      for (let i = v.interfaces.length - 1; i >= n; i--) out = unplug(out, vm, i);
      return out.map((x) => (x.name === vm ? { ...x, interfaces: n > x.interfaces.length ? [...x.interfaces, ...Array.from({ length: n - x.interfaces.length }, () => ({}))] : x.interfaces.slice(0, n) } : x));
    });
  const choose = async (vm: string, i: number, value: string) => {
    if (value === "") setVMs((cur) => unplug(cur!, vm, i));
    else if (value.startsWith("rede\u0000")) setVMs((cur) => attach(cur!, vm, i, value.slice(5)));
    else if (value === "bridge") {
      formDialog({
        title: t("Ligar {vm} · {port} numa bridge", { vm, port: portName(vms.find((v) => v.name === vm)!, i) }),
        intro: t("Uma bridge Linux da máquina (br0, virbr0…). O QEMU entra nela pelo qemu-bridge-helper, que precisa da bridge liberada em /etc/qemu/bridge.conf (allow br0)."),
        submit: t("Ligar"),
        fields: [{ id: "br", label: "Bridge", value: "br0" }],
        onSubmit: async (f) => {
          const br = String(f.br || "").trim();
          if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(br)) throw new Error(t("Nome de bridge inválido."));
          setVMs((cur) => unplug(cur!, vm, i).map((x) => (x.name === vm ? { ...x, interfaces: x.interfaces.map((f2, k) => (k === i ? { bridge: br } : f2)) } : x)));
        },
      });
    } else {
      const [ovm, oi] = value.split("\u0000");
      setVMs((cur) => connect(cur!, [vm, i], [ovm, Number(oi)]));
    }
  };
  const save = async () => {
    setBusy(true);
    try {
      const body: Record<string, Iface[]> = {};
      for (const v of vms) {
        const o = orig?.find((x) => x.name === v.name);
        if (JSON.stringify(o?.interfaces) !== JSON.stringify(v.interfaces)) body[v.name] = v.interfaces.map(ifaceBody);
      }
      const r = await api<{ live?: boolean; error?: string; output?: string }>(base, { method: "PUT", body: { vms: body } });
      if (r.error) gantry.toast(r.error, "err");
      else if (r.live) gantry.toast(t("Cabos trocados ao vivo, sem reiniciar as VMs."), "ok");
      else gantry.toast(t("Cabos salvos no compose. Valem quando as VMs subirem."), "ok");
      cablesChanged(lab.id);
      loadDrift(host, lab.id, true);
    } catch (e) {
      gantry.toast(fileError((e as Error).message), "err");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="block cables">
      <div className="block-head">
        <h2 className="block-title">{t("Cabos")}</h2>
        <span className="muted small">{t("as portas de cada VM ou container e onde cada uma está ligada")}</span>
        <span className="grow" />
        {dirty ? (
          <button className="btn small btn-ghost" type="button" onClick={() => setVMs(orig)}>
            {t("Desfazer")}
          </button>
        ) : null}
        <button className="btn small primary" type="button" disabled={!dirty || busy} onClick={() => void save()}>
          {busy ? <span className="spin" /> : t("Salvar cabos")}
        </button>
      </div>
      <div className="cable-grid">
        {vms.map((v) => (
          <div key={v.name} className="panel panel-pad cable-vm">
            <div className="cable-head">
              <b>{v.name}</b>
              <span className="muted small">
                {v.kind === "iol" ? t("IOL · portas Ethernet0/0, 0/1…") : v.kind === "dyn" ? t("Dynamips c{p} · portas FastEthernet", { p: v.platform ?? "" }) : v.kind === "vpcs" ? t("VPCS · uma porta (eth0)") : v.kind === "ctr" ? t("container · portas eth1, eth2… (eth0 é a rede do engine)") : (v.nic || "virtio") + " · " + (v.management ? t("placa 0: gerência (internet e SSH)") : t("sem placa de gerência"))}
              </span>
              <span className="grow" />
              <label className="small muted">
                {t("Portas")}{" "}
                <input
                  type="number"
                  min={0}
                  max={28}
                  value={v.interfaces.length}
                  style={{ width: 56 }}
                  onChange={(e) => setPortCount(v.name, Math.max(0, Math.min(28, parseInt(e.target.value, 10) || 0)))}
                  aria-label={t("Número de portas de {vm}", { vm: v.name })}
                />
              </label>
            </div>
            {!v.interfaces.length ? <p className="muted small">{t("Sem portas extras. Aumente o número para ligar cabos.")}</p> : null}
            {v.interfaces.map((f, i) => {
              const peer = f.link ? peerOf(vms, f.link, [v.name, i]) : null;
              const value = peer ? peer[0] + "\u0000" + peer[1] : f.rede ? "rede\u0000" + f.rede : f.bridge ? "bridge" : "";
              return (
                <div key={i} className="cable-row">
                  <span className="mono small cable-port">{portName(v, i)}</span>
                  <span className="muted">⟷</span>
                  <select className="select" value={value} onChange={(e) => void choose(v.name, i, e.target.value)} aria-label={t("Onde {vm} · {port} está ligada", { vm: v.name, port: portName(v, i) })}>
                    <option value="">{t("— sem cabo —")}</option>
                    {redes.map((r) => (
                      <option key={"rede" + r.name} value={"rede\u0000" + r.name}>
                        {t("rede {name}", { name: r.name }) + (r.subnet ? " · " + r.subnet : "")}
                      </option>
                    ))}
                    {vms.flatMap((o) =>
                      o.interfaces.map((of, k) => {
                        if (o.name === v.name && k === i) return null;
                        const busyWith = of.link ? peerOf(vms, of.link, [o.name, k]) : null;
                        const taken = (busyWith && !(busyWith[0] === v.name && busyWith[1] === i)) || of.bridge;
                        return (
                          <option key={o.name + k} value={o.name + "\u0000" + k}>
                            {o.name + " · " + portName(o, k) + (taken ? t(" (troca o cabo de lá)") : "")}
                          </option>
                        );
                      }),
                    )}
                    {v.kind === "iol" || v.kind === "dyn" || v.kind === "vpcs" || v.kind === "ctr" ? null : <option value="bridge">{f.bridge ? t("bridge {name}", { name: f.bridge }) : t("bridge da máquina…")}</option>}
                  </select>
                  {takesAddr(v, f) ? (
                    <input
                      key={addrText(f)}
                      className="input mono small cable-ip"
                      defaultValue={addrText(f)}
                      placeholder={f.rede ? t("IP/máscara, dhcp ou vazio") : t("IP/máscara ou vazio")}
                      title={t("Os endereços desta porta: um ou mais IP/máscara separados por vírgula, dhcp (numa rede com DHCP) ou vazio para configurar dentro da máquina")}
                      aria-label={t("Endereços de {vm} · {port}", { vm: v.name, port: portName(v, i) })}
                      onBlur={(e) => e.target.value !== addrText(f) && setVMs((cur) => setAddr(cur!, v.name, i, e.target.value))}
                      onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
                    />
                  ) : null}
                  {(f.link || f.rede) && !dirty ? (
                    <button type="button" className="icon-btn cable-cap" title={t("Capturar o tráfego deste cabo")} aria-label={t("Capturar o tráfego de {vm} · {port}", { vm: v.name, port: portName(v, i) })} onClick={(e) => captureMenu(e, host, lab.id, v, i, portName(v, i))}>
                      ⦿
                    </button>
                  ) : null}
                </div>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}

// captureMenu: watch a cable live in Gantry (the only way now: no menu).
export function captureMenu(_e: React.MouseEvent, host: string, lab: string, v: CVM, i: number, port: string) {
  const [live] = captureItems(host, lab, v, i, port) as { onClick: () => void }[];
  live.onClick();
}

// captureItems: the ways to watch the cable on a port (or the port on its
// network).
export function captureItems(host: string, lab: string, v: CVM, i: number, port: string): MenuItem[] {
  return captureTargetItems(host, lab, "node=" + enc(v.name) + "&port=" + i, "captura:" + lab + ":" + v.name + ":" + i, t("captura {vm} · {port}", { vm: v.name, port }));
}

// redeCaptureItems: the ways to watch a whole lab network (every port on it).
export function redeCaptureItems(host: string, lab: string, rede: string): MenuItem[] {
  return captureTargetItems(host, lab, "rede=" + enc(rede), "captura:" + lab + ":rede:" + rede, t("captura da rede {rede}", { rede }));
}

// The capture opens live in Gantry; Salvar .pcap… keeps it for Wireshark.
function captureTargetItems(host: string, lab: string, q: string, id: string, name: string): MenuItem[] {
  const base = "/hosts/" + enc(host) + "/labs/" + enc(lab) + "/captura";
  return [
    {
      label: t("Ver ao vivo aqui"),
      onClick: () => {
        let file = "";
        gantry.openLogs(
          host,
          {
            id,
            name,
            onEvent: { arquivo: (d) => (file = d) },
            actions: [
              {
                label: t("Salvar .pcap…"),
                title: t("O que foi capturado até agora, para abrir no Wireshark"),
                run: async () => {
                  if (!file) return;
                  const dest = await gantry.pickPath({ kind: "salvar", title: t("Salvar a captura"), name: file.split("/").pop() || "captura.pcap" });
                  if (!dest) return;
                  try {
                    const r = await api<{ path: string }>(base + "/salvar", { method: "POST", body: { path: file, dest } });
                    gantry.toast(t("Captura salva em {path}", { path: r.path }), "ok");
                  } catch (err) {
                    gantry.toast((err as Error).message, "err");
                  }
                },
              },
            ],
          },
          base + "/ao-vivo?" + q,
        );
      },
    },
  ];
}
