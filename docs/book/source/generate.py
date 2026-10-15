#!/usr/bin/env python3
"""Generate the shared books' complete source inventory, or check it for drift."""

import argparse
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[3]
OUTPUT = Path(__file__).with_name("catalogue.tex")

# These introductions give each package a reading entry point before its listings.
GROUPS = [
    ("cmd", "Starting the binaries",
     r"Begin with \src{cmd/nodwyn/main.go} and its command dispatch. The mode files assemble options and hand interaction to a frontend. The schema command emits the RPC contract; the egress command starts the proxy used by contained servers. Platform-specific app files have build constraints: read those before assuming all implementations run in one binary."),
    ("internal/scheme", "The Scheme interpreter",
     r"Read value, reader, environment and interpreter first; then follow primitive application in \src{internal/scheme/prim.go}. The forms files implement evaluation rules, while builtins implement callable operations. JSON conversion, origin and taint cross the host boundary, so they are part of the security contract as well as language conveniences. Tests: \file{internal/scheme/scheme\_test.go}, \file{taint\_test.go} and \file{json\_test.go}."),
    ("internal/core", "Assembling and owning a session",
     r"Start with \src{internal/core/session.go} and \src{internal/core/construct.go}; return here whenever a subsystem needs a session-owned capability. The feature files register primitives, connect policy to state and record events. Read resume, reload and close paths with the normal path: persistence does not mean replaying side effects. The adjacent test files exercise these features through constructed sessions and fake providers."),
    ("internal/loop", "The host beneath the Scheme loop",
     r"Read \src{internal/loop/loop.go} for installation, freezing and turn entry; \src{internal/loop/prims.go} for Scheme-facing contracts; model and tools for actual requests and calls; turn and progress for accounting and stops. Pair these with the complete Scheme listings earlier in the catalogue. The strategy decides when to continue, but cannot invent a provider's tool call or escape host budgets."),
    ("internal/prim", "Operations on the host",
     r"Read registration and checker assembly in \src{internal/prim/prim.go}, then choose one operation. A primitive's declaration and handler belong together: changing its arguments without changing their roles can alter what the guard sees. Filesystem reads and edits live in fs; subprocess lifetime spans shell and procs; policy and versions have their own persistence rules. The package's tests and \file{test/exfil/} cover different levels of the same boundary."),
    ("internal/guard", "Permission at the single exit",
     r"Read \src{internal/guard/guard.go} to see how decisions combine and when a confirmer is consulted. Then follow path and command checks, standing approvals, provenance, credential bindings and text-secret detection. A checker reads declared roles rather than relying on a tool's name. Study denial tests before changing ordering: an ordinary approval must not erase a security refusal."),
    ("internal/binding", "Credentials bound to destinations",
     r"Start with the table and matching rules in \src{internal/binding/binding.go}, then append and live reload. This package describes authorised uses; it does not store secret values. Pair live policy changes with the guard's binding reload checker and the session's binding primitives."),
    ("internal/keychain", "Secret storage and acquisition",
     r"Read store for the persisted representation and access API, acquire for obtaining credentials, and oauth for refresh. Follow callers in core to see where values become handles and where actual credentials enter a request. Tests use temporary stores and fake services; copying a production credential into a test defeats the separation this code exists to maintain."),
    ("internal/detect/l1", "Recognising secret shapes",
     r"This detector identifies recognised credential formats in text. Read its result types before the individual patterns, then see how \src{internal/guard/textsecret.go} and redaction consume them. Detection is not a claim to recognise arbitrary passwords, and it is separate from provenance tracking."),
    ("internal/redact", "Redacting recorded content",
     r"Read the text transformations, then the JSON traversal. Follow their callers in core events and the log writer to understand why redaction happens at more than one ingress point. Tests cover shapes and nested payloads; the replacement describes a removal without retaining the removed secret."),
    ("internal/log", "The durable event contract",
     r"Read \src{internal/log/payloads.go} beside \src{internal/log/log.go}: payloads define meaning, while the writer and reader enforce the stream format, sequence and recovery rules. Use the log tests for truncation and locking behaviour. A new event is also a question for window construction, replay and frontend consumers."),
    ("internal/window", "Deriving the next request",
     r"Begin with Build in \src{internal/window/window.go}, then reply pairing, summaries and images. Replay reconstructs an earlier request from the recipe and preceding events. Keep provider wire details out of this layer: its output is provider-neutral, and historical recipes must continue to reproduce their recorded hashes."),
    ("internal/retrieval", "Ranking by words",
     r"Read \src{internal/retrieval/words.go} for how text becomes the words a search matches, then the index. It holds no text and writes nothing: \src{internal/core/recall.go} builds one from the log for the conversation (ADR 50). A ranking by embeddings would be a second ranked list fused with this one, not a change to it. Tests: \file{internal/retrieval/retrieval\_test.go}."),
    ("internal/llm", "Provider wire formats",
     r"Begin at the interfaces in \src{internal/llm/llm.go}. Compare one complete adapter with its tests before reading the others. Dialect, effort, status, retry and redirect files isolate decisions shared across requests. A streamed response has to become the same canonical reply shape as a non-streamed response; usage and reasoning fields still have provider-specific semantics."),
    ("internal/config", "Configuration and its edits",
     r"Read config and its model types, then budget, MCP and workspace configuration. Edit and legacy handling explain what is preserved or migrated when a person changes setup. This is the on-disk configuration layer; a running session's pending model choice and frozen tool catalogue belong elsewhere."),
    ("internal/catalogue", "Published model information",
     r"Read the catalogue lookup API before the models.dev conversion. The generator under gen refreshes data; ordinary lookup uses the bundled catalogue. Keep a person's explicit model settings distinct from values filled from published information. See \file{internal/catalogue/catalogue\_test.go} and the configuration tests for fallback behaviour."),
    ("internal/budget", "Measuring and stopping a turn",
     r"Read the limits and accounting together. Follow the loop's calls into this package to distinguish a soft checkpoint from a hard stop. Scheme may choose a strategy, but cannot make the host forget consumed resources. The package tests exercise thresholds without a real model."),
    ("internal/event", "What frontends observe",
     r"This package defines frontend events and prompt data. Compare events derived from log records with ephemeral notifications. Consumers must not infer durable history solely from a notification channel; catch-up reads the session log."),
    ("internal/frontend", "The people-facing surfaces",
     r"The subdirectories are independent entry routes to core. Plain implements line-oriented interaction; TUI maintains a terminal view; run supervises longer work; RPC owns daemon peers and methods; web bridges allowed methods to browser clients. Within RPC start with types, protocol, server and peer before feature handlers. Within TUI start with model, backend and events before individual widgets. Browser assets are outside this Go catalogue and require their own browser checks."),
    ("internal/mcp", "Calling MCP servers",
     r"Start with the client API and tool descriptions, then follow either HTTP or stdio transport. The session registers the resulting tools in core. Read tests for failed calls, startup and transport behaviour, and compare the separate container layer when the server itself must be confined."),
    ("internal/container", "Running a contained server",
     r"Read runtime discovery and process construction before server setup. The session supplies policy; this package supplies the runtime mechanism. Container tests use a fake runtime, while explicitly named integration tests can exercise an installed container engine. Do not infer containment merely from the MCP protocol."),
    ("internal/termemu", "The terminal emulator seam",
     r"Read the interface and the registry together. This package exists because the core may not import the Charm library that parses a program's output (ADR 10): \src{internal/prim/term.go} uses only this interface, the adapter in \src{internal/frontend/termvt/termvt.go} registers the real emulator, and the binary links it in. The terminal primitives are experimental (ADR 48); expect this package to change or go with them."),
    ("internal/egress", "The contained server's network boundary",
     r"The proxy is the network route for contained MCP servers. Read its request checks with \src{cmd/nodwyn-egress/main.go} and the binding facts supplied by core. Its tests exercise the proxy directly; model tool-call checks alone cannot establish what a server sends onward."),
    ("internal/extract", "Documents and images at ingress",
     r"Read extraction, image handling and outgoing conversion as separate paths. Follow their core callers in attach, docs and uploads. Content entering a conversation needs provenance and redaction; conversion is not an authorisation to transmit a document."),
    ("internal/policyfile", "Reading and updating policy files",
     r"Read the policy document representation and update rules alongside the guarded primitives in \src{internal/prim/policy.go}. A structured update still acts on a protected file, so the caller's authority and the file's validation remain necessary."),
    ("scripts/validate", "The repository's evidence collector",
     r"Read main, changes, checks, base and runner in that order. This is the program behind make validate and the project's Scheme wrapper. Check selection follows changed paths; regression testing compares changed Go tests against unchanged code. The report distinguishes failures, skipped work and warnings so an agent can describe what actually ran."),
    ("test", "Reusable fake services and the browser demo",
     r"These are support implementations, not the Go test cases themselves. The fake model serves scripted replies; fake OAuth, Drive, MCP, judge and container services let integration tests exercise real boundaries without paid models or real credentials. The browser demo constructs a controllable application for UI checks. Read each helper with a test that calls it to understand who owns startup and cleanup."),
]

