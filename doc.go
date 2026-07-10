// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-confd/confd authors

// Package confd is a pure-Go (no cgo) Ruby-facing adapter over the confd
// configuration-templating tool github.com/abtreece/confd — the maintained
// pure-Go fork of kelseyhightower/confd.
//
// confd renders configuration files from Go text/templates whose data comes
// from a backend key/value store (env, file, etcd, consul, vault, redis, …).
// It is already pure Go and is the reference implementation, so this package
// does not reimplement any of it: it imports confd's own packages
// (pkg/backends, pkg/template, pkg/memkv, pkg/util) and exposes a small,
// importable Go API plus the seams a Ruby interpreter (go-embedded-ruby /
// rbgo) will later wire. 100% compatibility here means running confd's actual
// code, not a hand-port.
//
// # What this package adds
//
//   - [Backend]: a thin wrapper selecting a confd backend via backends.New.
//     [EnvBackend] and [FileBackend] need no external service and are the
//     always-available, fully-tested path; [NewBackend] reaches the rest
//     (etcd/consul/vault/redis/… — those need a live service, so they are
//     exercised through the [MemoryClient] seam or skipped, never mocked into
//     a false pass).
//
//   - [MemoryClient]: an in-memory implementation of confd's
//     backends.StoreClient interface, backed by a Go map. It lets tests and
//     the rbgo binding feed key/value data without any network, while confd's
//     real template engine does the rendering.
//
//   - [Processor]: runs confd's template processing over a config directory
//     (conf.d/*.toml resources + templates/*.tmpl) against a [Backend], in
//     one-shot ([Processor.Once]) and interval ([Processor.RunInterval])
//     modes. The interval/watch mode sits behind a seam so tests do not block.
//
//   - [RenderString] / [RenderWithBackend]: render a single template body to a
//     string, without the caller touching disk or the network. confd's byte
//     level renderer and default function map are unexported, so these drive
//     confd's real template.Process against an ephemeral, internally-managed
//     temp directory and return the produced bytes; the network is never
//     touched (the data comes from an in-memory backend). All of confd's
//     template functions (getv, getvs, gets, exists, base64Encode, json,
//     toUpper, …) are reachable through this path because it is confd's own
//     function map doing the work.
//
// # License
//
// This package is BSD-3-Clause ("the go-ruby-confd/confd authors"). The
// imported dependency github.com/abtreece/confd is MIT-licensed
// (Copyright (c) 2013 Kelsey Hightower); it is imported and built from source,
// not vendored or forked, so there is no license mixing of this project's own
// code.
package confd
