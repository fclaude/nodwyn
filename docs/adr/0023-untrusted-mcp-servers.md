# ADR 0023: Untrusted MCP servers — isolation, pinning, fail-closed

- Status: **implemented** (W3a, remote servers, 2026-09-30; W3b, contained servers, 2026-10-01)
- Date: 2026-09-22

## Context
ADR 22 modelled an MCP server as a primitive provider with a declared
capability profile, written for servers the user wrote or vetted. The
operating assumption is now the opposite: **nodwyn must be safe with servers
it cannot write and cannot review**, installed from a registry, updated by
someone else, possibly hostile.

That moves the risk. With a vetted server the risk surface is the tool
*call*. With an untrusted server there are four surfaces, and three of them
exist before any tool is called:

1. **The process.** `npx -y @someone/server` downloads and executes code on
   the machine, with the user's privileges, at session start. A capability
   profile does not constrain it: it constrains what nodwyn will *ask* it to
   do, not what it does on its own.
2. **The manifest.** Tool names, descriptions and schemas are text the
   server chooses, and they land in the model's context. A server can
   redescribe a tool after it has been approved.
3. **The arguments.** Whatever nodwyn sends a tool leaves the machine (or
   reaches a local process that can send it on).
4. **The results.** Whatever comes back enters the model's context.

## Decision

### 1. No untrusted server runs in nodwyn's process tree
nodwyn does not `npx`, `uvx` or otherwise spawn a third-party server as a
child on the host. Two shapes are supported:

- **Remote (HTTP/SSE).** The server is a network service. It is then exactly
  what the binding table already models: a destination, a credential class,
  positions and methods. Nothing new is needed.
- **Contained (local).** nodwyn starts the server inside a container
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
On connect, nodwyn hashes the server's tool manifest — every name,
description and schema — and compares it with the hash recorded in
`.nodwyn/mcp.toml` when the server was approved. A mismatch **stops the
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

## W3a implemented — 2026-09-30

Remote servers over MCP's streamable HTTP transport (`internal/mcp`), as
decided above, with these specifics:

- `mcp.toml` in the config directory lists servers: `url` (https, or http
  on loopback), optional `credential` and `position`, the approved
  `manifest`, the `workspaces` where the server is offered, and per tool an
  `effect` (`read` | `write`, absent = undeclared) and `on`. A new server
  offers nothing until reviewed; a new tool is off until turned on.
- The manifest hashes every tool's name, title, description, canonical input
  schema and annotations. **Deviation:** a changed or unapproved manifest
  withholds that server's tools for the session and says why (session
  details, the task's Tools panel), rather than stopping the session: the
  rest of the task still works, and nothing of the server is offered.
  Approval re-reads the live manifest and refuses if it changed since review.
- A tool is a primitive `server__tool` (model APIs refuse `/` in tool names;
  the console shows `server/tool`). It is always egress to the server's host
  with a fixed method and position, and a new Go-only `EgressClass` makes it
  credential-bearing for E3, so the binding table is checked before the call
  and the confirm names class, host and position. The transport checks the
  binding again and injects the key on every request (initialize, list,
  call), as keyed models do; redirects are not followed.
- `effect = "read"` is not irreversible; `write` and undeclared tools confirm
  every call, with the integration's own reason, a preview of the named
  arguments, and no standing answer (none can be derived for them).
- §4 as decided: every parameter read from the tool's schema has the
  `payload` role, since the whole call is sent. L1 scans every string in
  every argument, objects and arrays included, and refuses a literal key
  whose class is not bound for the server's host, exactly as for an
  `http-request` body. L2 reads the arguments' provenance. Results pass the
  ingress redactor like every tool result and are tagged `external:mcp`: a
  tag of their own rather than `external:http`, because it names the source
  (a third party the person approved) and not the transport, which a
  contained server (W3b) would share with the web.
- **Deviation:** L2 denies content from outside heading to a host no binding
  names, and a server without a credential has no binding row. The person
  named its host by approving the server in `mcp.toml`, which is the same
  act a binding is, so a new Go-only `EgressApproved` makes the server's
  host a named destination for L2, for that server's tools only and never
  for another primitive reaching the same host. A keyed server's host is
  named by its binding anyway, so both kinds of server behave alike.
- The console's Connections page adds, reviews, approves and disconnects
  servers; approval and edits are local-console only.

Known gaps: optional arguments the model skips are omitted, so an explicit
empty or false value for an optional parameter cannot be sent; nested input
schemas reach the model as `array`/`object` without their inner shape. L2
does not tell one approved server from another: a result of one server may
go to another as its arguments, as file content may.
Checks made while connecting at session start are recorded right after
`session.start`; inspection from Connections has no session log to record in.

## W3b implemented — 2026-10-01

