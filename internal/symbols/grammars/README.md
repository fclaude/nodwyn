# Bundled Tree-sitter grammars

Go and Python modules are unmodified assets from
[wasitter v0.1.0](https://github.com/zema1/wasitter/releases/tag/v0.1.0).
JavaScript comes from that exact Go module's embedded asset. The runtime is
upstream Tree-sitter, compiled to WebAssembly, not a Go parser reimplementation.

`grammar-registry.json` records the upstream grammar/runtime revisions used by
the release. `THIRD_PARTY_NOTICES.txt` is its unmodified license bundle and must
accompany redistribution of these assets, including binaries embedding them.
The Go wrapper is MIT-licensed; wazero is Apache-2.0-licensed. Their licenses
are included alongside the assets for redistribution. All three notice files
are also embedded in the standalone binary and available with `nibb licenses`.

| Asset | SHA-256 |
|---|---|
| `wasitter-go.wasm` | `2812c281eeee4a59ab27ae71595a625c1e50619507431a8f85f03edd361ae508` |
| `wasitter-python.wasm` | `0b4396e688a08f9d2c6a11de2b018bdda63eef7b59497ee1fd56481cc96f5423` |
| JavaScript (dependency) | `7abe2b54935fc1bf33bdcb8f48434676b801bcf62eca104b19b1dd1af18bfa5b` |
| `THIRD_PARTY_NOTICES.txt` | `3d140ea7ac2dc9eaee4ecda4f484d2cb78960d537facc960774d7b3c32984e50` |
| `grammar-registry.json` | `07f7d2ca8d626d273d22828bbba225258d1eaca41ed67f7a40f405bd5e0ed85b` |

Updates are deliberate: pin the binding version, use matching release assets,
verify upstream checksums, refresh notices and this table, and run grammar,
guard, cancellation and four-target portable-build tests. No runtime downloads,
user grammar loading, native compiler or disk compilation cache is enabled.
The runtime receives no host filesystem, environment or network access and
limits guest memory to 128 MiB. Each outline owns and closes its runtime.
