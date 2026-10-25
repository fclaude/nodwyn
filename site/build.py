#!/usr/bin/env python3
"""Builds the website (GitHub Pages) from site/content, docs/ and the ADRs.

The guide is written for the site, in site/content. The reference is the
repository's own documentation (the manual, the protocol, the Scheme language
reference, the decision records), converted as it is, so the two never drift:
a change to a doc in docs/ is a change to the site.

    pip install markdown pygments
    python3 site/build.py            # writes site/_out
    python3 -m http.server -d site/_out

In a guide page, ![caption](shot:task) is the console's screenshot docs/images/task-*.webp,
in the theme the reader's system prefers. A link to a repository file is
rewritten to the page that file became, or to GitHub when it did not become
one.
"""

import html
import posixpath
import re
import shutil
import sys
from pathlib import Path

import markdown
from pygments.formatters import HtmlFormatter

ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / "site"
OUT = SITE / "_out"
REPO = "https://github.com/fclaude/nodwyn"

# The guide, in reading order. Each entry is (file in site/content, section).
GUIDE = [
    ("install.md", "Start here"),
    ("first-run.md", "Start here"),
    ("console.md", "The console"),
    ("tasks.md", "The console"),
    ("approvals.md", "The console"),
    ("setup.md", "The console"),
    ("automation.md", "The console"),
    ("standalone.md", "Running it"),
    ("server.md", "Running it"),
    ("terminals.md", "Running it"),
    ("keys.md", "Your keys"),
]

# The repository's own docs, as the reference: (path, section, title in the
# sidebar). A title of None is the document's own first heading.
REFERENCE = [
    ("docs/manual.md", "Manual", "Manual"),
    ("docs/browser.md", "Manual", "The console, in detail"),
    ("docs/app.md", "Manual", "The app and its window"),
    ("docs/tui.md", "Manual", "The terminal client"),
    ("docs/clients.md", "Manual", "What each client does"),
    ("docs/runs.md", "Manual", "Supervised runs"),
    ("docs/memory.md", "Manual", "Session memory"),
    ("docs/security.md", "Manual", "Security boundaries"),
    ("docs/reference.md", "Manual", "Build, run and limits"),
    ("docs/releases.md", "Manual", "Releases"),
    ("docs/protocol.md", "Specifications", "Session protocol"),
    ("docs/scheme.md", "Specifications", "The Scheme language"),
    ("SPEC.md", "Specifications", "Architecture"),
    ("plan.md", "Specifications", "Plan"),
    ("docs/friction.md", "Specifications", "Friction log"),
]


class Page:
    def __init__(self, source, out, title, section, kind):
        self.source, self.out, self.title, self.section, self.kind = source, out, title, section, kind
        self.body = ""
        self.toc = []


def slugify(value, separator="-"):
    """GitHub's heading ids, so that anchors in the docs keep working."""
    value = re.sub(r"[^\w\- ]", "", value.lower()).strip()
    return value.replace(" ", separator)


def title_of(text, fallback):
    match = re.search(r"^# (.+)$", text, re.M)
    return re.sub(r"[`*_]", "", match.group(1)).strip() if match else fallback


def collect():
    pages = {}
    for name, section in GUIDE:
        path = SITE / "content" / name
        source = path.relative_to(ROOT).as_posix()
        text = path.read_text(encoding="utf-8")
        pages[source] = Page(source, f"guide/{path.stem}.html", title_of(text, path.stem), section, "guide")
    for source, section, title in REFERENCE:
        text = (ROOT / source).read_text(encoding="utf-8")
        stem = Path(source).stem.lower()
        pages[source] = Page(source, f"reference/{stem}.html", title or title_of(text, stem), section, "reference")
    adr = sorted((ROOT / "docs/adr").glob("0*.md"))
    for path in adr:
        source = path.relative_to(ROOT).as_posix()
        text = path.read_text(encoding="utf-8")
        title = re.sub(r"^ADR (\d+)[:.]?\s*", r"\1. ", title_of(text, path.stem))
        pages[source] = Page(source, f"decisions/{path.stem[:4]}.html", title, "Decisions", "decision")
    source = "docs/adr/README.md"
    pages[source] = Page(source, "decisions/index.html", "Decision records", "Decisions", "decision")
    return pages


