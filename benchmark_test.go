// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-confd/confd authors

package confd

import (
	"fmt"
	"testing"
)

// benchData builds n keys under /svc and a template that renders all of them.
func benchData(n int) (string, map[string]string) {
	data := make(map[string]string, n)
	for i := 0; i < n; i++ {
		data[fmt.Sprintf("/svc/%04d", i)] = fmt.Sprintf("value-%d", i)
	}
	body := `{{range gets "/svc/*"}}{{.Key}}={{.Value}}
{{end}}`
	return body, data
}

// BenchmarkRenderString measures end-to-end template processing (fetch N keys
// from the in-memory backend + render) through confd's real engine. Since this
// package imports confd rather than reimplementing it, confd itself is the
// reference and the measured cost is confd's own plus the negligible adapter
// overhead (a temp dir + one resource/template write per call).
func BenchmarkRenderString(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		body, data := benchData(n)
		b.Run(fmt.Sprintf("keys=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := RenderString(body, data, WithKeys("/svc")); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMemoryClientGetValues isolates the in-memory backend seam (no disk).
func BenchmarkMemoryClientGetValues(b *testing.B) {
	_, data := benchData(1000)
	c := NewMemoryClient(data)
	keys := []string{"/svc"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := c.GetValues(nil, keys); err != nil {
			b.Fatal(err)
		}
	}
}
