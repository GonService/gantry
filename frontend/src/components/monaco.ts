// The code editor of Gantry: Monaco, the editor of VS Code (and of Lens).
// It loads only when an editor opens (a chunk of its own), with what the
// files of a project and the objects of a cluster need: YAML and the
// languages around a compose (Dockerfile, .env, shell, nginx-like configs),
// find and replace, folding, the minimap and multiple cursors.

import * as monaco from "monaco-editor/esm/vs/editor/editor.api";
import "monaco-editor/esm/vs/editor/editor.all";
import "monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution";
import "monaco-editor/esm/vs/basic-languages/dockerfile/dockerfile.contribution";
import "monaco-editor/esm/vs/basic-languages/shell/shell.contribution";
import "monaco-editor/esm/vs/basic-languages/ini/ini.contribution";
import "monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution";
import "monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution";
import "monaco-editor/esm/vs/basic-languages/python/python.contribution";
import "monaco-editor/esm/vs/basic-languages/sql/sql.contribution";
import "monaco-editor/esm/vs/basic-languages/xml/xml.contribution";
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import { LineCounter, isMap, isSeq, isScalar, parseAllDocuments, type Node as YNode } from "yaml";

// The worker computes in the background (links, word highlights); when a
// window refuses workers, Monaco does the same work on the page.
window.MonacoEnvironment = { getWorker: () => new EditorWorker() };

const css = (v: string, fallback: string) => getComputedStyle(document.documentElement).getPropertyValue(v).trim() || fallback;

function isDark(): boolean {
  const t = document.documentElement.getAttribute("data-theme");
  return t ? t === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
}

// Gantry's colors on top of VS Code's themes.
function defineThemes() {
  const hex = (c: string, fb: string) => (/^#[0-9a-f]{6}$/i.test(c) ? c : fb);
  monaco.editor.defineTheme("gantry-light", {
    base: "vs",
    inherit: true,
    rules: [],
    colors: {
      "editor.background": hex(css("--surface", "#ffffff"), "#ffffff"),
      "editorGutter.background": hex(css("--surface", "#ffffff"), "#ffffff"),
    },
  });
  monaco.editor.defineTheme("gantry-dark", {
    base: "vs-dark",
    inherit: true,
    rules: [],
    colors: {
      "editor.background": hex(css("--surface", "#1b1e24"), "#1b1e24"),
      "editorGutter.background": hex(css("--surface", "#1b1e24"), "#1b1e24"),
    },
  });
}

let themed = false;
function applyTheme() {
  defineThemes();
  monaco.editor.setTheme(isDark() ? "gantry-dark" : "gantry-light");
}

function watchTheme() {
  if (themed) return;
  themed = true;
  new MutationObserver(applyTheme).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTheme);
}

// The language of a file by its name.
export function languageOf(name: string): string {
  const n = name.toLowerCase().split("/").pop() || "";
  if (/\.ya?ml$/.test(n)) return "yaml";
  if (n === "dockerfile" || n.startsWith("dockerfile.") || n.endsWith(".dockerfile") || n === "containerfile") return "dockerfile";
  if (n === ".env" || n.startsWith(".env.") || n.endsWith(".env") || /\.(ini|conf|cfg|toml|properties)$/.test(n)) return "ini";
  if (/\.(sh|bash|zsh)$/.test(n)) return "shell";
  if (/\.(md|markdown)$/.test(n)) return "markdown";
  if (/\.(json|js|mjs|cjs|ts)$/.test(n)) return "javascript";
  if (/\.py$/.test(n)) return "python";
  if (/\.sql$/.test(n)) return "sql";
  if (/\.(xml|html?|svg)$/.test(n)) return "xml";
  return "plaintext";
}

// A problem shown in the editor: a red (or yellow) line under it, in the
// ruler and with F8 to go from one to the next.
export type Problem = { line: number; col?: number; endLine?: number; endCol?: number; message: string; warning?: boolean };

export type EditorHandle = {
  setProblems(list: Problem[]): void;
  nextProblem(): void;
  getValue(): string;
  setValue(v: string): void;
  setReadOnly(ro: boolean): void;
  setLanguage(lang: string): void;
  focus(): void;
  dispose(): void;
};

