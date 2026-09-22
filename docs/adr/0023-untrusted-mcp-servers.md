# ADR 0023: Untrusted MCP servers — isolation, pinning, fail-closed

- Status: **proposed** (posture agreed 2026-09-22; refines ADR 22 §4)
- Date: 2026-09-22

## Context
ADR 22 modelled an MCP server as a primitive provider with a declared
capability profile, written for servers the user wrote or vetted. The
operating assumption is now the opposite: **nibb must be safe with servers
it cannot write and cannot review**, installed from a registry, updated by
someone else, possibly hostile.

That moves the risk. With a vetted server the risk surface is the tool
*call*. With an untrusted server there are four surfaces, and three of them
exist before any tool is called:

1. **The process.** `npx -y @someone/server` downloads and executes code on
   the machine, with the user's privileges, at session start. A capability
   profile does not constrain it: it constrains what nibb will *ask* it to
   do, not what it does on its own.
2. **The manifest.** Tool names, descriptions and schemas are text the
   server chooses, and they land in the model's context. A server can
   redescribe a tool after it has been approved.
3. **The arguments.** Whatever nibb sends a tool leaves the machine (or
   reaches a local process that can send it on).
4. **The results.** Whatever comes back enters the model's context.

## Decision

### 1. No untrusted server runs in nibb's process tree
nibb does not `npx`, `uvx` or otherwise spawn a third-party server as a
child on the host. Two shapes are supported:

- **Remote (HTTP/SSE).** The server is a network service. It is then exactly
  what the binding table already models: a destination, a credential class,
  positions and methods. Nothing new is needed.
- **Contained (local).** nibb starts the server inside a container
  (`podman` or `docker`, chosen in config) and talks to it over HTTP on a
  loopback port. The container is created from the server's declared
  isolation policy:

```toml
[[server]]
name      = "notion"
transport = "container"
image     = "ghcr.io/example/mcp-notion@sha256:…"   # digest-pinned, never a tag
network   = ["api.notion.com"]        # empty list = no network at all
mounts    = []                        # workspace subdirectories, read or write
credential = "notion-key"
position   = "env:NOTION_TOKEN"       # a binding position, like any other
```

`network = []` means no egress at all; a non-empty list is enforced by the
container network policy *and* mirrored into the binding table, so the two
never disagree. `mounts` are workspace-relative and default to none: an MCP
server sees no filesystem unless the user gives it one. The host environment
is never passed; only the credential the binding authorises, at the declared
position.

A user who insists on a host process gets an explicit
`transport = "host-process"` that prints what it means and is refused unless
`policy.allow_host_mcp = true`. It is not the default and not recommended.

### 2. The manifest is pinned
On connect, nibb hashes the server's tool manifest — every name,
description and schema — and compares it with the hash recorded in
`.nibb/mcp.toml` when the server was approved. A mismatch **stops the
session before the first message** and shows a diff of what changed;
continuing requires re-approval, which is an ordinary confirmed write to
the profile.

This is what makes "approved once" mean something for software that updates
itself: you approved *these tools with these descriptions*, not a name.

### 3. Fail closed on capability
Unchanged from ADR 22 and now load-bearing: a tool whose profile does not
declare `egress` and `irreversible` is treated as **both**. Every call
confirms; any egress the binding table does not authorise is denied.

### 4. Arguments are egress, results are ingress
Both edges are already built. Arguments pass the binding check (E3) and L1
on the way out; results pass the ingress redactor (E2) and are tagged
`external` for L2 on the way in. **A result is data, never instruction**:
nothing an MCP server returns can grant a permission, and the system prompt
says so.

### 5. What the user is really deciding
The confirm on an MCP call is not "is this argument safe?" — it is "do I
want this third party to see this?". The prompt therefore names the server,
the destination and the class of what is being sent, the way an egress
confirm names class and host.

## Consequences
- **W3 splits.** W3a is remote servers: small, because it is the binding
  table plus a JSON-RPC client. W3b is contained servers: a container
  runtime dependency, an isolation policy, image digest pinning, and a
  health/lifecycle story. They ship separately.
- A container runtime becomes an optional dependency. Without one, only
  remote servers work, and that is a reasonable product.
- Digest pinning means a server does not silently update; updating is a
  deliberate, reviewable act.
- The exfil suite gains: a contained server with `network = []` cannot
  reach the network even when its tool is called; a manifest change blocks
  the session; an undeclared tool confirms.

## Accepted limits
- **A container is not a VM.** A kernel escape, or a runtime misconfigured
  by the user, is out of scope.
- **A server sees what we send it.** Isolation constrains what the server
  can reach on its own; it does not un-send an argument. That decision stays
  with the user, which is why it is a confirm and a binding, not a default.
- **Descriptions still reach the model.** Pinning makes them stable and
  reviewed; it does not make them true. The guard never reads them, so the
  worst case remains a call the user sees in a confirm.
