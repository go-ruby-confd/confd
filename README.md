# go-ruby-confd/confd

A pure-Go (**CGO=0**) Ruby-facing adapter over
[`github.com/abtreece/confd`](https://github.com/abtreece/confd) — the
maintained pure-Go fork of `kelseyhightower/confd`.

confd renders configuration files from Go text/templates whose data comes from
a backend key/value store (env, file, etcd, consul, vault, redis, …). confd is
already pure Go and is *the* reference implementation, so **this package does
not reimplement any of it**: it imports confd's own packages
(`pkg/backends`, `pkg/template`, `pkg/memkv`, `pkg/util`) and adds a small,
importable Go API plus the seams a Ruby interpreter
([go-embedded-ruby / rbgo](https://github.com/go-embedded-ruby)) will later
wire. 100% compatibility means running confd's actual code, not a hand-port.

## Install

```sh
go get github.com/go-ruby-confd/confd
```

Requires Go 1.26+.

## Usage

### Render a template to a string (no disk or network for the caller)

```go
out, err := confd.RenderString(
    `host={{getv "/db/host"}} up={{toUpper (getv "/db/host")}}`,
    map[string]string{"/db/host": "pg1"},
    confd.WithKeys("/db"),
)
// out == "host=pg1 up=PG1"
```

All of confd's template functions — `getv`, `getvs`, `gets`, `exists`,
`base64Encode`, `json`, `toUpper`, `map`, `seq`, … — are reachable, because it
is confd's own function map doing the rendering.

### Render from a real backend

```go
b, _ := confd.EnvBackend()          // reads OS environment variables
defer b.Close()
out, _ := confd.RenderWithBackend(`v={{getv "/app/host"}}`, b, confd.WithKeys("/app"))

f, _ := confd.FileBackend([]string{"data.yaml"}, "*.yaml")  // reads YAML/JSON
```

### Process a confd config directory

```go
p, _ := confd.NewProcessor(confd.ProcessorConfig{
    ConfDir: "/etc/confd",           // conf.d/*.toml + templates/*.tmpl
    Backend: b,
})
_ = p.Once()                         // render every resource once, write target files

// interval mode (blocking; run in a goroutine)
stop, done, errCh := make(chan bool), make(chan bool), make(chan error, 1)
go p.RunInterval(30, stop, done, errCh)
```

## Backends

| Backend | Constructor | External service? | Test status |
|---------|-------------|-------------------|-------------|
| env    | `EnvBackend()`               | no  | fully tested |
| file   | `FileBackend(files, filter)` | no  | fully tested |
| memory | `MemoryBackend(kv)` / `NewMemoryClient` | no | fully tested (adapter seam) |
| etcd / consul / vault / redis / dynamodb / ssm / … | `NewBackend(backends.Config{Backend: …})` | **yes** | reachable, exercised via the in-memory seam; not tested against a live service |

The `env` and `file` backends need nothing external and are the always-available
path. The remote backends are constructed through confd's own `backends.New`;
they are not mocked into a false pass — supply a real endpoint, or use the
in-memory [`MemoryClient`](confd.go) seam for tests and for the interpreter.

## Design

- **`Backend`** — selects a confd backend via `backends.New`.
- **`MemoryClient`** — an in-memory `backends.StoreClient` (the one piece of
  StoreClient logic this package owns, so tests/rbgo can inject KV data).
- **`Processor`** — runs confd's template processing over a config directory
  (`Once` / `RunInterval`); watch/interval sits behind a seam so tests do not
  block.
- **`RenderString` / `RenderWithBackend`** — render a single template body to a
  string. confd's byte-level renderer and default function map are unexported,
  so these drive confd's real `template.Process` against an ephemeral,
  internally-managed temp directory and return the produced bytes; the network
  is never touched.

## Licensing

This package is **BSD-3-Clause** (`the go-ruby-confd/confd authors`; see
[`LICENSE`](LICENSE), with SPDX headers on every source file).

The dependency `github.com/abtreece/confd` is **MIT-licensed**
(Copyright © 2013 Kelsey Hightower). It is **imported and built from source**,
not vendored or forked into this repository, so this project's own code does not
mix licenses. Consumers of a compiled binary that links confd must observe
confd's MIT terms for that portion.

## Benchmarks

See [`BENCHMARKS.md`](BENCHMARKS.md). Because this package imports confd rather
than reimplementing it, confd itself is the performance reference and the
adapter overhead is negligible.
