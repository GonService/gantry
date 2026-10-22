// The VMs and the containers of a project.
import { copyItem, openMenu } from "@/components/contextmenu";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { ctrHref, enc, fmtPct, labHref, useApp, type Host } from "@/lib/app";
import { fmtBytes } from "@/lib/format";
import { t } from "@/lib/i18n";
import { confirm, Modal, openDialog } from "@/components/dialog";
import { FilterInput, filterItems, useFilter, usePage } from "@/components/list";
import { PortLabel, ensureWeb } from "@/views/shared/web";
import { openVMScreen, SSHCell, VM_STATE, VMAccess, VMUsageCells, vmPorts, vmStatKey } from "@/views/shared/vm";
import type { Lab, LabCtr, LabVM } from "./types";

// Query string that sends a container call to the engine it lives in.
export const rtq = (hst: Host, c: { runtime?: string }) => (c.runtime && c.runtime !== hst.runtime ? "?rt=" + enc(c.runtime) : "");

function Spinning({ onClick, className = "btn small", title, children }: { onClick: () => Promise<unknown>; className?: string; title?: string; children: React.ReactNode }) {
  const [busy, setBusy] = useState(false);
  return (
    <button
      className={className}
      title={title}
      disabled={busy}
      onClick={async () => {
        setBusy(true);
        try {
          await onClick();
        } finally {
          setBusy(false);
        }
      }}
    >
      {busy ? <span className="spin" /> : children}
    </button>
  );
}

