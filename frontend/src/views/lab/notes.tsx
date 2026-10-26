// The notes on the drawing of a lab (x-notas in the compose): text in a
// small Markdown (lines, **bold**, *italic*, `code`, # titles, - lists,
// tables and images of the project folder), where they are and their size.
// Notes imported from PNETLab or EVE-NG come here too.
import { apiURL, enc } from "@/lib/api";

export type Nota = { id: string; text: string; pos?: [number, number]; size?: [number, number]; cor?: string };

export const NOTE_W = 220;
export const NOTE_H = 110;
export const NOTE_CORES = ["", "amarela", "azul", "verde", "rosa", "cinza"];

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

// inline: the marks inside a line, on text already escaped.
function inline(s: string): string {
  return s
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>")
    .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<i>$2</i>")
    .replace(/(^|\s)_([^_\s][^_]*)_(?=\s|$|[.,;:!?])/g, "$1<i>$2</i>");
}

// imgPath: an image the note may show: a file of the project folder, by a
// relative path that does not leave it.
const imgPath = (p: string) => !!p && !/^[a-z]+:/i.test(p) && !p.startsWith("/") && !p.split("/").includes("..");

// noteHTML makes the HTML of a note. Everything typed is escaped first;
// only the marks above become tags, and images come from the project
// through the API.
export function noteHTML(text: string, host: string, lab: string): string {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const out: string[] = [];
  let list = false;
  const endList = () => {
    if (list) out.push("</ul>");
    list = false;
  };
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i];
    const l = raw.trim();
    if (l.startsWith("|")) {
      endList();
      const rows: string[] = [];
      while (i < lines.length && lines[i].trim().startsWith("|")) {
        const row = lines[i].trim();
        if (!/^\|[\s:|-]+$/.test(row)) {
          const cells = row.replace(/^\|/, "").replace(/\|$/, "").split("|");
          rows.push("<tr>" + cells.map((c) => "<td>" + inline(esc(c.trim())) + "</td>").join("") + "</tr>");
        }
        i++;
      }
      i--;
      out.push("<table>" + rows.join("") + "</table>");
      continue;
    }
    const img = /^!\[([^\]]*)\]\(([^)\s]+)\)$/.exec(l);
    if (img) {
      endList();
      if (imgPath(img[2])) out.push('<img alt="' + esc(img[1]) + '" src="' + esc(apiURL("/hosts/" + enc(host) + "/labs/" + enc(lab) + "/nota-img?path=" + encodeURIComponent(img[2]), true)) + '">');
      continue;
    }
    const h = /^(#{1,4})\s+(.*)$/.exec(l);
    if (h) {
      endList();
      out.push('<div class="h' + h[1].length + '">' + inline(esc(h[2])) + "</div>");
      continue;
    }
    const li = /^[-*]\s+(.*)$/.exec(l);
    if (li) {
      if (!list) out.push("<ul>");
      list = true;
      out.push("<li>" + inline(esc(li[1])) + "</li>");
      continue;
    }
    endList();
    out.push(l ? "<div>" + inline(esc(raw)) + "</div>" : '<div class="gap"></div>');
  }
  endList();
  return out.join("");
}

// newNoteID: a name not yet used by a note.
export function newNoteID(notas: Nota[]): string {
  let i = notas.length + 1;
  while (notas.some((n) => n.id === "nota-" + i)) i++;
  return "nota-" + i;
}
