// A small Markdown reader for model replies. It builds DOM nodes from a
// fixed subset (headings, paragraphs, lists, quotes, fences, tables, inline
// code, emphasis, links) and never parses HTML: a reply that contains markup
// shows the markup as text. Links open only for http(s) URLs.
import { h } from "./dom.js";

export function markdown(source) {
  const root = h("div", { class: "prose" });
  const lines = String(source ?? "").replace(/\r\n?/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const fence = /^\s*(`{3,}|~{3,})([^`]*)$/.exec(line);
    if (fence) {
      const body = [];
      const close = new RegExp("^\\s*" + fence[1] + "\\s*$");
      i++;
      while (i < lines.length && !close.test(lines[i])) body.push(lines[i++]);
      i++;
      root.append(h("pre", {}, h("code", {}, body.join("\n"))));
      continue;
    }
    const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (heading) {
      const level = Math.min(4, heading[1].length);
      root.append(h(`h${level}`, {}, inline(heading[2])));
      i++;
      continue;
    }
    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      root.append(h("hr"));
      i++;
      continue;
    }
    if (/^\s*>/.test(line)) {
      const body = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) body.push(lines[i++].replace(/^\s*>\s?/, ""));
      const quote = markdown(body.join("\n"));
      quote.className = "";
      root.append(h("blockquote", {}, [...quote.childNodes]));
      continue;
    }
    if (isTableStart(lines, i)) {
      const [table, next] = readTable(lines, i);
      root.append(table);
      i = next;
      continue;
    }
    if (listItem(line)) {
      const [list, next] = readList(lines, i, indent(line));
      root.append(list);
      i = next;
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() && !startsBlock(lines, i)) para.push(lines[i++].trim());
    if (!para.length) para.push(lines[i++].trim());
    const p = h("p");
    para.forEach((text, n) => {
      if (n) p.append(h("br"));
      p.append(...inline(text));
    });
    root.append(p);
  }
  return root;
}

function startsBlock(lines, i) {
  const line = lines[i];
  return /^\s*(`{3,}|~{3,})/.test(line) || /^#{1,6}\s/.test(line) || /^\s*>/.test(line) || !!listItem(line) || isTableStart(lines, i);
}
function indent(line) {
  return /^\s*/.exec(line)[0].replace(/\t/g, "    ").length;
}
function listItem(line) {
  return /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/.exec(line);
}
function readList(lines, i, base) {
  const first = listItem(lines[i]);
  const ordered = /\d/.test(first[2]);
  const list = h(ordered ? "ol" : "ul");
  if (ordered && Number.parseInt(first[2], 10) !== 1) list.setAttribute("start", Number.parseInt(first[2], 10));
  let item = null;
  while (i < lines.length) {
    const line = lines[i];
    const m = listItem(line);
    if (m && indent(line) === base) {
      item = h("li", {}, taskBox(m[3]));
      list.append(item);
      i++;
    } else if (m && indent(line) > base && item) {
      const [sub, next] = readList(lines, i, indent(line));
      item.append(sub);
      i = next;
    } else if (line.trim() && indent(line) > base && item) {
      item.append(" ", ...inline(line.trim()));
      i++;
    } else break;
  }
  return [list, i];
}
function taskBox(text) {
  const m = /^\[([ xX])\]\s+(.*)$/.exec(text);
  if (!m) return inline(text);
  return [m[1] === " " ? "☐ " : "☑ ", inline(m[2])];
}
function cells(line) {
  return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
}
function isTableStart(lines, i) {
  return i + 1 < lines.length && lines[i].includes("|") && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(lines[i + 1]);
}
function readTable(lines, i) {
  const head = cells(lines[i]);
  i += 2;
  const table = h("table", {}, h("thead", {}, h("tr", {}, head.map((c) => h("th", {}, inline(c))))));
  const body = h("tbody");
  while (i < lines.length && lines[i].includes("|") && lines[i].trim()) {
    body.append(h("tr", {}, cells(lines[i]).map((c) => h("td", {}, inline(c)))));
    i++;
  }
  table.append(body);
  return [table, i];
}

// inline handles code spans first (their contents are literal), then links,
// then strong and emphasis.
export function inline(text) {
  const out = [];
  const pattern = /(`+)([\s\S]*?[^`])\1(?!`)|\[([^\]]+)\]\(([^)\s]+)\)|\*\*([^*]+)\*\*|__([^_]+)__|\*([^*\s][^*]*)\*|(?<![\w])_([^_\s][^_]*)_(?![\w])|(https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g;
  let last = 0;
  for (const m of text.matchAll(pattern)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1]) out.push(h("code", {}, m[2]));
    else if (m[3]) out.push(link(m[4], inline(m[3])));
    else if (m[5] || m[6]) out.push(h("strong", {}, inline(m[5] || m[6])));
    else if (m[7] || m[8]) out.push(h("em", {}, inline(m[7] || m[8])));
    else if (m[9]) out.push(link(m[9], m[9]));
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
function link(url, label) {
  let safe = null;
  try {
    const parsed = new URL(url);
    if (parsed.protocol === "http:" || parsed.protocol === "https:") safe = parsed.href;
  } catch {
    safe = null;
  }
  if (!safe) return h("span", {}, label);
  return h("a", { href: safe, target: "_blank", rel: "noopener noreferrer" }, label);
}
