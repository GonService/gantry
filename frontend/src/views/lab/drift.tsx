// When the project's files differ from what runs: a notice on the project
// page ("Aplicar agora"), a badge in the list of projects, and the option
// to apply by itself (off by default).
import { useEffect } from "react";
import { create } from "zustand";
import { hostByName } from "@/lib/store";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { enc, labHref, useApp } from "@/lib/app";
import { confirm, formDialog } from "@/components/dialog";
import { t } from "@/lib/i18n";

export type DriftItem = { service: string; kind?: string; state: string; running?: boolean };
export type Drift = { services: DriftItem[]; pending: boolean; auto?: boolean; error?: string; builds?: string[] };
type Entry = { data?: Drift; at: number; loading?: boolean };

export const useDrift = create<Record<string, Entry>>(() => ({}));

export async function loadDrift(host: string, id: string, force = false) {
  const k = host + "/" + id;
  const cur = useDrift.getState()[k];
  if (cur?.loading) return;
  if (cur && !force && Date.now() - cur.at < 10000) return;
  useDrift.setState({ [k]: { ...(cur ?? { at: 0 }), loading: true } });
  let data: Drift;
  try {
    data = await api<Drift>("/hosts/" + enc(host) + "/labs/" + enc(id) + "/drift");
  } catch (e) {
    data = { error: (e as Error).message, services: [], pending: false };
  }
  useDrift.setState({ [k]: { data, at: Date.now() } });
}

// setDrift: the file tab compares by itself and shares what it found.
export const setDrift = (host: string, id: string, data: Drift) => useDrift.setState({ [host + "/" + id]: { data, at: Date.now() } });

export const driftPending = (host: string, id: string) => !!useDrift.getState()[host + "/" + id]?.data?.pending;
export const labBuilds = (host: string, id: string) => useDrift.getState()[host + "/" + id]?.data?.builds ?? [];

// queueDriftCheck asks, in the background, whether a project in the list
// has files newer than what runs.
const queue = new Set<string>();
let chain: Promise<void> = Promise.resolve();
export function queueDriftCheck(host: string, id: string) {
  const k = host + "/" + id;
  const cur = useDrift.getState()[k];
  if (queue.has(k) || (cur && Date.now() - cur.at < 60000)) return;
  queue.add(k);
  chain = chain.then(async () => {
    await new Promise((r) => setTimeout(r, 200));
    // a machine disconnected meanwhile is not woken up by a check
    if (hostByName(host)?.ok) await loadDrift(host, id);
    queue.delete(k);
  });
}

// driftText says, in a few words, what applying does to one item.
export function driftText(x: DriftItem): string {
  if (x.kind === "cables") return t("cabos do arquivo diferentes dos ligados; Aplicar troca sem reiniciar as VMs");
  if (x.kind === "vm") {
    return (
      {
        changed: x.running ? t("VM: definição nova, reinicia ao aplicar") : t("VM: definição nova, vale ao iniciar"),
        nix: x.running ? t("Configuração Nix alterada") : t("Configuração Nix alterada · inicie a VM para aplicar"),
        missing: t("VM nova, será criada"),
        orphan: t("VM fora do arquivo (não é removida)"),
      } as Record<string, string>
    )[x.state] || x.state;
  }
  return ({ changed: t("diferente, será recriado"), missing: t("novo, será criado"), orphan: t("fora do arquivo, será removido") } as Record<string, string>)[x.state] || x.state;
}

