// The cloud-init of each VM while the project is made: the part the user
// writes (any cloud-config: users, write_files, runcmd, timezone…), saved
// in a file of the project, and what the VM gets on its first boot, which
// is that part merged with what Gantry builds (the gantry user and key,
// packages, startup script, project network).
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { t } from "@/lib/i18n";
import { CodeEditor, useDebounced } from "@/components/editor";

type CIVM = { name: string; extra: string; file?: string; userData?: string; error?: string; installer?: boolean };

export const CI_SAMPLE = `# ${t("cloud-config da VM, somado ao que o Gantry gera")}
timezone: America/Sao_Paulo
write_files:
  - path: /etc/motd
    content: |
      ${t("VM criada pelo Gantry")}
runcmd:
  - echo pronto > /root/pronto
`;

export function VMCloudInit({ yaml, env, files, onChange }: { yaml: string; env: string; files: Record<string, string>; onChange: (yaml: string, files: Record<string, string>) => void }) {
  const [list, setList] = useState<CIVM[] | null>(null);
  const [cur, setCur] = useState("");
  const [view, setView] = useState<"editar" | "resultado">("editar");
  // What the user is typing, per VM (the preview does not overwrite it).
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [err, setErr] = useState("");
  const seq = useRef(0);
  const latest = useRef({ yaml, files, onChange });
  latest.current = { yaml, files, onChange };

  useDebounced(
    () => {
      const my = ++seq.current;
      api<CIVM[]>("/cloud-init", { method: "POST", body: { compose: yaml, env, files } })
        .then((l) => {
          if (my !== seq.current) return;
          setList(l);
          if (!l.some((x) => x.name === cur) && l.length) setCur(l[0].name);
        })
        .catch(() => my === seq.current && setList((l) => l ?? []));
    },
    350,
    [yaml, env, JSON.stringify(files)],
  );

  const vm = list?.find((x) => x.name === cur);
  const text = vm ? (draft[vm.name] ?? vm.extra) : "";

  // A pause in the typing writes the part to its file and points the
  // compose to it.
  useDebounced(
    () => {
      if (!vm || draft[vm.name] === undefined || draft[vm.name] === vm.extra) return;
      const want = draft[vm.name];
      api<{ compose: string; file: string; content: string }>("/cloud-init/editar", { method: "POST", body: { compose: latest.current.yaml, vm: vm.name, text: want } })
        .then((r) => {
          setErr("");
          const f = { ...latest.current.files };
          if (r.content) f[r.file] = r.content;
          else delete f[r.file];
          latest.current.onChange(r.compose, f);
        })
        .catch((e) => setErr((e as Error).message));
    },
    600,
    [draft, cur],
  );
  useEffect(() => setErr(""), [cur]);

  if (!list || !list.length) return null;
  return (
    <section className="panel panel-pad np-ci" style={{ marginTop: 16 }} aria-label={t("cloud-init das VMs")}>
      <div className="block-head" style={{ marginBottom: 8 }}>
        <h2>{t("cloud-init das VMs")}</h2>
        <span className="grow" />
        {list.length > 1 ? (
          <select className="select" style={{ width: "auto", marginRight: 8 }} value={cur} onChange={(e) => setCur(e.target.value)} aria-label={t("VM")}>
            {list.map((x) => (
              <option key={x.name} value={x.name}>
                {x.name}
              </option>
            ))}
          </select>
        ) : null}
        <div className="seg" role="group" aria-label={t("Ver")}>
          <button type="button" className="seg-btn" aria-pressed={view === "editar"} onClick={() => setView("editar")}>
            {t("Editar")}
          </button>
          <button type="button" className="seg-btn" aria-pressed={view === "resultado"} onClick={() => setView("resultado")}>
            {t("Resultado na VM")}
          </button>
        </div>
      </div>
      {!vm ? null : vm.installer ? (
        <p className="muted small">{t("{name} não usa cloud-init (instala por uma ISO, ou tem cloud-init: false).", { name: vm.name })}</p>
      ) : view === "editar" ? (
        <>
          <p className="hint" style={{ marginTop: 0 }}>
            {t("O seu cloud-config de {name}. Ele é somado ao que o Gantry gera (usuário gantry com a chave do app, pacotes, script de inicialização e rede do projeto) e roda uma vez, no primeiro boot. Fica em {file}, na pasta do projeto.", {
              name: vm.name,
              file: vm.file || "./" + vm.name + ".cloud-init.yaml",
            })}
          </p>
          {!text.trim() ? (
            <div className="np-snips">
              <button type="button" className="btn small" onClick={() => setDraft((d) => ({ ...d, [vm.name]: CI_SAMPLE }))}>
                {t("Começar com um exemplo")}
              </button>
            </div>
          ) : null}
          <div className="editor">
            <CodeEditor key={vm.name} value={text} onChange={(v) => setDraft((d) => ({ ...d, [vm.name]: v }))} label={t("cloud-init de {name}", { name: vm.name })} language="yaml" />
          </div>
          {err ? <p className="flash flash-err">{err}</p> : null}
        </>
      ) : (
        <>
          <p className="hint" style={{ marginTop: 0 }}>
            {t("O user-data completo que {name} recebe no primeiro boot. Só leitura: mude pela aba Editar ou pelos campos da VM no compose.", { name: vm.name })}
          </p>
          {vm.error ? (
            <p className="flash flash-err">{vm.error}</p>
          ) : (
            <div className="editor">
              <CodeEditor key={vm.name + ":r"} value={vm.userData || ""} onChange={() => {}} readOnly label={t("user-data de {name}", { name: vm.name })} language="yaml" />
            </div>
          )}
        </>
      )}
    </section>
  );
}