def rel(from_out, to_out):
    return posixpath.relpath(to_out, posixpath.dirname(from_out)) or "."


def rewrite_links(body, page, pages):
    """Points links between repository files at the pages they became."""
    here = posixpath.dirname(page.source)

    def fix(match):
        url = match.group(2)
        # A link to a page of the site itself (written as .html) stays as it is.
        if re.match(r"^([a-z][a-z0-9+.-]*:|#|/)", url, re.I) or re.search(r"\.html(#.*)?$", url):
            return match.group(0)
        path, _, anchor = url.partition("#")
        target = posixpath.normpath(posixpath.join(here, path)) if path else page.source
        if target in pages:
            new = rel(page.out, pages[target].out) + (f"#{anchor}" if anchor else "")
        else:
            kind = "tree" if (ROOT / target).is_dir() else "blob"
            new = f"{REPO}/{kind}/main/{target}" + (f"#{anchor}" if anchor else "")
        return f'{match.group(1)}"{new}"'

    return re.sub(r'(href=)"([^"]*)"', fix, body)


SHOT = re.compile(r"!\[([^\]]*)\]\(shot:([\w-]+)\)")


def shots(text, page):
    def figure(match):
        caption, name = match.groups()
        base = "../" * page.out.count("/") + "images/" + name
        picture = (
            f'<figure class="shot"><picture>'
            f'<source media="(prefers-color-scheme: dark)" srcset="{base}-dark.webp">'
            f'<img src="{base}-light.webp" alt="{html.escape(caption, quote=True)}" loading="lazy">'
            f"</picture><figcaption>{html.escape(caption)}</figcaption></figure>"
        )
        return "\n\n" + picture + "\n\n"

    return SHOT.sub(figure, text)


def render(page):
    text = (ROOT / page.source).read_text(encoding="utf-8")
    if page.kind in ("guide", "home"):
        text = shots(text, page)
    md = markdown.Markdown(
        extensions=["fenced_code", "tables", "toc", "attr_list", "sane_lists", "codehilite", "md_in_html"],
        extension_configs={
            "toc": {"slugify": slugify, "toc_depth": "2-3"},
            "codehilite": {"guess_lang": False, "css_class": "hl", "pygments_style": "default"},
        },
    )
    body = md.convert(text)
    page.toc = md.toc_tokens
    page.body = body


NAV_SECTIONS = ["Start here", "The console", "Running it", "Your keys"]


def sidebar(page, pages):
    parts = []
    guide = [p for p in pages.values() if p.kind == "guide"]
    for section in NAV_SECTIONS:
        parts.append(f"<h3>{section}</h3><ul>")
        for p in guide:
            if p.section == section:
                current = ' aria-current="page"' if p is page else ""
                parts.append(f'<li><a href="{rel(page.out, p.out)}"{current}>{html.escape(p.title)}</a></li>')
        parts.append("</ul>")
    parts.append("<h3>Reference</h3><ul>")
    for p in pages.values():
        if p.kind == "reference":
            current = ' aria-current="page"' if p is page else ""
            parts.append(f'<li><a href="{rel(page.out, p.out)}"{current}>{html.escape(p.title)}</a></li>')
    parts.append(f'<li><a href="{rel(page.out, "decisions/index.html")}">Decision records</a></li></ul>')
    return "".join(parts)


def toc_html(tokens, depth=0):
    if not tokens:
        return ""
    items = "".join(
        f'<li><a href="#{t["id"]}">{html.escape(re.sub(r"<[^>]+>", "", t["name"]))}</a>{toc_html(t["children"], depth + 1)}</li>'
        for t in tokens
    )
    return f"<ul>{items}</ul>"


TEMPLATE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{title}</title>
<meta name="description" content="{description}">
<link rel="icon" href="{root}assets/icon.svg" type="image/svg+xml">
<link rel="stylesheet" href="{root}assets/site.css">
</head>
<body class="{kind}">
<header class="top">
  <a class="brand" href="{root}index.html">nodwyn<span>.</span></a>
  <nav>
    <a href="{root}guide/install.html">Guide</a>
    <a href="{root}reference/manual.html">Reference</a>
    <a href="{root}decisions/index.html">Decisions</a>
    <a href="{repo}">GitHub</a>
  </nav>