// applyNix sends the changed Nix configuration to a running VM; the VM
// runs nixos-rebuild and the job shows how it went (with the end of the
// log when it fails, the change then stays pending).
export async function applyNix(host: string, lab: { id: string; name: string }, vm: string) {
  const key = host + "/" + lab.id + "/nix:" + vm;
  try {
    const r = await api<{ id: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/vms/" + enc(vm) + "/nix-aplicar", { method: "POST" });
    gantry.markBusy(key, "apply");
    gantry.openJob(r.id, t("Aplicar a configuração Nix · {name}", { name: vm }), async () => {
      gantry.markBusy(key, null);
      loadDrift(host, lab.id, true);
    });
  } catch (e) {
    gantry.toast((e as Error).message, "err");
  }
}

// NixApplyButton is the Aplicar of one VM whose Nix configuration changed.
export function NixApplyButton({ host, lab, item }: { host: string; lab: { id: string; name: string }; item: DriftItem }) {
  const S = useApp();
  const busy = !!S.busy[host + "/" + lab.id + "/nix:" + item.service];
  return (
    <button
      className="btn small"
      type="button"
      disabled={busy || !item.running}
      title={item.running ? t("Envia o módulo e os parâmetros para a VM e roda nixos-rebuild, sem reiniciar") : t("inicie a VM para aplicar")}
      onClick={() => applyNix(host, lab, item.service)}
    >
      {busy ? (
        <>
          <span className="spin" />
          {t("Aplicando…")}
        </>
      ) : (
        t("Aplicar")
      )}
    </button>
  );
}

export function applyLabFiles(host: string, lab: { id: string; name: string }) {
  const st = useDrift.getState()[host + "/" + lab.id];
  const vmsToRestart = (st?.data?.services ?? []).filter((x) => x.kind === "vm" && x.state === "changed" && x.running);
  const key = host + "/" + lab.id;
  const run = async (restartVms: boolean) => {
    try {
      const r = await api<{ id: string }>("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/aplicar", { method: "POST", body: { restartVms } });
      gantry.markBusy(key, "apply");
      gantry.openJob(r.id, t("Aplicar · {name}", { name: lab.name }), async () => {
        gantry.markBusy(key, null);
        await gantry.loadLabs(host);
        loadDrift(host, lab.id, true);
      });
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    }
  };
  if (!vmsToRestart.length) {
    run(false);
    return;
  }
  const names = vmsToRestart.map((x) => x.service).join(", ");
  formDialog({
    title: t("Aplicar os arquivos de {name}", { name: lab.name }),
    intro:
      vmsToRestart.length === 1
        ? t("Os containers que mudaram são recriados e as VMs novas, criadas. {names} está rodando com a definição antiga e precisa reiniciar para valer (CPU, memória, portas).", { names })
        : t("Os containers que mudaram são recriados e as VMs novas, criadas. {names} estão rodando com a definição antiga e precisam reiniciar para valer (CPU, memória, portas).", { names }),
    submit: t("Aplicar"),
    fields: [{ id: "restart", label: vmsToRestart.length === 1 ? t("Reiniciar a VM {names} agora", { names }) : t("Reiniciar as VMs {names} agora", { names }), type: "checkbox", value: true }],
    onSubmit: async (v) => run(!!v.restart),
  });
}

type LabLike = { id: string; name: string; loose?: boolean; total: number; containers?: unknown[] };

// DriftBanner is the notice on the project page.
export function DriftBanner({ host, lab }: { host: string; lab: LabLike }) {
  const S = useApp();
  const k = host + "/" + lab.id;
  const st = useDrift((s) => s[k]);
  useEffect(() => {
    loadDrift(host, lab.id);
  });
  const d = st?.data;
  if (!d) return null;
  // An error shows even before the project is brought up: it is what
  // Subir would stop on.
  if (d.error)
    return (
      <div className="banner err">
        <span className="grow">{t("Os arquivos do projeto têm um erro e não podem ser aplicados: {err}", { err: d.error })}</span>
        <a className="btn small" href={labHref(host, lab.id, "arquivo")}>
          {t("Abrir o arquivo")}
        </a>
      </div>
    );
  if (!d.pending) return null;
  const items = d.services.filter((x) => x.state !== "ok");
  // A change only in the Nix configuration of a VM has its own Aplicar;
  // the project's Aplicar agora is for the rest.
  const onlyNix = items.every((x) => x.state === "nix");
  const busy = !!S.busy[k];
  return (
    <div className="banner drift">
      <div className="grow">
        <div>
          <b>{t("Os arquivos do projeto mudaram.")}</b> {t("O que está rodando ainda é a versão anterior. Deseja aplicar agora?")}
        </div>
        <ul className="drift-items">
          {items.map((x) => (
            <li key={x.service}>
              <span className="mono">{x.service}</span> · {driftText(x)}
              {x.state === "nix" ? (
                <>
                  {" "}
                  <NixApplyButton host={host} lab={lab} item={x} />
                </>
              ) : null}
            </li>
          ))}
        </ul>
        {d.auto ? <div className="small">{t("Aplicar sozinho está ligado: o Gantry aplica quando os arquivos param de mudar (VMs não são reiniciadas sozinhas).")}</div> : null}
      </div>
      <div className="drift-actions">
        <a className="btn small" href={labHref(host, lab.id, "arquivo")}>
          {t("Ver o arquivo")}
        </a>
        {onlyNix ? null : (
          <button className="btn small primary" disabled={busy} onClick={() => applyLabFiles(host, lab)}>
            {busy ? (
              <>
                <span className="spin" />
                {t("Aplicando…")}
              </>
            ) : (
              t("Aplicar agora")
            )}
          </button>
        )}
      </div>
    </div>
  );
}

// AutoApplyToggle is the per-project switch (Arquivo tab); off by default.
export function AutoApplyToggle({ host, lab }: { host: string; lab: { id: string; name: string } }) {
  const st = useDrift((s) => s[host + "/" + lab.id]);
  const on = !!st?.data?.auto;
  return (
    <label className="check small" title={t("Desligado, o Gantry só avisa e você aplica com o botão")}>
      <input
        type="checkbox"
        checked={on}
        onChange={async (e) => {
          const want = e.target.checked;
          if (
            want &&
            !(await confirm(
              t("Aplicar sozinho em {name}?", { name: lab.name }),
              t("Quando os arquivos do projeto mudarem (pelo Gantry, pelo seu editor ou por um git pull) e ficarem alguns segundos sem mudar, o Gantry aplica sem perguntar. Um arquivo com erro não é aplicado. VMs que precisam reiniciar continuam esperando você."),
              t("Ligar"),
            ))
          )
            return;
          try {
            await api("/hosts/" + enc(host) + "/labs/" + enc(lab.id) + "/aplicar-sozinho", { method: "PUT", body: { on: want } });
            gantry.toast(want ? t("Aplicar sozinho ligado") : t("Aplicar sozinho desligado"), "ok");
            loadDrift(host, lab.id, true);
          } catch (err) {
            gantry.toast((err as Error).message, "err");
          }
        }}
      />{" "}
      {t("Aplicar sozinho quando os arquivos mudarem")}

    </label>
  );
}