SCHEME_ORDER = [
    "internal/scheme/prelude/base.scm",
    *[f"internal/loop/{name}.scm" for name in (
        "strategy", "prompts", "compact", "agents", "optimize", "plan",
        "shell", "hygiene", "readdef", "envhint", "checkloop")],
    ".nodwyn/init.scm", "dev/go.scm", "dev/git.scm", "dev/repo.scm",
]


def render():
    tracked = subprocess.check_output(
        ["git", "ls-files", "-z"], cwd=ROOT
    ).decode().split("\0")
    go = sorted(p for p in tracked if p.endswith(".go"))
    host = [p for p in go if not p.endswith("_test.go")]
    scheme = sorted(p for p in tracked if p.endswith(".scm"))
    lines = lambda paths: sum(len((ROOT / p).read_text().splitlines()) for p in paths)
    host_lines, go_lines, scheme_lines = lines(host), lines(go), lines(scheme)
    out = [
        "% Generated by docs/book/source/generate.py; edit the generator, not this inventory.",
        r"\part{The source catalogue}\label{part:source-catalogue}",
        r"\chapter{Coverage and reading conventions}",
        f"This inventory includes {len(host)} of {len(go)} tracked Go files: every non-test Go file, "
        f"{host_lines:,} of {go_lines:,} Go source lines ({host_lines / go_lines:.1%} by lines). "
        f"It also includes all {len(scheme)} tracked Scheme files ({scheme_lines:,} lines). "
        "Counts include comments and blank lines; Go test files are omitted, not silently shortened.",
        r"Each file is printed in full from the checkout at build time, including build constraints, imports and comments. The navigation map in Part~\ref{part:source-map} supplies routes through these listings. Some Unicode symbols are rendered with equivalent typesetting glyphs; consult the source file for exact bytes.",
        r"\chapter{Complete Scheme sources}",
        r"The prelude comes first, then the loop in installation order, then this repository's customisation and the adversarial scenarios. Read Chapter~\ref{chap:repo-customisation} with the init and dev listings. The scenarios depend on the exfiltration test fixtures and must not be treated as a normal session script.",
    ]
    ordered = [p for p in SCHEME_ORDER if p in scheme]
    ordered += [p for p in scheme if p not in ordered]
    for path in ordered:
        out.append(r"\sourcefile{" + path + "}{Scheme}")
        if path == ".nodwyn/init.scm":
            out += [r"\section{The standing approvals used by this customisation}",
                    r"This is the checked-in policy discussed in Chapter~\ref{chap:repo-customisation}; deny rules still apply.",
                    r"\lstinputlisting[style=repository,language={}]{../../.nodwyn/allow.toml}"]
    included = set()
    for prefix, title, intro in GROUPS:
        paths = [p for p in host if p.startswith(prefix + "/")]
        if not paths:
            continue
        out += [r"\chapter{" + title + "}", intro]
        for path in paths:
            if path in included:
                raise ValueError(f"duplicate source: {path}")
            included.add(path)
            out.append(r"\sourcefile{" + path + "}{Go}")
    if included != set(host):
        raise ValueError(f"Go files without a reading group: {sorted(set(host) - included)}")
    # Percent signs in prose are escaped so the coverage sentence survives TeX.
    return "\n\n".join(out).replace("% by lines", r"\% by lines") + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail if the catalogue is stale")
    args = parser.parse_args()
    content = render()
    if args.check:
        if not OUTPUT.exists() or OUTPUT.read_text() != content:
            parser.exit(1, "Source catalogue is stale; run python3 docs/book/source/generate.py\n")
        print("PASS: source catalogue covers all tracked non-test Go and all tracked Scheme files")
    else:
        OUTPUT.write_text(content)
        print(f"Wrote {OUTPUT.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
