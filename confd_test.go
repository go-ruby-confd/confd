// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-confd/confd authors

package confd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abtreece/confd/pkg/backends"
	"github.com/abtreece/confd/pkg/backends/types"
	"github.com/abtreece/confd/pkg/template"
)

func init() {
	// Quiet confd's info-level logging during tests.
	_ = SetLogLevel("error")
}

// fakeClient is a StoreClient used to drive the newBackendClient seam.
type fakeClient struct {
	types.NoopWatcher
	types.NoopCloser
}

func (fakeClient) GetValues(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (fakeClient) HealthCheck(context.Context) error { return nil }

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

func TestSetLogLevel(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "warning", "error", "fatal", "panic", "ERROR"} {
		if err := SetLogLevel(lvl); err != nil {
			t.Fatalf("SetLogLevel(%q) unexpected error: %v", lvl, err)
		}
	}
	if err := SetLogLevel("bogus"); err == nil {
		t.Fatal("SetLogLevel(bogus): want error, got nil")
	}
	_ = SetLogLevel("error") // restore quiet
}

// ---------------------------------------------------------------------------
// Backend
// ---------------------------------------------------------------------------

func TestNewBackendError(t *testing.T) {
	if _, err := NewBackend(backends.Config{Backend: "no-such-backend"}); err == nil {
		t.Fatal("want error for invalid backend, got nil")
	}
}

