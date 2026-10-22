// The states of a network lab: the configuration of all the nodes saved
// together (a snapshot of each VM, the NVRAM of each IOL node), restored in
// one click to repeat an exercise; and starting the nodes from nothing.
import { useEffect, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { LOCALE, t } from "@/lib/i18n";
import { confirm, formDialog } from "@/components/dialog";

type State = { name: string; when: number; nodes: Record<string, number> };
type Node = { name: string; kind: "iol" | "dyn" | "vpcs" | "vm"; running: boolean };
type Data = { states: State[]; nodes: Node[] };
type Result = { ok: boolean; error?: string; output?: string };

const fmtWhen = (s: number) => (s ? new Date(s * 1000).toLocaleString(LOCALE, { dateStyle: "short", timeStyle: "short" }) : "—");

export function LabStates({ host, lab, onChange }: { host: string; lab: { id: string; name: string }; onChange?: () => void }) {
  const base = "/hosts/" + enc(host) + "/labs/" + enc(lab.id);
  const [d, setD] = useState<Data | null>(null);
  const [busy, setBusy] = useState("");
  const load = () =>
    api<Data>(base + "/estados")
      .then(setD)
      .catch(() => setD(null));
  useEffect(() => {
    void load();
  }, [base]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!d || !d.nodes.length) return null;

  const run = async (what: string, call: () => Promise<Result>, done: string) => {
    setBusy(what);
    try {
      const r = await call();
      if (r.output) gantry.showOutput(lab.name + " · " + what, { ...r, command: what });
      if (!r.ok) throw new Error(r.error || t("falhou"));
      gantry.toast(done, "ok");
    } catch (e) {
      gantry.toast((e as Error).message, "err");
    } finally {
      setBusy("");
      void load();
      onChange?.();
    }
  };
  const running = d.nodes.filter((n) => n.running).map((n) => n.name);
  const hasIOL = d.nodes.some((n) => n.kind !== "vm");
  const hasVM = d.nodes.some((n) => n.kind === "vm");

  const save = () => {
    const now = new Date();
    const p2 = (n: number) => String(n).padStart(2, "0");
    formDialog({
      title: t("Salvar o estado de {lab}", { lab: lab.name }),
      intro: t("Guarda a configuração de todos os nós juntos: um snapshot de cada VM e a NVRAM de cada nó IOL ou Dynamips. Depois, Restaurar volta tudo a este ponto."),
      submit: t("Salvar estado"),
      fields: [
        { id: "name", label: t("Nome"), value: "estado-" + p2(now.getDate()) + p2(now.getMonth() + 1) + "-" + p2(now.getHours()) + p2(now.getMinutes()) },
        ...(running.length ? [{ id: "write", label: t("Gravar a configuração dos nós rodando antes (write memory no console)"), type: "checkbox" as const, value: true }] : []),
      ],
      onSubmit: async (v) => {
        const name = String(v.name || "").trim();
        if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$/.test(name)) throw new Error(t("Use letras, números, ponto, - e _ no nome."));
        if (d.states.some((s) => s.name === name) && !(await confirm(t("Substituir o estado {name}?", { name }), t("Já existe um estado com este nome; ele será trocado pelo atual."), t("Substituir")))) return;
        void run(t("salvar estado"), () => api<Result>(base + "/estados", { method: "POST", body: { name, write: !!v.write } }), t("Estado {name} salvo", { name }));
      },
    });
  };
  const restore = async (s: State) => {
    const stopping = running.filter((n) => s.nodes[n] !== undefined);
    const missing = d.nodes.filter((n) => s.nodes[n.name] === undefined).map((n) => n.name);
    const body = [
      stopping.length ? t("Os nós rodando ({list}) param e voltam com a configuração salva.", { list: stopping.join(", ") }) : t("Os nós voltam à configuração salva."),
      missing.length ? t("Sem este estado (ficam como estão): {list}.", { list: missing.join(", ") }) : "",
      t("O que mudou desde então se perde, a menos que você salve antes."),
    ]
      .filter(Boolean)
      .join(" ");
    if (!(await confirm(t("Restaurar o estado {name}?", { name: s.name }), body, t("Restaurar")))) return;
    void run(t("restaurar {name}", { name: s.name }), () => api<Result>(base + "/estados/" + enc(s.name), { method: "POST" }), t("Lab de volta ao estado {name}", { name: s.name }));
  };
  const remove = async (s: State) => {
    if (!(await confirm(t("Apagar o estado {name}?", { name: s.name }), t("Os snapshots e as cópias da NVRAM deste estado são apagados. A configuração atual dos nós não muda."), t("Apagar")))) return;
    void run(t("apagar {name}", { name: s.name }), () => api<Result>(base + "/estados/" + enc(s.name), { method: "DELETE" }), t("Estado {name} apagado", { name: s.name }));
  };
  const zero = () =>
    formDialog({
      title: t("Recomeçar {lab} do zero", { lab: lab.name }),
      intro: (hasIOL ? t("Os nós IOL e Dynamips perdem a configuração (a NVRAM é apagada) e reiniciam limpos.") + " " : "") + t("Os estados salvos continuam na lista."),
      submit: t("Recomeçar"),
      fields: hasVM ? [{ id: "vms", label: t("Apagar também as VMs (o disco volta da imagem quando o lab subir)"), type: "checkbox" as const, value: false }] : [],
      onSubmit: async (v) => {
        void run(t("recomeçar do zero"), () => api<Result>(base + "/zerar", { method: "POST", body: { vms: !!v.vms } }), t("Lab recomeçado do zero"));
      },
    });

  return (
    <div className="block lab-states">
      <div className="block-head">
        <h2 className="block-title">{t("Estados do lab")}</h2>
        <span className="muted small">{t("a configuração de todos os equipamentos, salva junta, para repetir um exercício")}</span>
        <span className="grow" />
        {busy ? (
          <span className="muted small">
            <span className="spin" /> {busy}
          </span>
        ) : null}
        <button className="btn small btn-ghost" type="button" disabled={!!busy} onClick={zero}>
          {t("Recomeçar do zero…")}
        </button>
        <button className="btn small primary" type="button" disabled={!!busy} onClick={save}>
          {t("Salvar estado…")}
        </button>
      </div>
      {d.states.length ? (
        <div className="panel table-wrap">
          <table className="grid">
            <thead>
              <tr>
                <th>{t("Estado")}</th>
                <th>{t("Salvo em")}</th>
                <th>{t("Nós")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {d.states.map((s) => (
                <tr key={s.name}>
                  <td className="mono">{s.name}</td>
                  <td className="small">{fmtWhen(s.when)}</td>
                  <td className="small muted">{Object.keys(s.nodes).length === d.nodes.length ? t("todos ({n})", { n: d.nodes.length }) : Object.keys(s.nodes).join(", ")}</td>
                  <td className="row-actions">
                    <button className="btn small" type="button" disabled={!!busy} onClick={() => void restore(s)}>
                      {t("Restaurar")}
                    </button>
                    <button className="btn small btn-ghost" type="button" disabled={!!busy} onClick={() => void remove(s)}>
                      {t("Apagar")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="muted small">{t("Nenhum estado salvo. Configure os equipamentos e use Salvar estado para voltar a este ponto quando quiser.")}</p>
      )}
    </div>
  );
}