Contained servers (`transport = "container"` in `mcp.toml`), as decided in
§1, with these specifics (`internal/container`, `internal/egress`,
`cmd/nodwyn-egress`, `internal/core/contained.go`):

- An entry names `image` (`name@sha256:` and 64 hex characters; a tag, or a
  tag beside the digest, is refused), `network` (hosts, or `host:port`; no
  wildcard), `mounts` (`path`, `path:read`, `path:write`), optional
  `command`/`args` replacing the image's entrypoint (an argument list, no
  shell), and `credential` with `position = "env:NAME"`. A credential with
  `network = []` is refused: it would have nowhere to go. Mounts are folders
  strictly inside the workspace, resolved through links when the server
  starts, seen under `/workspace/PATH`; the workspace root and `.nodwyn/`
  (or a folder holding one) are never mounted. `[mcp] runtime` in
  `config.toml` picks podman or docker (default podman if on PATH, else
  docker); with neither, a contained server is `unavailable` with the
  reason and remote servers work as before.
- **Deviation: the MCP channel is stdio, not loopback HTTP.** nodwyn runs
  `run --rm -i` and speaks MCP's stdio transport on the container's
  standard input and output (`mcp.NewStdio`; the JSON-RPC client is shared
  with the HTTP transport). No port is published, nothing listens that
  another local process could reach, and it works unchanged with
  `--network=none`, where a runtime cannot publish a port at all. The
  server's standard error is kept, capped at 8 KiB, and shown redacted in
  the task's details; it never reaches the model. A server's requests to
  nodwyn are answered only for `ping`: nodwyn declares no client
  capability, so a server cannot ask it to sample the model.