export function VMsTable({ hst, lab }: { hst: Host; lab: Lab }) {
  const S = useApp();
  const base = "/hosts/" + enc(hst.name) + "/labs/" + enc(lab.id) + "/vms/";
  const vms = lab.vms ?? [];
  useEffect(() => ensureWeb(hst.name), [hst.name]);
  const op = (vm: LabVM, action: string, label: string, ask?: [string, string]) => (
    <Spinning
      className={"btn small" + (action === "rm" ? " danger" : "")}
      onClick={async () => {
        if (ask && !(await confirm(ask[0], ask[1], label, true))) return;
        try {
          const r = await api<{ ok: boolean; error?: string; output?: string }>(base + enc(vm.name) + "/" + action, { method: "POST" });
          if (!r.ok) {
            gantry.showOutput("VM " + vm.name, r);
            gantry.toast(r.error || t("falhou"), "err");
          } else
            gantry.toast(
              "VM " +
                vm.name +
                ": " +
                (r.output || "ok")
                  .trim()
                  .split("\n")
                  .pop(),
              "ok",
            );
        } catch (err) {
          gantry.toast((err as Error).message, "err");
        }
        await gantry.loadLabs(hst.name);
      }}
    >
      {label}
    </Spinning>
  );
  const stats = S.stats[hst.name] || {};
  const lanCol = vms.some((vm) => vm.lan);
  const heads = ["VM", t("Estado"), "CPU", t("Memória"), t("Disco"), lanCol ? t("Rede do projeto") : null, t("Portas"), "SSH", ""].filter((h) => h !== null) as string[];
  return (
    <div className="block">
      <div className="block-head">
        <h2 className="block-title">{t("Máquinas virtuais")}</h2>
        <span className="muted small">QEMU · x-vms</span>
      </div>
      <div className="panel table-wrap">
        <table className="grid compact">
          <thead>
            <tr>
              {heads.map((h, i, a) => (
                <th key={i} className={i === a.length - 1 ? "act" : i >= 2 && i <= 4 ? "num" : undefined}>
                  {h || <span className="sr">{t("Ações")}</span>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {vms.map((vm) => {
              const st = VM_STATE[vm.state] || ["", vm.state];
              const running = vm.state === "running";
              const u = running ? stats[vmStatKey(lab.id, vm.name)] : null;
              const vbase = base + enc(vm.name);
              return (
                <tr key={vm.name}>
                  <td>
                    <div className="cname">
                      <a className="cname-link" href={labHref(hst.name, lab.id, "metricas")} title={t("Ver as métricas")}>
                        <span className={"dot " + st[0]} />
                        {vm.name}
                        <span className="rt-chip rt-vm">{vm.accel === "iol" ? "iol" : vm.accel === "dynamips" ? "dynamips" : vm.accel === "vpcs" ? "vpcs" : "vm"}</span>
                      </a>
                    </div>
                  </td>
                  <td className="small">
                    {st[1]}
                    {vm.rejected ? (
                      <>
                        {" · "}
                        <button type="button" className="link-btn warn-text" onClick={() => openRejected(hst.name, lab.id, vm.name)} title={t("Linhas da configuração inicial que o IOS recusou ao iniciar")}>
                          {vm.rejected === 1 ? t("1 linha recusada") : t("{n} linhas recusadas", { n: vm.rejected })}
                        </button>
                      </>
                    ) : null}
                    {vm.accel === "dynamips" && vm.noIdlePC ? (
                      <>
                        {" · "}
                        <button type="button" className="link-btn warn-text" onClick={() => vmManageDialog(hst.name, lab.id, vm)} title={t("Sem o idle-PC, o roteador ocupa uma thread inteira do processador mesmo parado. Calcule em Gerenciar, com ele no prompt.")}>
                          {t("sem idle-PC")}
                        </button>
                      </>
                    ) : null}
                    {vm.accel === "tcg" && running ? (
                      <span className="muted" title={t("Sem KVM/HVF: a VM roda em emulação e fica lenta")}>
                        {" "}
                        {t("· sem aceleração")}
                      </span>
                    ) : null}
                  </td>
                  <VMUsageCells u={u} running={running} />
                  {lanCol ? (
                    <td className="mono" title={t("Endereço na rede do projeto: os containers e as outras VMs usam o nome {name}", { name: vm.name })}>
                      {vm.lan || "—"}
                    </td>
                  ) : null}
                  <td className="mono">
                    {vm.ports.length ? (
                      <div className="ports">
                        {vm.ports.map((raw) => (
                          <PortLabel key={raw} host={hst.name} port={raw.split(":")[2]} text={vmPorts([raw])[0]} />
                        ))}
                      </div>
                    ) : (
                      "—"
                    )}
                  </td>
                  <td className="mono">
                    <SSHCell vm={vm} base={vbase} />
                  </td>
                  <td className="act">
                    {running && vm.vnc ? (
                      <button className="btn small" onClick={() => openVMScreen(vbase, vm.name)}>
                        {t("Tela")}
                      </button>
                    ) : null}
                    <VMAccess host={hst.name} project={lab.id} vm={vm} base={vbase} />
                    {running ? op(vm, "stop", t("Parar")) : op(vm, "start", vm.state === "missing" ? t("Criar") : t("Iniciar"))}
                    {running ? op(vm, "restart", t("Reiniciar")) : null}
                    {vm.state !== "missing" ? (
                      <button className="btn small" title={t("Snapshots, disco, clonar e iniciar com a máquina")} onClick={() => vmManageDialog(hst.name, lab.id, vm)}>
                        {t("Gerenciar…")}
                      </button>
                    ) : null}
                    {vm.state !== "missing" ? op(vm, "rm", t("Remover"), [t('Remover a VM "{name}"?', { name: vm.name }), t("A VM é desligada e o disco dela é apagado. Subir o projeto cria uma nova, do zero.")]) : null}
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

// VMPortsEdit publishes or takes out a port of the VM without restarting
// it (the QEMU monitor), and the compose gets the new list.
function VMPortsEdit({ base, vm, host }: { base: string; vm: LabVM; host: string }) {
  const [ports, setPorts] = useState<string[]>(() => (vm.ports ?? []).filter((p) => !p.endsWith(":9100")));
  const [add, setAdd] = useState("");
  const [busy, setBusy] = useState(false);
  // proto:ip:host:guest (as the VM reports it) -> how the compose writes it.
  const short = (p: string) => {
    const [proto, ip, hp, gp] = p.split(":");
    return (ip && ip !== "0.0.0.0" ? ip + ":" : "") + hp + ":" + gp + (proto !== "tcp" ? "/" + proto : "");
  };
  const change = async (op: "add" | "remove", port: string) => {
    setBusy(true);
    try {
      const r = await api<{ output?: string }>(base + "/portas", { method: "POST", body: { op, port } });
      gantry.toast(r.output || t("Feito"), "ok");
      if (op === "add") {
        const q = port.includes("/") ? port.split("/") : [port, "tcp"];
        const parts = q[0].split(":");
        const [ip, hp, gp] = parts.length > 2 ? [parts.slice(0, -2).join(":"), parts[parts.length - 2], parts[parts.length - 1]] : ["", parts[0], parts[1]];
        setPorts((x) => [...x, q[1] + ":" + ip + ":" + hp + ":" + gp]);
        setAdd("");
      } else setPorts((x) => x.filter((p) => short(p) !== port));
      void gantry.loadLabs(host);
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <h2 style={{ margin: "12px 0 6px" }}>{t("Portas")}</h2>
      <p className="hint" style={{ marginTop: 0 }}>
        {vm.state === "running" ? t("Publicar ou tirar uma porta vale na hora, sem reiniciar a VM; o compose é atualizado junto.") : t("A VM está parada: a porta entra no compose e vale quando ela subir.")}
      </p>
      {ports.length ? (
        <table className="table">
          <tbody>
            {ports.map((p) => (
              <tr key={p}>
                <td className="mono">{short(p)}</td>
                <td className="row-actions">
                  <button className="btn small" disabled={busy} onClick={() => void change("remove", short(p))}>
                    {t("Tirar")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      <div className="input-pick">
        <input placeholder="8080:80" spellCheck={false} value={add} onChange={(e) => setAdd(e.target.value)} onKeyDown={(e) => e.key === "Enter" && add.trim() && void change("add", add.trim())} aria-label={t("Porta do host:porta da VM")} />
        <button className="btn small primary" disabled={busy || !add.trim()} onClick={() => void change("add", add.trim())}>
          {t("Publicar")}
        </button>
      </div>
    </>
  );
}

// vmManageDialog: snapshots, disk size, clone and start with the machine.
function vmManageDialog(host: string, lab: string, vm: LabVM) {
  openDialog((close) => <VMManage host={host} lab={lab} vm={vm} close={close} />);
}

type Snap = { name: string; date: string; memory?: boolean };

function VMManage({ host, lab, vm, close }: { host: string; lab: string; vm: LabVM; close: () => void }) {
  const base = "/hosts/" + enc(host) + "/labs/" + enc(lab) + "/vms/" + enc(vm.name);
  const [snaps, setSnaps] = useState<Snap[] | null>(null);
  const [err, setErr] = useState("");
  const [snapName, setSnapName] = useState("");
  const [size, setSize] = useState("");
  const [cloneName, setCloneName] = useState("");
  const [auto, setAuto] = useState(!!vm.autostart);
  const [autoBusy, setAutoBusy] = useState(false);
  const loadSnaps = async () => {
    try {
      setSnaps(await api<Snap[]>(base + "/snapshots"));
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  const prog = vm.accel === "iol" || vm.accel === "dynamips" || vm.accel === "vpcs";
  useEffect(() => {
    if (!prog) loadSnaps();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const runJob = async (url: string, body: unknown, title: string, after?: () => void) => {
    try {
      const r = await api<{ id: string }>(url, { method: "POST", body: body || {} });
      gantry.openJob(r.id, title, async () => {
        await gantry.loadLabs(host);
        after?.();
      });
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  return (
    <Modal
      title={"VM " + vm.name}
      onClose={close}
      actions={
        <button className="btn" onClick={close}>
          {t("Fechar")}
        </button>
      }
    >
      <div className="vm-manage">
        {vm.accel === "vpcs" ? (
          <p className="hint" style={{ marginTop: 12 }}>
            {t("VPCS: um computador simples (ip, ping, trace), um programa sem disco. A configuração fica no startup.vpc do nó (use save no console).")}
          </p>
        ) : vm.accel === "iol" ? (
          <p className="hint" style={{ marginTop: 12 }}>
            {t("Nó IOL: roda como um programa, sem disco de VM. A configuração fica na NVRAM do nó (use write memory no console).")}
          </p>
        ) : vm.accel === "dynamips" ? (
          <>
            <p className="hint" style={{ marginTop: 12 }}>
              {t("Nó Dynamips: o roteador roda emulado como um programa, sem disco de VM. A configuração fica na NVRAM do nó (use write memory no console).")}
            </p>
            <h2 style={{ margin: "12px 0 6px" }}>Idle-PC</h2>
            <p className="hint" style={{ marginTop: 0 }}>
              {t("Sem o idle-PC, o Dynamips usa um núcleo inteiro mesmo com o roteador parado no prompt. Com o nó rodando e já no prompt (Router>), calcule uma vez: o valor fica guardado para a imagem e vale para todos os nós dela.")}
            </p>
            <button
              className="btn small primary"
              disabled={vm.state !== "running"}
              title={vm.state !== "running" ? t("Inicie o nó e espere o prompt") : undefined}
              onClick={async () => {
                if (!(await confirm(t("Calcular o idle-PC de {vm}?", { vm: vm.name }), t("Leva até um minuto. Depois o nó reinicia para usar o valor: grave a configuração antes (write memory)."), t("Calcular")))) return;
                void runJob(base + "/idle-pc", { restart: true }, t("Idle-PC de {vm}", { vm: vm.name }));
              }}
            >
              {t("Calcular idle-PC")}
            </button>
          </>
        ) : (
          <>
            {vm.noCloudInit ? null : <VMPortsEdit base={base} vm={vm} host={host} />}
            <h2 style={{ margin: "12px 0 6px" }}>Snapshots</h2>
            <p className="hint" style={{ marginTop: 0 }}>
              {vm.state === "running" ? t("Com a VM rodando, o snapshot guarda também a memória: voltar a ele retoma de onde estava, sem reiniciar.") : t("Com a VM parada, o snapshot guarda o disco.")}
            </p>
            {err ? (
              <p className="flash flash-err">{err}</p>
            ) : !snaps ? (
              <span className="muted small">
                <span className="spin" /> {t("carregando…")}
              </span>
            ) : snaps.length ? (
              <table className="table">
                <tbody>
                  {snaps.map((sn) => (
                    <tr key={sn.name}>
                      <td className="mono">{sn.name}</td>
                      <td className="muted small">{sn.date + (sn.memory ? t(" · com memória") : t(" · só disco"))}</td>
                      <td className="row-actions">
                        <button
                          className="btn small"
                          onClick={async () => {
                            if (
                              !(await confirm(
                                t("Voltar {vm} a {snap}?", { vm: vm.name, snap: sn.name }),
                                sn.memory && vm.state === "running"
                                  ? t("A VM volta exatamente àquele momento, com a memória, sem reiniciar. O que mudou depois é perdido.")
                                  : t("O disco volta àquele momento (a VM reinicia se estiver rodando). O que mudou depois é perdido."),
                                t("Voltar|snapshot"),
                                true,
                              ))
                            )
                              return;
                            runJob(base + "/snapshots/" + enc(sn.name) + "/restore", {}, t("Voltar {vm} a {snap}", { vm: vm.name, snap: sn.name }), loadSnaps);
                          }}
                        >
                          {t("Voltar a este")}
                        </button>
                        <button
                          className="btn small danger"
                          onClick={async () => {
                            if (!(await confirm(t("Apagar o snapshot {name}?", { name: sn.name }), t("O estado salvo é apagado; a VM não muda."), t("Apagar"), true))) return;
                            try {
                              await api(base + "/snapshots/" + enc(sn.name), { method: "DELETE" });
                              loadSnaps();
                            } catch (e) {
                              gantry.toast((e as Error).message, "err");
                            }
                          }}
                        >
                          {t("Apagar")}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p className="muted small" style={{ margin: 0 }}>
                {t("Nenhum snapshot ainda.")}
              </p>
            )}
            <div className="input-pick" style={{ marginTop: 8 }}>
              <input placeholder={t("antes-da-atualizacao")} spellCheck={false} value={snapName} onChange={(e) => setSnapName(e.target.value)} />
              <button className="btn small primary" onClick={() => runJob(base + "/snapshots", { name: snapName.trim() }, t("Snapshot de {name}", { name: vm.name }), loadSnaps)}>
                {t("Criar snapshot")}
              </button>
            </div>
            <h2 style={{ margin: "16px 0 6px" }}>{t("Disco")}</h2>
            <div className="input-pick">
              <input placeholder="40G" spellCheck={false} style={{ width: 110 }} value={size} onChange={(e) => setSize(e.target.value)} />
              <button
                className="btn small"
                onClick={async () => {
                  if (!(await confirm(t("Aumentar o disco de {name} para {size}?", { name: vm.name, size }), t("A VM é parada e iniciada de novo. Só dá para aumentar. O tamanho também é gravado no compose."), t("Aumentar")))) return;
                  runJob(base + "/resize", { size: size.trim() }, t("Disco de {name}", { name: vm.name }));
                }}
              >
                {t("Aumentar disco")}
              </button>
            </div>
            <h2 style={{ margin: "16px 0 6px" }}>{t("Clonar")}</h2>
            <p className="hint" style={{ marginTop: 0 }}>
              {t("Copia a VM com o disco para uma nova no mesmo projeto, sem as portas publicadas (para não conflitar). Ela entra no compose e sobe como uma máquina nova (nome e chaves próprios).")}
            </p>
            <div className="input-pick">
              <input placeholder={vm.name + "-2"} spellCheck={false} value={cloneName} onChange={(e) => setCloneName(e.target.value)} />
              <button
                className="btn small"
                onClick={() => {
                  runJob(base + "/clone", { name: cloneName.trim() || vm.name + "-2" }, t("Clonar {name}", { name: vm.name }));
                  close();
                }}
              >
                {t("Clonar")}
              </button>
            </div>
          </>
        )}
        <h2 style={{ margin: "16px 0 6px" }}>{t("Inicialização")}</h2>
        <label className="check" style={{ display: "flex", gap: 8, alignItems: "center" }}>
          <input
            type="checkbox"
            checked={auto}
            disabled={autoBusy}
            onChange={async (e) => {
              const on = e.target.checked;
              setAuto(on);
              setAutoBusy(true);
              try {
                const r = await api<{ hook?: string; output?: string }>(base + "/autostart", { method: "POST", body: { on } });
                gantry.toast(r.hook || r.output || "ok", "ok");
                gantry.loadLabs(host);
              } catch (err) {
                gantry.toast((err as Error).message, "err");
                setAuto(!on);
              }
              setAutoBusy(false);
            }}
          />
          {t("Iniciar junto com a máquina")}
        </label>
      </div>
    </Modal>
  );
}

export function portsOf(c: LabCtr) {
  const seen = new Set<string>();
  const out: LabCtr["ports"] = [];
  for (const p of c.ports) {
    const key = p.PublicPort ? p.PublicPort + "→" + p.PrivatePort + "/" + p.Type : p.PrivatePort + "/" + p.Type;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(p);
  }
  out.sort((a, b) => (b.PublicPort ? 1 : 0) - (a.PublicPort ? 1 : 0) || a.PrivatePort - b.PrivatePort);
  return out;
}

export function ContainersTable({ hst, lab }: { hst: Host; lab: Lab }) {
  const S = useApp();
  const pk = "lab:" + hst.name + "/" + lab.id;
  const [q] = useFilter(pk);
  const matched = filterItems(q, lab.containers, (c) => c.name + " " + (c.service || "") + " " + c.image + " " + c.state);
  const pg = usePage(pk, matched);
  useEffect(() => ensureWeb(hst.name), [hst.name]);
  if (!lab.containers.length)
    return (
      <div className="panel panel-pad">
        <p className="muted" style={{ margin: 0 }}>
          {t("Nenhum container criado. Use Subir para criar a partir do arquivo compose.")}
        </p>
      </div>
    );
  const st = S.stats[hst.name] || {};
  const act = (c: LabCtr, action: string, label: string) => (
    <Spinning
      onClick={async () => {
        try {
          await api("/hosts/" + enc(hst.name) + "/containers/" + c.id + "/" + action + rtq(hst, c), { method: "POST" });
        } catch (err) {
          gantry.toast((err as Error).message, "err");
        }
        await gantry.loadLabs(hst.name);
      }}
    >
      {label}
    </Spinning>
  );
  // The same actions from the right-click menu.
  const run = async (c: LabCtr, action: string) => {
    try {
      const base = "/hosts/" + enc(hst.name) + "/containers/" + c.id;
      await api(action === "rm" ? base + rtq(hst, c) : base + "/" + action + rtq(hst, c), { method: action === "rm" ? "DELETE" : "POST" });
      if (action === "rm") gantry.toast(t("Container {name} removido", { name: c.name }), "ok");
    } catch (err) {
      gantry.toast((err as Error).message, "err");
    }
    await gantry.loadLabs(hst.name);
  };
  const ctrMenu = (e: React.MouseEvent, c: LabCtr) => {
    const running = c.state === "running";
    openMenu(
      e,
      [
        { label: t("Abrir"), onClick: () => void (location.hash = ctrHref(hst.name, rtq(hst, c) ? c.runtime : "", c.id)) },
        { label: "Logs", onClick: () => gantry.openLogs(hst.name, c) },
        running ? { label: "Shell", onClick: () => gantry.openTerminal(hst.name, c) } : null,
        "-",
        c.state === "paused" ? { label: t("Retomar"), onClick: () => run(c, "unpause") } : running ? { label: t("Parar"), onClick: () => run(c, "stop") } : { label: t("Iniciar"), onClick: () => run(c, "start") },
        running ? { label: t("Reiniciar"), onClick: () => run(c, "restart") } : null,
        "-",
        copyItem(t("Copiar ID"), c.id, () => gantry.toast(t("Copiado"), "ok")),
        "-",
        {
          label: t("Remover…"),
          danger: true,
          onClick: async () => {
            const msg = running ? t("O container está rodando e será parado antes. Volumes nomeados são mantidos.") : t("Volumes nomeados são mantidos.");
            if (!(await confirm(t('Remover o container "{name}"?', { name: c.name }), msg, t("Remover"), true))) return;
            await run(c, "rm");
          },
        },
      ],
      c.service || c.name,
    );
  };
  return (
    <div>
      {lab.containers.length > 10 ? (
        <div className="list-tools">
          <FilterInput listKey={pk} placeholder={t("Filtrar containers…")} />
        </div>
      ) : null}
      <div className="panel table-wrap">
        <table className="grid">
          <thead>
            <tr>
              {[t("Nome"), t("Imagem"), t("Estado"), "CPU", t("Memória"), t("Portas"), "IP", ""].map((h, i) => (
                <th key={i} className={i === 7 ? "act" : undefined}>
                  {h || <span className="sr">{t("Ações")}</span>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pg.items.map((c) => {
              const running = c.state === "running";
              const s = st[c.id];
              const dot = running ? "on" : c.state === "restarting" || c.state === "paused" ? "warn" : c.state === "dead" ? "err" : "";
              const ports = portsOf(c);
              return (
                <tr key={c.id} onContextMenu={(e) => ctrMenu(e, c)}>
                  <td>
                    <div className="cname">
                      <a className="cname-link" href={ctrHref(hst.name, rtq(hst, c) ? c.runtime : "", c.id)}>
                        <span className={"dot " + dot} />
                        {c.service || c.name}
                        {lab.runtimes && lab.runtimes.length > 1 && c.runtime ? <span className={"rt-chip rt-" + c.runtime}>{c.runtime}</span> : null}
                      </a>
                      {c.service ? <small>{c.name}</small> : null}
                    </div>
                  </td>
                  <td className="mono">{c.image.length > 42 ? "…" + c.image.slice(-40) : c.image}</td>
                  <td className="small">{c.status}</td>
                  <td className="num">{running && s ? fmtPct(s.cpu) : "—"}</td>
                  <td className="num">{running && s ? fmtBytes(s.mem) : "—"}</td>
                  <td className="mono">
                    {ports.length ? (
                      <div className="ports">
                        {ports.map((p, i) =>
                          p.PublicPort ? (
                            <PortLabel key={i} host={hst.name} port={p.PublicPort} text={(p.IP && p.IP !== "0.0.0.0" && p.IP !== "::" ? p.IP + ":" : ":") + p.PublicPort + "→" + p.PrivatePort} />
                          ) : (
                            <span key={i} className="muted">
                              {p.PrivatePort + "/" + p.Type}
                            </span>
                          ),
                        )}
                      </div>
                    ) : (
                      "—"
                    )}
                  </td>
                  <td className="mono">{c.ips.length ? c.ips.join(", ") : "—"}</td>
                  <td className="act">
                    {running ? (
                      <button className="btn small" onClick={() => gantry.openTerminal(hst.name, c)}>
                        Shell
                      </button>
                    ) : null}
                    <button className="btn small" onClick={() => gantry.openLogs(hst.name, c)}>
                      Logs
                    </button>
                    {c.state === "paused" ? act(c, "unpause", t("Retomar")) : running ? act(c, "stop", t("Parar")) : act(c, "start", t("Iniciar"))}
                    {running ? act(c, "restart", t("Reiniciar")) : null}
                    <Spinning
                      className="btn small danger"
                      title={t("Remover o container")}
                      onClick={async () => {
                        const msg = running ? t("O container está rodando e será parado antes. Volumes nomeados são mantidos.") : t("Volumes nomeados são mantidos.");
                        if (!(await confirm(t('Remover o container "{name}"?', { name: c.name }), msg, t("Remover"), true))) return;
                        try {
                          await api("/hosts/" + enc(hst.name) + "/containers/" + c.id + rtq(hst, c), { method: "DELETE" });
                          gantry.toast(t("Container {name} removido", { name: c.name }), "ok");
                        } catch (err) {
                          gantry.toast((err as Error).message, "err");
                        }
                        await gantry.loadLabs(hst.name);
                      }}
                    >
                      {t("Remover")}
                    </Spinning>

                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {pg.bar}
    </div>
  );
}

type Rejected = { line: string; message: string };

// openRejected shows the lines of the startup configuration that the IOS of
// a node refused when it started: typos, commands of another IOS version,
// interfaces this router does not have.
function openRejected(host: string, lab: string, vm: string) {
  openDialog((close) => <RejectedDialog host={host} lab={lab} vm={vm} close={close} />);
}

function RejectedDialog({ host, lab, vm, close }: { host: string; lab: string; vm: string; close: () => void }) {
  const [list, setList] = useState<Rejected[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    api<Rejected[]>("/hosts/" + enc(host) + "/labs/" + enc(lab) + "/vms/" + enc(vm) + "/recusadas")
      .then(setList)
      .catch((e) => setErr((e as Error).message));
  }, [host, lab, vm]);
  return (
    <Modal title={t("Linhas recusadas · {vm}", { vm })} wide onClose={close}>
      <p className="muted small">
        {t("O IOS não aceitou estas linhas da configuração inicial e seguiu sem elas. Pode ser erro de digitação, comando de outra versão do IOS ou uma interface que este equipamento não tem. Elas não estão na configuração em uso (show running-config).")}
      </p>
      {err ? <p className="flash flash-err">{err}</p> : null}
      {list ? (
        list.length ? (
          <table className="grid compact">
            <thead>
              <tr>
                <th>{t("Linha")}</th>
                <th>{t("O IOS disse")}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((r, i) => (
                <tr key={i}>
                  <td className="mono small">{r.line || "—"}</td>
                  <td className="small">{r.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="muted">{t("Nenhuma linha recusada no último início.")}</p>
        )
      ) : null}
    </Modal>
  );
}
