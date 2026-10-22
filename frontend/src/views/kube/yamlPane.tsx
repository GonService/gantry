import { useEffect, useRef, useState } from "react";
import { api, enc } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { t, tn } from "@/lib/i18n";
import { confirm } from "@/components/dialog";
import { CodeEditor, type FieldProblem, type Problem } from "@/components/editor";
import type { EditorHandle } from "@/components/monaco";
import { closePane, openPane, setCloseGuard } from "@/shell/dock";
import { kubeNS, loadView } from "./store";
import type { Cluster, Row } from "./types";

// The YAML of a cluster object in the bottom panel, as in Lens: next to the
// logs and terminals, with the list still in view. While it is edited the
// cluster checks it (dry run), and the problems show on their lines.

const kapi = <T,>(c: Cluster, path: string, opts: { method?: string; body?: unknown } = {}) => api<T>("/clusters/" + enc(c.id) + path, opts);

const validate = (c: Cluster, body: { yaml: string; ns: string; kind?: string; name?: string }) =>
  kapi<{ problems: FieldProblem[] }>(c, "/validar", { method: "POST", body }).then((r) => r.problems ?? []);

function ProblemsBadge({ problems, h, checks }: { problems: Problem[] | null | undefined; h: React.MutableRefObject<EditorHandle | null>; checks: boolean }) {
  if (problems === undefined) return null;
  if (problems === null) return <span className="yaml-check muted">{t("verificando no cluster…")}</span>;
  if (!problems.length) return <span className="yaml-check ok">{checks ? t("✓ válido no cluster") : t("✓ YAML válido")}</span>;
  return (
    <button type="button" className="yaml-check bad" title={t("Ir para o próximo problema (F8)")} onClick={() => h.current?.nextProblem()}>
      {tn(problems.length, "{n} problema", "{n} problemas")}
    </button>
  );
}

