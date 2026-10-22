// Pieces of the VM rows, on the machine page and in projects.
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, fmtPct } from "@/lib/app";
import { copyText, fmtBytes } from "@/lib/format";
import { confirm } from "@/components/dialog";
import { t } from "@/lib/i18n";

export const VM_STATE: Record<string, [string, string]> = { running: ["on", t("rodando")], stopped: ["", t("parada")], missing: ["", t("não criada")], unknown: ["warn", t("sem resposta")] };

export function vmPorts(list: string[] | undefined): string[] {
  return (list || []).map((p) => {
    const [proto, ip, hp, gp] = p.split(":");
    return (ip && ip !== "0.0.0.0" ? ip + ":" : ":") + hp + "→" + gp + (proto !== "tcp" ? "/" + proto : "");
  });
}

// vmStatKey is how /stats names a VM.
export const vmStatKey = (project: string, name: string) => "vm:" + project + "/" + name;

type Usage = { cpu: number; mem: number; disk?: number } | null | undefined;

// VMUsageCells: CPU, memory and disk of a VM (the QEMU process).
export function VMUsageCells({ u, running }: { u: Usage; running: boolean }) {
  const wait = running ? (
    <span className="muted" title={t("Coletando: a primeira amostra leva alguns segundos")}>
      …
    </span>
  ) : (
    "—"
  );
  return (
    <>
      <td className="num">{u ? fmtPct(u.cpu) : wait}</td>
      <td className="num" title={t("Memória do processo QEMU")}>
        {u ? fmtBytes(u.mem) : wait}
      </td>
      <td className="num" title={t("Espaço que o disco ocupa na máquina")}>
        {u && u.disk ? fmtBytes(u.disk) : running ? wait : "—"}
      </td>
    </>
  );
}

type VMRef = { name: string; state: string; vnc?: boolean; sshPort?: number; serial?: number; noCloudInit?: boolean };

// VMAccess: Shell and Logs in the app (SSH with the VMs' key), Console
// (the serial output, useful before SSH is up).
export function VMAccess({ host, project, vm, base }: { host: string; project: string; vm: VMRef; base: string }) {
  const running = vm.state === "running";
  const ref = (logs: boolean) => ({
    id: "vm-" + project + "-" + vm.name + (logs ? "-logs" : ""),
    name: "VM " + vm.name + (logs ? " · logs" : ""),
    service: "VM " + vm.name + (logs ? " · logs" : ""),
    termURL: "/hosts/" + enc(host) + "/labs/" + enc(project) + "/vms/" + enc(vm.name) + "/term" + (logs ? "?logs=1" : ""),
  });
  // No Gantry user inside (ISO installs, network appliances): no Shell.
  const iso = !!vm.vnc || !!vm.noCloudInit;
  const serial = {
    id: "vm-" + project + "-" + vm.name + "-serial",
    name: "VM " + vm.name + " · console",
    service: "VM " + vm.name + " · console",
    termURL: "/hosts/" + enc(host) + "/labs/" + enc(project) + "/vms/" + enc(vm.name) + "/term?serial=1",
  };
  return (
    <>
      {running && !iso ? (
        <button className="btn small" title={t("Terminal na VM, dentro do Gantry")} onClick={() => gantry.openTerminal(host, ref(false))}>
          Shell
        </button>
      ) : null}
      {running && !iso ? (
        <button className="btn small" title={t("Logs do sistema da VM ao vivo (journalctl -f)")} onClick={() => gantry.openTerminal(host, ref(true))}>
          Logs
        </button>
      ) : null}
      {running && vm.serial ? (
        <button className="btn small" title={t("Console serial interativo, como um cabo de console: o boot, o IOS de um switch, o login de uma VM sem SSH")} onClick={() => gantry.openTerminal(host, serial)}>
          Console
        </button>
      ) : vm.state !== "missing" ? (
        <button className="btn small" title={t("Saída serial: o boot e o cloud-init, antes do SSH estar pronto")} onClick={() => gantry.openLogs(host, { id: "vm-" + project + "-" + vm.name, name: "VM " + vm.name }, base + "/console")}>
          Console
        </button>
      ) : null}
    </>
  );
}

// SSHCell: the SSH port of the VM; a click copies the ssh command.
export function SSHCell({ vm, base }: { vm: VMRef; base: string }) {
  if (!vm.sshPort) return <>—</>;
  if (vm.state !== "running" || vm.vnc) return <>{":" + vm.sshPort + "→22"}</>;
  return (
    <button
      type="button"
      className="port-link"
      title={t("Copiar o comando ssh (para usar num terminal seu)")}
      onClick={async () => {
        try {
          const r = await api<{ command: string }>(base + "/ssh");
          gantry.toast((await copyText(r.command)) ? t("Comando SSH copiado") : r.command, "ok");
        } catch (err) {
          gantry.toast((err as Error).message, "err");
        }
      }}
    >
      {":" + vm.sshPort + "→22"}
      <span className="port-go"> ⧉</span>
    </button>
  );
}

// openVMScreen opens the VNC screen of a VM installed from an ISO.
export async function openVMScreen(base: string, name: string) {
  try {
    const r = await api<{ address: string; password: string; error?: string }>(base + "/screen?open=1");
    await confirm(
      t("Tela da VM {name}", { name }),
      <span style={{ whiteSpace: "pre-wrap" }}>
        {t("Abrindo o visualizador de VNC do sistema.\n\nEndereço: {address}\nSenha: {password}", { address: r.address, password: r.password }) + (r.error ? t("\n\nNão consegui abrir sozinho ({error}); use um cliente VNC com esses dados.", { error: r.error }) : "")}
      </span>,
      "Ok",
    );
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}