func TestNewBackendDefaultName(t *testing.T) {
	// Drive the seam so an empty backend name reaches the name-defaulting
	// branch without needing a live etcd.
	orig := newBackendClient
	newBackendClient = func(cfg backends.Config) (backends.StoreClient, error) {
		return fakeClient{}, nil
	}
	defer func() { newBackendClient = orig }()

	b, err := NewBackend(backends.Config{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if b.Name() != "etcd" {
		t.Fatalf("default name: want etcd, got %q", b.Name())
	}
}

func TestEnvBackend(t *testing.T) {
	t.Setenv("MYAPP_HOST", "envhost")
	b, err := EnvBackend()
	if err != nil {
		t.Fatalf("EnvBackend: %v", err)
	}
	defer b.Close()

	if b.Name() != "env" {
		t.Fatalf("name: want env, got %q", b.Name())
	}
	if b.Client() == nil {
		t.Fatal("Client() nil")
	}
	if err := b.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	vals, err := b.GetValues(context.Background(), []string{"/myapp"})
	if err != nil {
		t.Fatalf("GetValues: %v", err)
	}
	if vals["/myapp/host"] != "envhost" {
		t.Fatalf("env GetValues: got %v", vals)
	}
}

func TestFileBackend(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "data.yaml")
	if err := os.WriteFile(yml, []byte("db:\n  host: filehost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := FileBackend([]string{yml}, "*.yaml")
	if err != nil {
		t.Fatalf("FileBackend: %v", err)
	}
	defer b.Close()
	vals, err := b.GetValues(context.Background(), []string{"/db"})
	if err != nil {
		t.Fatalf("GetValues: %v", err)
	}
	if vals["/db/host"] != "filehost" {
		t.Fatalf("file GetValues: got %v", vals)
	}
}

// ---------------------------------------------------------------------------
// MemoryClient
// ---------------------------------------------------------------------------

func TestMemoryClient(t *testing.T) {
	c := NewMemoryClient(map[string]string{"/a/b": "1", "/c": "2", "/d/e": "3"})
	c.Set("/f", "4")

	if err := c.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	// prefix match
	if v, _ := c.GetValues(context.Background(), []string{"/a"}); v["/a/b"] != "1" || len(v) != 1 {
		t.Fatalf("prefix match: got %v", v)
	}
	// exact match
	if v, _ := c.GetValues(context.Background(), []string{"/c"}); v["/c"] != "2" || len(v) != 1 {
		t.Fatalf("exact match: got %v", v)
	}
	// root "/" matches everything
	if v, _ := c.GetValues(context.Background(), []string{"/"}); len(v) != 4 {
		t.Fatalf("root match: got %v", v)
	}
	// no match
	if v, _ := c.GetValues(context.Background(), []string{"/zzz"}); len(v) != 0 {
		t.Fatalf("no match: got %v", v)
	}

	// nil map is treated as empty
	empty := NewMemoryClient(nil)
	if v, _ := empty.GetValues(context.Background(), []string{"/"}); len(v) != 0 {
		t.Fatalf("nil map: got %v", v)
	}

	// embedded no-op closer is reachable
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMemoryBackend(t *testing.T) {
	b := MemoryBackend(map[string]string{"/k": "v"})
	if b.Name() != "memory" {
		t.Fatalf("name: got %q", b.Name())
	}
}

// ---------------------------------------------------------------------------
// Processor
// ---------------------------------------------------------------------------

func TestNewProcessorErrors(t *testing.T) {
	if _, err := NewProcessor(ProcessorConfig{}); err == nil {
		t.Fatal("want error for missing backend")
	}
	b := MemoryBackend(nil)
	if _, err := NewProcessor(ProcessorConfig{Backend: b}); err == nil {
		t.Fatal("want error for missing config dir")
	}
	if _, err := NewProcessor(ProcessorConfig{Backend: b, ConfigDir: "/x"}); err == nil {
		t.Fatal("want error for missing template dir")
	}
}

func TestNewProcessorConfigDerivation(t *testing.T) {
	b := MemoryBackend(nil)

	// Explicit ConfigDir + TemplateDir (no ConfDir), explicit Ctx.
	p, err := NewProcessor(ProcessorConfig{
		Backend:     b,
		ConfigDir:   "/conf",
		TemplateDir: "/tmpl",
		Ctx:         context.Background(),
	})
	if err != nil {
		t.Fatalf("explicit dirs: %v", err)
	}
	if p.cfg.ConfigDir != "/conf" || p.cfg.TemplateDir != "/tmpl" {
		t.Fatalf("explicit dirs not honoured: %+v", p.cfg)
	}

	// Derived from ConfDir, nil Ctx -> Background.
	p2, err := NewProcessor(ProcessorConfig{Backend: b, ConfDir: "/root"})
	if err != nil {
		t.Fatalf("derived dirs: %v", err)
	}
	if p2.cfg.ConfigDir != filepath.Join("/root", "conf.d") ||
		p2.cfg.TemplateDir != filepath.Join("/root", "templates") {
		t.Fatalf("derived dirs wrong: %+v", p2.cfg)
	}
	if p2.cfg.Ctx == nil {
		t.Fatal("nil Ctx should default to Background")
	}
}

// writeConfdDir creates a confd root with one resource + template and returns
// the root dir and the dest path the template renders to.
func writeConfdDir(t *testing.T, tmplBody, toml string) (root, dest string) {
	t.Helper()
	root = t.TempDir()
	confDir := filepath.Join(root, "conf.d")
	tmplDir := filepath.Join(root, "templates")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(root, "out.conf")
	if err := os.WriteFile(filepath.Join(tmplDir, "r.tmpl"), []byte(tmplBody), 0o644); err != nil {
		t.Fatal(err)
	}
	full := "[template]\nsrc = \"r.tmpl\"\ndest = \"" + dest + "\"\nmode = \"0644\"\n" + toml
	if err := os.WriteFile(filepath.Join(confDir, "r.toml"), []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, dest
}

func TestProcessorOnce(t *testing.T) {
	root, dest := writeConfdDir(t, `host={{getv "/db/host"}}`+"\n", "keys = [\"/db\"]\n")
	b := MemoryBackend(map[string]string{"/db/host": "pg1"})
	p, err := NewProcessor(ProcessorConfig{Backend: b, ConfDir: root})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	if err := p.Once(); err != nil {
		t.Fatalf("Once: %v", err)
	}
	out, _ := os.ReadFile(dest)
	if strings.TrimSpace(string(out)) != "host=pg1" {
		t.Fatalf("rendered: %q", out)
	}
}

func TestProcessorOnceError(t *testing.T) {
	// A template calling an unknown function fails at parse time.
	root, _ := writeConfdDir(t, `{{nosuchfunc}}`, "keys = [\"/db\"]\n")
	b := MemoryBackend(nil)
	p, _ := NewProcessor(ProcessorConfig{Backend: b, ConfDir: root})
	if err := p.Once(); err == nil {
		t.Fatal("Once: want error for bad template, got nil")
	}
}

func TestProcessorRunIntervalOnceThenStop(t *testing.T) {
	root, dest := writeConfdDir(t, `host={{getv "/db/host"}}`+"\n", "keys = [\"/db\"]\n")
	b := MemoryBackend(map[string]string{"/db/host": "iv"})
	p, _ := NewProcessor(ProcessorConfig{Backend: b, ConfDir: root})

	stop := make(chan bool)
	done := make(chan bool)
	errCh := make(chan error, 1)
	close(stop) // pre-closed: one process cycle, then return
	p.RunInterval(3600, stop, done, errCh)
	<-done

	out, _ := os.ReadFile(dest)
	if strings.TrimSpace(string(out)) != "host=iv" {
		t.Fatalf("interval render: %q", out)
	}
	select {
	case err := <-errCh:
		t.Fatalf("unexpected err: %v", err)
	default:
	}
}

func TestProcessorRunIntervalLoadError(t *testing.T) {
	// A resource with empty src fails to load -> interval processor reports it
	// on errCh and returns.
	root := t.TempDir()
	confDir := filepath.Join(root, "conf.d")
	tmplDir := filepath.Join(root, "templates")
	_ = os.MkdirAll(confDir, 0o755)
	_ = os.MkdirAll(tmplDir, 0o755)
	_ = os.WriteFile(filepath.Join(confDir, "bad.toml"),
		[]byte("[template]\nsrc = \"\"\nkeys = [\"/db\"]\n"), 0o644)

	b := MemoryBackend(nil)
	p, _ := NewProcessor(ProcessorConfig{Backend: b, ConfDir: root})

	stop := make(chan bool)
	done := make(chan bool)
	errCh := make(chan error, 1)
	p.RunInterval(3600, stop, done, errCh)
	<-done

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("want load error")
		}
	default:
		t.Fatal("expected an error on errCh")
	}
}

func TestProcessorRunIntervalSeam(t *testing.T) {
	// Verify RunInterval honours the newInterval seam.
	b := MemoryBackend(nil)
	p, _ := NewProcessor(ProcessorConfig{Backend: b, ConfDir: t.TempDir()})

	called := false
	p.newInterval = func(cfg template.Config, stop, done chan bool, errCh chan error, interval int, reload <-chan struct{}) template.Processor {
		called = true
		return fakeProcessor{done: done}
	}
	stop := make(chan bool)
	done := make(chan bool)
	errCh := make(chan error, 1)
	p.RunInterval(1, stop, done, errCh)
	<-done
	if !called {
		t.Fatal("newInterval seam not called")
	}
}

type fakeProcessor struct{ done chan bool }

func (f fakeProcessor) Process() { close(f.done) }

// ---------------------------------------------------------------------------
// RenderString / RenderWithBackend
// ---------------------------------------------------------------------------

func TestRenderString(t *testing.T) {
	out, err := RenderString(
		`host={{getv "/db/host"}} up={{toUpper (getv "/db/host")}} b64={{base64Encode (getv "/db/host")}}`,
		map[string]string{"/db/host": "pg1"},
		WithKeys("/db"),
	)
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if !strings.Contains(out, "host=pg1") || !strings.Contains(out, "up=PG1") {
		t.Fatalf("render: %q", out)
	}
}

func TestRenderStringDefaultKeysAndFuncs(t *testing.T) {
	// No WithKeys -> default "/" fetches everything; exercise several confd funcs.
	out, err := RenderString(
		`{{range gets "/svc/*"}}{{.Key}}={{.Value}} {{end}}exists={{exists "/svc/a"}}`,
		map[string]string{"/svc/a": "1", "/svc/b": "2"},
	)
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if !strings.Contains(out, "exists=true") {
		t.Fatalf("render: %q", out)
	}
}

func TestRenderWithBackendEnv(t *testing.T) {
	t.Setenv("RWB_HOST", "envval")
	b, _ := EnvBackend()
	defer b.Close()
	out, err := RenderWithBackend(`v={{getv "/rwb/host"}}`, b, WithKeys("/rwb"))
	if err != nil {
		t.Fatalf("RenderWithBackend: %v", err)
	}
	if strings.TrimSpace(out) != "v=envval" {
		t.Fatalf("render: %q", out)
	}
}

func TestRenderWithOutputFormat(t *testing.T) {
	out, err := RenderString(
		`{"host":{{getv "/j/host" | printf "%q"}}}`,
		map[string]string{"/j/host": "h"},
		WithKeys("/j"), WithOutputFormat("json"), WithPrefix(""),
	)
	if err != nil {
		t.Fatalf("RenderString json: %v", err)
	}
	if !strings.Contains(out, `"host":"h"`) {
		t.Fatalf("render: %q", out)
	}
}

func TestRenderWithBackendNil(t *testing.T) {
	if _, err := RenderWithBackend("x", nil); err == nil {
		t.Fatal("want error for nil backend")
	}
}

func TestRenderProcessError(t *testing.T) {
	// Bad template body -> confd's template.Process returns an error.
	if _, err := RenderString(`{{nosuchfunc}}`, nil); err == nil {
		t.Fatal("want error for bad template body")
	}
}

// --- filesystem-seam error branches ---

func swapFS(t *testing.T) {
	t.Helper()
	mt, ma, wf, rf, ra := osMkdirTemp, osMkdirAll, osWriteFile, osReadFile, osRemoveAll
	t.Cleanup(func() {
		osMkdirTemp, osMkdirAll, osWriteFile, osReadFile, osRemoveAll = mt, ma, wf, rf, ra
	})
}

func TestRenderMkdirTempError(t *testing.T) {
	swapFS(t)
	osMkdirTemp = func(string, string) (string, error) { return "", errors.New("boom") }
	if _, err := RenderString("x", nil); err == nil {
		t.Fatal("want mkdirtemp error")
	}
}

func TestRenderMkdirConfError(t *testing.T) {
	swapFS(t)
	osMkdirAll = func(path string, _ os.FileMode) error {
		if strings.HasSuffix(path, "conf.d") {
			return errors.New("boom")
		}
		return nil
	}
	if _, err := RenderString("x", nil); err == nil {
		t.Fatal("want conf.d mkdir error")
	}
}

func TestRenderMkdirTemplatesError(t *testing.T) {
	swapFS(t)
	osMkdirAll = func(path string, m os.FileMode) error {
		if strings.HasSuffix(path, "templates") {
			return errors.New("boom")
		}
		return os.MkdirAll(path, m)
	}
	if _, err := RenderString("x", nil); err == nil {
		t.Fatal("want templates mkdir error")
	}
}

func TestRenderWriteTemplateError(t *testing.T) {
	swapFS(t)
	osWriteFile = func(path string, data []byte, m os.FileMode) error {
		if strings.HasSuffix(path, ".tmpl") {
			return errors.New("boom")
		}
		return os.WriteFile(path, data, m)
	}
	if _, err := RenderString("x", nil); err == nil {
		t.Fatal("want write .tmpl error")
	}
}

func TestRenderWriteResourceError(t *testing.T) {
	swapFS(t)
	osWriteFile = func(path string, data []byte, m os.FileMode) error {
		if strings.HasSuffix(path, ".toml") {
			return errors.New("boom")
		}
		return os.WriteFile(path, data, m)
	}
	if _, err := RenderString("x", nil); err == nil {
		t.Fatal("want write .toml error")
	}
}

func TestRenderReadOutputError(t *testing.T) {
	swapFS(t)
	osReadFile = func(string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := RenderString(`ok`, nil); err == nil {
		t.Fatal("want read-output error")
	}
}

// ---------------------------------------------------------------------------
// tomlQuote
// ---------------------------------------------------------------------------

func TestTomlQuote(t *testing.T) {
	got := tomlQuote("a\"b\\c\nd\te")
	want := `"a\"b\\c\nd\te"`
	if got != want {
		t.Fatalf("tomlQuote: got %q want %q", got, want)
	}
}
