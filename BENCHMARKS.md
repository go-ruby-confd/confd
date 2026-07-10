# Benchmarks

The adapter imports `github.com/abtreece/confd` rather than reimplementing it,
so **confd itself is the reference** — the numbers below are confd's own
template-processing cost plus the adapter's negligible overhead (one temp dir
and one resource/template write per `RenderString` call). There is no second
implementation to be "as fast as": the code path *is* confd's.

Run them yourself:

```sh
GOWORK=off go test -run '^$' -bench . -benchmem ./...
```

## Results (Apple M4 Max, darwin/arm64, Go 1.26.4, `-benchtime 100x`)

| Benchmark | keys | ns/op | B/op | allocs/op |
|-----------|-----:|------:|-----:|----------:|
| `RenderString`            |   10 |   829 524 |  71 555 |   511 |
| `RenderString`            |  100 |   820 424 | 118 403 |   980 |
| `RenderString`            | 1000 | 1 617 693 | 797 586 | 6 258 |
| `MemoryClientGetValues`   | 1000 |    70 954 | 162 427 |    57 |

### Reading the numbers

- `RenderString` is **end-to-end**: it stands up an ephemeral confd config
  directory, has confd fetch the keys from the in-memory backend, render the Go
  template through confd's real function map, stage the output and atomically
  rename it into place, then reads the result back. The bulk of the fixed
  ~0.8 ms is confd's file staging / atomic-rename in its `fileStager`, not the
  adapter — the adapter's own work (a `MkdirTemp` and two small writes) is a
  handful of syscalls.
- `MemoryClientGetValues` isolates the in-memory backend seam (the code this
  package actually owns): ~71 µs to prefix-scan and copy 1000 keys, no disk.
- The per-key rendering cost scales roughly linearly (10 → 1000 keys ≈ 2× wall
  time), dominated by confd's template execution and `memkv` population.

For a Ruby consumer that renders many templates, prefer a long-lived
`Processor` over repeated `RenderString` calls so the config directory and
template cache are reused across renders.