</header>
<div class="layout">
  <details class="menu"><summary>Menu</summary><nav class="side">{sidebar}</nav></details>
  <nav class="side wide" aria-label="Pages">{sidebar}</nav>
  <main>
    <article>
{body}
    </article>
    <footer>nodwyn is Apache 2.0. <a href="{repo}/blob/main/{source}">Edit this page on GitHub</a>.</footer>
  </main>
  <nav class="toc" aria-label="On this page">{toc}</nav>
</div>
</body>
</html>
"""


def write(page, pages, home=False):
    body = rewrite_links(page.body, page, pages)
    root = "../" * page.out.count("/")
    toc = f"<h4>On this page</h4>{toc_html(page.toc)}" if page.toc and page.kind != "home" else ""
    text = TEMPLATE.format(
        title=html.escape(f"{page.title} · nodwyn" if not home else "nodwyn"),
        description=html.escape("A coding agent that keeps your keys out of the model's hands."),
        root=root,
        kind=page.kind,
        repo=REPO,
        sidebar=sidebar(page, pages),
        body=body,
        source=page.source,
        toc=toc,
    )
    target = OUT / page.out
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(text, encoding="utf-8")


def css():
    light = HtmlFormatter(style="default", cssclass="hl").get_style_defs(".hl")
    dark = HtmlFormatter(style="native", cssclass="hl").get_style_defs(".hl")
    base = (SITE / "static/site.css").read_text(encoding="utf-8")
    return f"{base}\n/* Code colours: pygments' default (light) and native (dark). */\n{light}\n@media (prefers-color-scheme: dark) {{\n{dark}\n}}\n"


def check_links():
    """Fails the build on a link that goes nowhere: a page, an image, an anchor
    in a page of the site, or a file of the repository that isn't there."""
    pages = {p.relative_to(OUT).as_posix(): p for p in OUT.rglob("*.html")}
    ids = {rel: set(re.findall(r'\bid="([^"]+)"', p.read_text(encoding="utf-8"))) for rel, p in pages.items()}
    bad = []
    for rel, p in pages.items():
        for url in re.findall(r'(?:href|src|srcset)="([^"]+)"', p.read_text(encoding="utf-8")):
            if url.startswith(f"{REPO}/blob/main/") or url.startswith(f"{REPO}/tree/main/"):
                if not (ROOT / url.split("/main/", 1)[1].split("#")[0]).exists():
                    bad.append(f"{rel}: {url} is not in the repository")
                continue
            if re.match(r"^(https?:|mailto:|data:)", url):
                continue
            path, _, fragment = url.partition("#")
            target = posixpath.normpath(posixpath.join(posixpath.dirname(rel), path)) if path else rel
            if not (OUT / target).exists():
                bad.append(f"{rel}: {url} is missing")
            elif fragment and target.endswith(".html") and fragment not in ids.get(target, set()):
                bad.append(f"{rel}: {url} has no such anchor")
    if bad:
        sys.exit("broken links:\n" + "\n".join(sorted(set(bad))))


def main():
    if OUT.exists():
        shutil.rmtree(OUT)
    OUT.mkdir(parents=True)
    pages = collect()
    for page in pages.values():
        render(page)
    for page in pages.values():
        write(page, pages)
    # The landing page is its own file, with the hero and the three ways in.
    home = Page("site/content/index.md", "index.html", "nodwyn", "", "home")
    pages["site/content/index.md"] = home
    render(home)
    write(home, pages, home=True)
    assets = OUT / "assets"
    assets.mkdir()
    (assets / "site.css").write_text(css(), encoding="utf-8")
    fonts = ROOT / "internal/frontend/web/assets/fonts"
    for font in ("newsreader.woff2", "instrument-sans.woff2", "plex-mono-400.woff2", "plex-mono-500.woff2"):
        shutil.copy(fonts / font, assets / font)
    shutil.copy(ROOT / "internal/frontend/web/assets/icons/icon.svg", assets / "icon.svg")
    shutil.copytree(ROOT / "docs/images", OUT / "images")
    (OUT / ".nojekyll").write_text("")
    check_links()
    print(f"built {len(pages)} pages in {OUT.relative_to(ROOT)}")


if __name__ == "__main__":
    sys.exit(main())