function EditPane({ c, kind, ns, name, ro, paneKey, onSaved }: { c: Cluster; kind: string; ns: string; name: string; ro: boolean; paneKey: string; onSaved?: () => void }) {
  const path = "/r/" + kind + "/" + enc(ns || "-") + "/" + enc(name);
  const [text, setText] = useState("");
  const [base, setBase] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<Problem[] | null | undefined>(undefined);
  const h = useRef<EditorHandle | null>(null);
  const dirty = loaded && text !== base;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const load = async () => {
    if (dirtyRef.current && !(await confirm(t("Recarregar {name}?", { name }), t("As alterações não salvas serão perdidas."), t("Recarregar"), true))) return;
    setErr("");
    try {
      const x = await kapi<{ yaml: string }>(c, path);
      setText(x.yaml);
      setBase(x.yaml);
      setLoaded(true);
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  useEffect(() => {
    void load();
    setCloseGuard(paneKey, () => !dirtyRef.current || confirm(t("Fechar {name}?", { name }), t("As alterações não salvas serão perdidas."), t("Fechar"), true));
    return () => setCloseGuard(paneKey, null);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const save = async () => {
    if (ro || !dirty || busy) return;
    setErr("");
    setBusy(true);
    try {
      await kapi(c, path, { method: "PUT", body: { yaml: text } });
      gantry.toast(t("{name} salvo", { name }), "ok");
      // What the cluster keeps (defaults, a new resourceVersion) comes back.
      const x = await kapi<{ yaml: string }>(c, path);
      setText(x.yaml);
      setBase(x.yaml);
      void loadView(c.id, kind);
      onSaved?.();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="pane-bar">
        <span className="mono pane-title">{kind + " / " + (ns ? ns + " / " : "") + name}</span>
        <span className="muted small">{!loaded ? (err ? t("erro") : t("carregando…")) : ro ? t("só leitura") : dirty ? t("alterações não salvas") : t("salvo")}</span>
        <ProblemsBadge problems={problems} h={h} checks={!ro} />
        <span className="grow" />
        {err ? (
          <span className="pane-err" title={err}>
            {err}
          </span>
        ) : null}
        <button className="btn small ghost" type="button" onClick={() => void load()}>
          {t("Recarregar")}
        </button>
        {ro ? null : (
          <button className="btn small primary" type="button" disabled={!dirty || busy} title={t("Substitui o objeto no cluster (como kubectl replace). Cmd/Ctrl+S")} onClick={() => void save()}>
            {busy ? t("Salvando…") : t("Salvar")}
          </button>
        )}
      </div>
      <CodeEditor
        value={text}
        onChange={setText}
        readOnly={ro || !loaded}
        label={"YAML " + name}
        language="yaml"
        onSave={() => void save()}
        lint={ro ? undefined : (y) => validate(c, { yaml: y, ns: ns || "-", kind, name })}
        onProblems={setProblems}
        handle={h}
        fill
      />
    </>
  );
}

const SAMPLE = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: exemplo\ndata:\n  chave: valor\n";

function ApplyPane({ c, kind, paneKey }: { c: Cluster; kind: string; paneKey: string }) {
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<Problem[] | null | undefined>(undefined);
  const h = useRef<EditorHandle | null>(null);
  const textRef = useRef(text);
  textRef.current = text;
  useEffect(() => {
    setCloseGuard(paneKey, () => !textRef.current.trim() || confirm(t("Fechar sem aplicar?"), t("O YAML escrito aqui será perdido."), t("Fechar"), true));
    return () => setCloseGuard(paneKey, null);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const apply = async () => {
    if (!text.trim() || busy) return;
    setErr("");
    setBusy(true);
    try {
      const r = await kapi<{ kind: string; name: string; error?: string }[]>(c, "/aplicar", { method: "POST", body: { yaml: text, ns: kubeNS(c.id) } });
      const bad = r.filter((x) => x.error);
      if (bad.length) throw new Error(bad.map((x) => x.kind + "/" + x.name + ": " + x.error).join(" · "));
      gantry.toast(tn(r.length, "{n} objeto aplicado", "{n} objetos aplicados"), "ok");
      void loadView(c.id, kind);
      closePane(paneKey, true);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="pane-bar">
        <span className="muted small">{t("Um ou mais documentos (separados por ---), aplicados como kubectl apply --server-side. Sem namespace, vai para {ns}.", { ns: kubeNS(c.id) || "default" })}</span>
        <ProblemsBadge problems={text.trim() ? problems : undefined} h={h} checks />
        <span className="grow" />
        {err ? (
          <span className="pane-err" title={err}>
            {err}
          </span>
        ) : null}
        <button className="btn small primary" type="button" disabled={!text.trim() || busy} onClick={() => void apply()}>
          {busy ? t("Aplicando…") : t("Aplicar")}
        </button>
      </div>
      <CodeEditor
        value={text}
        onChange={setText}
        label="YAML"
        language="yaml"
        placeholder={SAMPLE}
        onSave={() => void apply()}
        lint={(y) => validate(c, { yaml: y, ns: kubeNS(c.id) })}
        onProblems={setProblems}
        handle={h}
        fill
      />
    </>
  );
}

// yamlEditor opens the YAML of an object in the panel (one tab per object).
export function yamlEditor(c: Cluster, kind: string, r: Row, noUpdate = false, onSaved?: () => void) {
  const ro = c.mode === "leitura" || noUpdate;
  const ns = r.namespace || "";
  const key = "yaml:" + c.id + ":" + kind + ":" + ns + "/" + r.name;
  openPane(key, "yaml", r.name, () => <EditPane c={c} kind={kind} ns={ns} name={r.name} ro={ro} paneKey={key} onSaved={onSaved} />);
}

// applyEditor: a new YAML to apply to the cluster, in the panel.
export function applyEditor(c: Cluster, kind: string) {
  const key = "apply:" + c.id + ":" + Date.now();
  openPane(key, "yaml", t("Aplicar em {name}", { name: c.name }), () => <ApplyPane c={c} kind={kind} paneKey={key} />);
}