export function create(
  el: HTMLElement,
  o: { value: string; language: string; readOnly?: boolean; label?: string; onChange: (v: string) => void; onSave?: () => void },
): EditorHandle {
  watchTheme();
  applyTheme();
  const model = monaco.editor.createModel(o.value, o.language);
  model.updateOptions({ tabSize: 2, insertSpaces: true });
  const ed = monaco.editor.create(el, {
    model,
    readOnly: !!o.readOnly,
    ariaLabel: o.label,
    automaticLayout: true,
    fontFamily: css("--mono", "ui-monospace, SFMono-Regular, Menlo, monospace"),
    fontSize: 13,
    lineHeight: 20,
    tabSize: 2,
    minimap: { enabled: true },
    scrollBeyondLastLine: false,
    renderWhitespace: "boundary",
    folding: true,
    showFoldingControls: "always",
    wordWrap: "off",
    fixedOverflowWidgets: true,
    padding: { top: 8 },
    contextmenu: true,
    find: { addExtraSpaceOnTop: false, seedSearchStringFromSelection: "selection" },
    guides: { indentation: true },
  });
  let quiet = false;
  model.onDidChangeContent(() => {
    if (!quiet) o.onChange(model.getValue());
  });
  // Cmd/Ctrl+S saves, as in the rest of Gantry.
  ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => o.onSave?.());
  return {
    getValue: () => model.getValue(),
    setValue: (v) => {
      if (v === model.getValue()) return;
      quiet = true;
      // As an edit (not setValue): Cmd+Z still works after a reload.
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text: v }], () => null);
      model.pushStackElement();
      quiet = false;
    },
    setProblems: (list) =>
      monaco.editor.setModelMarkers(
        model,
        "gantry",
        list.map((p) => ({
          startLineNumber: p.line,
          startColumn: p.col ?? 1,
          endLineNumber: p.endLine ?? p.line,
          endColumn: p.endCol ?? model.getLineMaxColumn(Math.min(p.endLine ?? p.line, model.getLineCount())),
          message: p.message,
          severity: p.warning ? monaco.MarkerSeverity.Warning : monaco.MarkerSeverity.Error,
        })),
      ),
    nextProblem: () => {
      ed.focus();
      void ed.getAction("editor.action.marker.next")?.run();
    },
    setReadOnly: (ro) => ed.updateOptions({ readOnly: ro }),
    setLanguage: (lang) => monaco.editor.setModelLanguage(model, lang),
    focus: () => ed.focus(),
    dispose: () => {
      ed.dispose();
      model.dispose();
    },
  };
}

/* YAML: syntax as you type, and where a field of the cluster's answer is */

// yamlSyntax: the errors of the YAML itself (indentation, a tab, a quote
// left open…), with their place.
export function yamlSyntax(text: string): Problem[] {
  const lc = new LineCounter();
  const out: Problem[] = [];
  for (const d of parseAllDocuments(text, { lineCounter: lc, prettyErrors: false })) {
    if (!("errors" in d)) continue;
    for (const e of [...d.errors, ...d.warnings]) {
      const a = lc.linePos(e.pos[0]);
      const b = lc.linePos(Math.max(e.pos[1], e.pos[0] + 1));
      out.push({ line: a.line, col: a.col, endLine: b.line, endCol: b.col, message: e.message.split("\n")[0], warning: e.name === "YAMLWarning" });
    }
  }
  return out;
}

// fieldLine: the line of a field (spec.template.spec.containers[0].image)
// in the n-th document; a field that is missing points to its parent.
export function fieldLine(text: string, doc: number, field?: string): { line: number; col: number; endCol?: number } {
  const lc = new LineCounter();
  const docs = parseAllDocuments(text, { lineCounter: lc });
  const d = Array.isArray(docs) ? docs[doc] : undefined;
  if (!d) return { line: 1, col: 1 };
  let at = d.contents?.range?.[0] ?? d.range[0];
  let endAt: number | undefined;
  let node: YNode | null = d.contents as YNode | null;
  const path = (field || "")
    .replace(/^\./, "")
    .split(/\.|\[(\d+)\]/)
    .filter((x) => x !== undefined && x !== "");
  for (const seg of path) {
    if (isMap(node)) {
      const pair = node.items.find((it) => isScalar(it.key) && String(it.key.value) === seg);
      if (!pair) break;
      const k = pair.key as YNode;
      if (k.range) {
        at = k.range[0];
        endAt = k.range[1];
      }
      node = pair.value as YNode | null;
    } else if (isSeq(node) && /^\d+$/.test(seg)) {
      const it = node.items[Number(seg)] as YNode | undefined;
      if (!it) break;
      if (it.range) {
        at = it.range[0];
        endAt = undefined;
      }
      node = it;
    } else break;
  }
  const p = lc.linePos(at);
  return { line: p.line, col: p.col, endCol: endAt !== undefined ? lc.linePos(endAt).col : undefined };
}