- Every run gets `--rm --pull=never --log-driver=none --read-only
  --cap-drop=ALL --security-opt=no-new-privileges`, a memory and a process
  limit, and for podman `--http-proxy=false`; the server also gets a 64 MB
  `/tmp`. The runtime
  itself starts with a fixed short environment (`PATH`, `HOME`,
  `XDG_RUNTIME_DIR`, the runtime's own variables); a container gets only
  what is named with `--env`. `--log-driver=none` keeps the MCP
  conversation out of journald or the runtime's JSON logs. The container
  is named `nodwyn-SERVER-SESSION`, labelled `nodwyn.session` and
  `nodwyn.mcp`, and removed (`rm --force`) when the session closes, after
  its input is closed; a server that is not approved, or changed, is
  removed as soon as its manifest is read.
- **Pulling is the person's act.** A session checks the image by digest
  (`image inspect`, comparing `RepoDigests`) and never pulls; a missing
  image makes the server `unavailable` with the command that fixes it.
  `nodwyn mcp pull NAME`, or Pull in Connections (local console only), runs
  `pull name@sha256:…`. `nodwyn mcp` lists the servers and whether each
  image is present.
- **The allowlist.** With `network = []` the server runs with
  `--network=none`. With hosts, nodwyn creates two networks per server and
  session: `NAME-in`, `--internal` (and `--disable-dns` for podman), and
  `NAME-out`, an ordinary one. The server is attached to `NAME-in` alone,
  with `HTTP_PROXY`, `HTTPS_PROXY`, their lower-case forms, `NO_PROXY` for
  loopback and `NODE_USE_ENV_PROXY=1`. The proxy, `nodwyn-egress`, runs in
  a sidecar on both networks: the server's own image as a root file system
  (so nothing more is pulled), the static binary mounted read-only as its
  entrypoint, as uid 65534, with the same isolation flags. It lets through
  a CONNECT tunnel or a plain-HTTP request only to a listed host, matched
  exactly, on 443 and 80 or the listed port; anything else is a 403 or 400,
  it is not a web server and not an open proxy, the Host header of a
  forwarded request is the address checked, and a listed name that
  resolves to a loopback, link-local, multicast or unspecified address is
  refused (pasta maps the host's loopback to 169.254.1.2). Each attempt is
  one JSON line on its standard output, host and port only, which the
  session records as **`mcp.egress`** {server, method, host, port, allowed,
  reason} (ADR 2 amendment), up to 1000 per server, then a count. The
  sidecar's standard input is held by nodwyn and it stops when that ends
  (`-lifeline`), so a proxy does not outlive a nodwyn that was killed.
- The binary is found as `[mcp] egress`, else next to the nodwyn
  executable; `make build` and `make promote` build it
  (`CGO_ENABLED=0 GOOS=linux`) and install it beside `nodwyn-stable`; the
  macOS app carries one per Linux architecture. The sidecar mounts a
  private copy, named by its content, under the data directory: on SELinux
  the mount is relabelled (`z`), which must not change the installed file.
- **Credentials.** The class must be bound for every host on the list at
  the `env:NAME` position (`binding.Exact` and `Position` accept `env:`
  positions; methods do not apply, so Connections writes `"*"`). Each
  lookup is recorded as an `egress.check` for `SERVER__*` (made while the
  session is built, written just after `session.start`); one unbound host
  and the server is not started. The
  value reaches the runtime only as `NAME=value` in its environment, through
  a new keychain sink (`InjectEnv`), and the argument list says `--env=NAME`
  alone. An OAuth2 token is refreshed (and logged) just before the server
  starts; a variable cannot be refreshed later, so a contained server gets
  the token valid at start. The bindings are checked again before every
  call, and a call is refused once one is gone: the container keeps the
  value it was given, but nodwyn asks nothing more of it.
- **What the guard sees.** A contained server's tool sends to its
  container, `mcp:NAME` (ADR 22 §4), approved like a remote server's host
  (`EgressApproved`), so L2 lets content from outside go there. Its
  arguments may go on to the listed hosts, so a new Go-only
  `EgressContained`/`EgressOnward` makes L1 and L2 treat a literal key, or a
  value derived from a credential, as bound only if its class is bound for
  every listed host, and with no network for none: a key in the arguments
  of a server with `network = []` is refused. The call carries no
  credential, so E3 has nothing to check per call; the checks above stand
  in for it. The confirm and the tool's description name "the contained
  server NAME (network: none | hosts)"; undeclared tools ask every call
  with no standing answer, as in W3a.
- `transport = "host-process"` is refused with the reason. **Deviation:**
  there is no `policy.allow_host_mcp` to lift it.
- Inspection from Connections starts the server with the same isolation,
  its credential only where bound for every host, and removes it; its proxy
  reports are not recorded (there is no session log).

Verified with rootless podman 5.8.7 (netavark, pasta) on Fedora 44 with
SELinux enforcing, and docker 29.8.2, by hand and by the opt-in test
(`NODWYN_CONTAINER_TESTS=1 go test -run TestContainedWithPodman
./internal/core`; `NODWYN_CONTAINER_TEST_RUNTIME=docker` for docker,
`NODWYN_CONTAINER_TESTS_NET=1` to reach a listed host), which runs a static
probe server (`test/fakes/mcp/stdio`) in a local image and pulls nothing:

- On the internal network the server's only route is its own subnet; with
  `--disable-dns` it has no DNS server, and the resolvers in its
  `resolv.conf` are unreachable. On docker's internal network the embedded
  DNS answers SERVFAIL for outside names, which a forwarding resolver
  would have answered: it does not forward. Direct connections to an
  address (1.1.1.1:443) or to a listed host, bypassing the proxy, fail;
  the sidecar's address on the outer network is unreachable; IPv6 has
  only loopback.
- Through the proxy, a listed host is reached (example.com, 200); an
  unlisted one is refused (www.google.com: 403 on CONNECT and on plain
  HTTP), as are a listed host on another port, an address, the proxy
  itself and localhost; a request for the proxy's own pages gets 400; a
  Host header naming another host does not move the request. Every
  attempt appears as `mcp.egress`.
- With `--network=none`: no route and no DNS. The read-only root and a
  read-only mount refuse writes; `/tmp` takes them. A variable set in
  nodwyn's environment is absent; the declared credential variable is
  present. `--http-proxy=false` keeps the host's proxy variables out.
- Attached standard input and output work with `--log-driver=none` on
  both runtimes. Containers and networks are gone after the session
  closes. The proxy stops when the process holding its input does.
- On SELinux, a bind-mounted binary labelled `user_home_t` or
  `user_tmp_t` cannot be executed in the container; relabelling the
  private copy with `z` makes it work. `podman cp` into a `--read-only`
  container also works, but docker refuses it ("container rootfs is
  marked read-only"), hence the mount.

Tests use fakes only: a fake runtime (`test/fakes/container`, the test
binary run again) records every argument list and environment and speaks
MCP over stdio, so `go test ./...` needs no runtime, image or network.

Known gaps: a client that ignores the proxy variables reaches nothing (it
fails closed). The proxy resolves the listed names, so their DNS is
trusted. A folder mounted on SELinux keeps the shared container label
afterwards; files a non-root image user writes in a writable mount belong
to a subordinate uid under rootless podman. If nodwyn is killed, the
proxy stops when its input ends, and so does a server that ends with its
input, as MCP servers do; one that does not keeps running, and the two
networks remain, until removed by hand (`podman ps` and `podman network
prune` with `--filter label=nodwyn.mcp`). A server
that dies mid-session is not restarted; its calls fail and say so. The
exfil suite (`test/exfil`) runs primitives without a session, so the
contained cases live in `internal/core` and `internal/guard` tests and the
opt-in test above. macOS (a podman or docker virtual machine) is built for
but was not run here.
