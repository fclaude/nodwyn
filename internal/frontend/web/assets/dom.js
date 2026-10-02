// DOM helpers. Every string reaches the page as a text node or an attribute
// value, never as markup: session content is untrusted (browser.md).

export const $ = (id) => document.getElementById(id);

// h(tag, props, ...children): props may hold class, attributes, dataset,
// and on* handlers; children are nodes, strings (as text), or arrays.
export function h(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === "class") node.className = value;
    // CSSOM, not the style attribute: the page's CSP refuses inline styles.
    else if (key === "style") node.style.cssText = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key.startsWith("on") && typeof value === "function")
      node.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key in node && typeof value !== "string") node[key] = value;
    else node.setAttribute(key, value === true ? "" : String(value));
  }
  append(node, children);
  return node;
}
function append(node, children) {
  for (const child of children) {
    if (child === undefined || child === null || child === false) continue;
    if (Array.isArray(child)) append(node, child);
    else if (child instanceof Node) node.append(child);
    else node.append(document.createTextNode(String(child)));
  }
}

// Icons are the canvas's 24px stroke drawings, built as SVG elements.
const paths = {
  plus: ["M12 5v14M5 12h14"],
  search: ["M20 20l-3.5-3.5"],
  folder: ["M3 7.5A1.5 1.5 0 0 1 4.5 6H9l2 2h8.5A1.5 1.5 0 0 1 21 9.5v8a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z"],
  plug: ["M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4"],
  box: ["M12 3l8 4.5v9L12 21l-8-4.5v-9z", "M4 7.5l8 4.5 8-4.5M12 12v9"],
  sliders: ["M4 7h9M17 7h3M4 17h3M11 17h9"],
  clip: ["M20 11.5l-8.2 8.2a5 5 0 0 1-7.1-7.1l8.5-8.5a3.3 3.3 0 0 1 4.7 4.7l-8.5 8.5a1.7 1.7 0 0 1-2.4-2.4l7.8-7.8"],
  up: ["M12 19V5M6 11l6-6 6 6"],
  down: ["M6 9l6 6 6-6"],
  right: ["M9 6l6 6-6 6"],
  terminal: ["M4 17l6-5-6-5M12 19h8"],
  check: ["M5 12.5l4.5 4.5L19 7.5"],
  cross: ["M6 6l12 12M18 6L6 18"],
  file: ["M14 3H6.5A1.5 1.5 0 0 0 5 4.5v15A1.5 1.5 0 0 0 6.5 21h11a1.5 1.5 0 0 0 1.5-1.5V8z", "M14 3v5h5"],
  pause: ["M9 5v14M15 5v14"],
  menu: ["M4 7h16M4 12h16M4 17h16"],
  commit: ["M3 12h5.5M15.5 12H21"],
  key: ["M14.5 9.5a4.5 4.5 0 1 1-1.3-3.2M13.2 6.3L20 13l-2 2-1.5-1.5L15 15l-1.5-1.5"],
  sun: ["M12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6l1.4 1.4M17 17l1.4 1.4M5.6 18.4L7 17M17 7l1.4-1.4"],
  more: ["M5 12h.01M12 12h.01M19 12h.01"],
  back: ["M15 6l-6 6 6 6"],
  image: ["M3.5 17l5.5-5.5 4 4 2.5-2.5 5 5"],
};
const circles = { search: [[11, 11, 7]], sliders: [[15, 7, 2], [9, 17, 2]], commit: [[12, 12, 3.5]], sun: [[12, 12, 4]], image: [[8.5, 9.5, 1.5]] };
const rects = { stop: [[6, 6, 12, 12, 1.5]], image: [[3, 5, 18, 14, 1.5]] };

export function icon(name, size = 16, width = 1.8) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  for (const [k, v] of Object.entries({ width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", "stroke-width": width, "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true", class: "icon" }))
    svg.setAttribute(k, v);
  for (const d of paths[name] || []) {
    const p = document.createElementNS(ns, "path");
    p.setAttribute("d", d);
    svg.append(p);
  }
  for (const [cx, cy, r] of circles[name] || []) {
    const c = document.createElementNS(ns, "circle");
    c.setAttribute("cx", cx);
    c.setAttribute("cy", cy);
    c.setAttribute("r", r);
    svg.append(c);
  }
  for (const [x, y, w, hh, rx] of rects[name] || []) {
    const r = document.createElementNS(ns, "rect");
    for (const [k, v] of Object.entries({ x, y, width: w, height: hh, rx })) r.setAttribute(k, v);
    svg.append(r);
  }
  return svg;
}

export function repoName(path) {
  if (!path) return "—";
  const parts = path.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || path;
}

// A path as a person reads it: under the home directory it starts with ~.
export function shortPath(path, home) {
  if (home && path && path.startsWith(home + "/")) return "~" + path.slice(home.length);
  return path || "";
}

export function ago(when) {
  const then = new Date(when).getTime();
  if (!then) return "";
  const s = Math.max(0, (Date.now() - then) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  if (s < 2 * 86400) return "yesterday";
  return new Date(when).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function clock(when) {
  const t = new Date(when);
  if (!t.getTime()) return "";
  return t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function minutes(ms) {
  const m = ms / 60000;
  if (m < 1) return `${Math.max(0, Math.round(ms / 1000))} s`;
  if (m < 90) return `${Math.round(m)} min`;
  return `${(m / 60).toFixed(1)} h`;
}

export function oneLine(value, limit = 140) {
  const flat = String(value ?? "").replace(/\s+/g, " ").trim();
  return flat.length > limit ? flat.slice(0, limit - 1) + "…" : flat;
}

export function sizeOf(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / 1048576).toFixed(1)} MB`;
}

export function basename(path) {
  const parts = String(path || "").split("/");
  return parts[parts.length - 1] || path;
}

// diffStats counts a unified diff's added and removed lines.
export function diffStats(detail) {
  let plus = 0,
    minus = 0;
  for (const line of String(detail || "").split("\n")) {
    if (line.startsWith("+") && !line.startsWith("+++")) plus++;
    else if (line.startsWith("-") && !line.startsWith("---")) minus++;
  }
  return { plus, minus };
}

let toastTimer = 0;
export function toast(message) {
  let node = $("toast");
  if (!node) {
    node = h("div", { id: "toast", class: "toast", role: "status" });
    document.body.append(node);
  }
  node.textContent = message;
  node.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (node.hidden = true), 5000);
}

// toolLabel shows an MCP tool (server__tool) as server/tool.
export function toolLabel(name) {
  const i = String(name || "").indexOf("__");
  return i > 0 ? `${name.slice(0, i)}/${name.slice(i + 2)}` : name;
}
// isMCP reports whether a tool comes from an MCP server.
export function isMCP(name) {
  return String(name || "").indexOf("__") > 0;
}
