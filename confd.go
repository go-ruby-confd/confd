// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-confd/confd authors

package confd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/abtreece/confd/pkg/backends"
	"github.com/abtreece/confd/pkg/backends/types"
	"github.com/abtreece/confd/pkg/log"
	"github.com/abtreece/confd/pkg/template"
)

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

// validLogLevels is the set of levels confd's logger accepts. We validate
// against it so a bad value returns an error instead of confd calling
// log.Fatal (which would os.Exit the whole process).
var validLogLevels = map[string]struct{}{
	"debug": {}, "info": {}, "warn": {}, "warning": {},
	"error": {}, "fatal": {}, "panic": {},
}

// SetLogLevel sets the verbosity of confd's internal logger. Valid levels are
// debug, info, warn/warning, error, fatal and panic. Rendering is noisy at
// info; callers typically want "error". An unknown level returns an error
// (confd itself would os.Exit on a bad level).
func SetLogLevel(level string) error {
	if _, ok := validLogLevels[strings.ToLower(level)]; !ok {
		return fmt.Errorf("confd: invalid log level %q", level)
	}
	log.SetLevel(level)
	return nil
}

// ---------------------------------------------------------------------------
// Backend
// ---------------------------------------------------------------------------

// Backend wraps a confd backends.StoreClient together with the config used to
// build it. It is the connection seam the rbgo binding wires: the key/value
// source can be a real service (env/file/etcd/…) or the in-memory
// [MemoryClient].
type Backend struct {
	client backends.StoreClient
	name   string
}

// newBackendClient is a seam over backends.New so the error path can be
// exercised without a live service.
var newBackendClient = backends.New

// NewBackend builds a Backend from a confd backend configuration. It selects
// the backend by cfg.Backend ("env", "file", "etcd", "consul", "vault",
// "redis", …) via confd's own backends.New. Backends other than env and file
// require a reachable service; construction of some (e.g. vault) validates
// eagerly and may fail here, while others fail later on GetValues.
func NewBackend(cfg backends.Config) (*Backend, error) {
	client, err := newBackendClient(cfg)
	if err != nil {
		return nil, err
	}
	name := cfg.Backend
	if name == "" {
		name = "etcd"
	}
	return &Backend{client: client, name: name}, nil
}

// EnvBackend returns a Backend reading confd keys from OS environment
// variables. It needs no external service and is always available.
func EnvBackend() (*Backend, error) {
	return NewBackend(backends.Config{Backend: "env"})
}

// FileBackend returns a Backend reading confd keys from the given YAML/JSON
// files (or directories, walked recursively). filter is an optional glob
// applied to filenames ("" matches all). It needs no external service.
func FileBackend(files []string, filter string) (*Backend, error) {
	return NewBackend(backends.Config{Backend: "file", YAMLFile: files, Filter: filter})
}

// MemoryBackend returns a Backend backed by an in-memory [MemoryClient] seeded
// with the given key/value data. It touches neither disk nor network and is
// the backend used by [RenderString].
func MemoryBackend(kv map[string]string) *Backend {
	return &Backend{client: NewMemoryClient(kv), name: "memory"}
}

// Name reports the backend kind ("env", "file", "memory", …).
func (b *Backend) Name() string { return b.name }

// Client returns the underlying confd StoreClient. This is the seam a caller
// (or the rbgo binding) uses to reach confd's backend directly.
func (b *Backend) Client() backends.StoreClient { return b.client }

// GetValues fetches the values under the given key prefixes from the backend,
// delegating to confd's StoreClient.
func (b *Backend) GetValues(ctx context.Context, keys []string) (map[string]string, error) {
	return b.client.GetValues(ctx, keys)
}

// HealthCheck reports whether the backend connection is healthy.
func (b *Backend) HealthCheck(ctx context.Context) error {
	return b.client.HealthCheck(ctx)
}

// Close releases any resources held by the backend.
func (b *Backend) Close() error { return b.client.Close() }

// ---------------------------------------------------------------------------
// MemoryClient — in-memory StoreClient seam
// ---------------------------------------------------------------------------

// MemoryClient is an in-memory implementation of confd's backends.StoreClient,
// backed by a Go map. It exists so tests and the rbgo binding can supply
// key/value data without a live backend service; confd's real template engine
// still performs the rendering. Keys are matched by prefix, mirroring the
// semantics of confd's real backends. It is safe for concurrent use.
type MemoryClient struct {
	types.NoopWatcher // supplies a no-op WatchPrefix
	types.NoopCloser  // supplies a no-op Close

	mu sync.RWMutex
	kv map[string]string
}

// NewMemoryClient returns a MemoryClient seeded with a copy of kv (nil is
// treated as empty).
func NewMemoryClient(kv map[string]string) *MemoryClient {
	c := &MemoryClient{kv: make(map[string]string, len(kv))}
	for k, v := range kv {
		c.kv[k] = v
	}
	return c
}

// Set stores or overwrites a single key.
func (c *MemoryClient) Set(key, value string) {
	c.mu.Lock()
	c.kv[key] = value
	c.mu.Unlock()
}

// GetValues returns every stored key that is prefixed by one of the requested
// keys, matching confd backend semantics.
func (c *MemoryClient) GetValues(ctx context.Context, keys []string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string)
	for k, v := range c.kv {
		for _, want := range keys {
			if want == "/" || k == want || strings.HasPrefix(k, strings.TrimSuffix(want, "/")+"/") {
				out[k] = v
				break
			}
		}
	}
	return out, nil
}

// HealthCheck always succeeds; an in-memory store is always reachable.
func (c *MemoryClient) HealthCheck(ctx context.Context) error { return nil }

// ---------------------------------------------------------------------------
// Processor — run confd over a config directory
// ---------------------------------------------------------------------------

// Processor renders the config files described by a confd config directory
// (its conf.d/*.toml resources and templates/*.tmpl templates) using a
// [Backend]. It wraps confd's own template package.
type Processor struct {
	cfg template.Config

	// Seams (overridable in tests / by rbgo) — default to confd's real code.
	processOnce func(template.Config) error
	newInterval func(cfg template.Config, stop, done chan bool, errCh chan error, interval int, reload <-chan struct{}) template.Processor
}

// ProcessorConfig configures a [Processor].
type ProcessorConfig struct {
	// ConfDir is the confd root directory. By default its conf.d subdirectory
	// holds the *.toml resources and its templates subdirectory holds the
	// *.tmpl templates (confd's standard layout).
	ConfDir string
	// ConfigDir overrides the resource directory (default: ConfDir/conf.d).
	ConfigDir string
	// TemplateDir overrides the template directory (default: ConfDir/templates).
	TemplateDir string
	// Backend supplies the key/value data. Required.
	Backend *Backend
	// Prefix is prepended to every resource's key prefix (confd -prefix).
	Prefix string
	// Noop, when true, logs what would change without writing target files.
	Noop bool
	// SyncOnly, when true, skips check/reload commands.
	SyncOnly bool
	// KeepStageFile keeps the intermediate staged files for debugging.
	KeepStageFile bool
	// Ctx is used for cancellation/timeouts (default: context.Background()).
	Ctx context.Context
}

// NewProcessor builds a Processor from cfg. It returns an error if no backend
// is supplied or if no config directory can be determined.
func NewProcessor(cfg ProcessorConfig) (*Processor, error) {
	if cfg.Backend == nil {
		return nil, errors.New("confd: a Backend is required")
	}
	configDir := cfg.ConfigDir
	if configDir == "" {
		if cfg.ConfDir == "" {
			return nil, errors.New("confd: ConfDir or ConfigDir is required")
		}
		configDir = filepath.Join(cfg.ConfDir, "conf.d")
	}
	templateDir := cfg.TemplateDir
	if templateDir == "" {
		if cfg.ConfDir == "" {
			return nil, errors.New("confd: ConfDir or TemplateDir is required")
		}
		templateDir = filepath.Join(cfg.ConfDir, "templates")
	}
	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return &Processor{
		cfg: template.Config{
			ConfDir:       cfg.ConfDir,
			ConfigDir:     configDir,
			TemplateDir:   templateDir,
			StoreClient:   cfg.Backend.client,
			Prefix:        cfg.Prefix,
			Noop:          cfg.Noop,
			SyncOnly:      cfg.SyncOnly,
			KeepStageFile: cfg.KeepStageFile,
			Ctx:           ctx,
		},
		processOnce: template.Process,
		newInterval: template.IntervalProcessor,
	}, nil
}

// Once loads and renders every template resource exactly once, writing the
// target config files. It runs confd's template.Process.
func (p *Processor) Once() error {
	return p.processOnce(p.cfg)
}

// RunInterval polls the backend every intervalSeconds and re-renders on change.
// It blocks until stop is closed (or the Processor context is cancelled), then
// closes done; backend/processing errors are delivered on errCh. Run it in its
// own goroutine. This is confd's IntervalProcessor, behind a seam.
func (p *Processor) RunInterval(intervalSeconds int, stop, done chan bool, errCh chan error) {
	var reload <-chan struct{} // nil: reload never fires
	p.newInterval(p.cfg, stop, done, errCh, intervalSeconds, reload).Process()
}

// ---------------------------------------------------------------------------
// RenderString — render a template body to a string (no disk/network exposed)
// ---------------------------------------------------------------------------

// renderOptions holds the tunables for [RenderString] / [RenderWithBackend].
type renderOptions struct {
	keys         []string
	prefix       string
	outputFormat string
}

// RenderOption customises a render call.
type RenderOption func(*renderOptions)

// WithKeys sets the key prefixes the template resource requests from the
// backend (default: []string{"/"}, i.e. everything).
func WithKeys(keys ...string) RenderOption {
	return func(o *renderOptions) { o.keys = keys }
}

// WithPrefix sets a global key prefix for the render (confd -prefix).
func WithPrefix(prefix string) RenderOption {
	return func(o *renderOptions) { o.prefix = prefix }
}

// WithOutputFormat enables confd's output-format validation (json, yaml, toml
// or xml) on the rendered result.
func WithOutputFormat(format string) RenderOption {
	return func(o *renderOptions) { o.outputFormat = format }
}

// Seams over the filesystem so RenderWithBackend's error branches are testable.
var (
	osMkdirTemp = os.MkdirTemp
	osMkdirAll  = os.MkdirAll
	osWriteFile = os.WriteFile
	osReadFile  = os.ReadFile
	osRemoveAll = os.RemoveAll
)

// RenderString renders templateBody — a Go text/template using confd's
// template functions (getv, getvs, gets, exists, base64Encode, json, toUpper,
// …) — against the in-memory key/value data, returning the produced text. It
// touches no network. confd's byte-level renderer is unexported, so this drives
// confd's real template.Process against an ephemeral, internally-managed temp
// directory (cleaned up before returning); the caller manages no files.
func RenderString(templateBody string, data map[string]string, opts ...RenderOption) (string, error) {
	return RenderWithBackend(templateBody, MemoryBackend(data), opts...)
}

// RenderWithBackend is like [RenderString] but fetches the template's data from
// an arbitrary [Backend] (env, file, …) instead of an in-memory map. Only the
// backend touches the outside world; the render itself is hermetic.
func RenderWithBackend(templateBody string, b *Backend, opts ...RenderOption) (string, error) {
	if b == nil {
		return "", errors.New("confd: a Backend is required")
	}
	o := renderOptions{keys: []string{"/"}}
	for _, opt := range opts {
		opt(&o)
	}

	dir, err := osMkdirTemp("", "go-ruby-confd-")
	if err != nil {
		return "", fmt.Errorf("confd: create work dir: %w", err)
	}
	defer func() { _ = osRemoveAll(dir) }()

	confDir := filepath.Join(dir, "conf.d")
	tmplDir := filepath.Join(dir, "templates")
	dest := filepath.Join(dir, "rendered.out")
	if err := osMkdirAll(confDir, 0o755); err != nil {
		return "", fmt.Errorf("confd: create conf.d: %w", err)
	}
	if err := osMkdirAll(tmplDir, 0o755); err != nil {
		return "", fmt.Errorf("confd: create templates dir: %w", err)
	}

	if err := osWriteFile(filepath.Join(tmplDir, "render.tmpl"), []byte(templateBody), 0o644); err != nil {
		return "", fmt.Errorf("confd: write template: %w", err)
	}
	if err := osWriteFile(filepath.Join(confDir, "render.toml"), []byte(renderResourceTOML(dest, o)), 0o644); err != nil {
		return "", fmt.Errorf("confd: write resource: %w", err)
	}

	cfg := template.Config{
		ConfDir:     dir,
		ConfigDir:   confDir,
		TemplateDir: tmplDir,
		StoreClient: b.client,
		Prefix:      o.prefix,
		Ctx:         context.Background(),
	}
	if err := template.Process(cfg); err != nil {
		return "", err
	}

	out, err := osReadFile(dest)
	if err != nil {
		return "", fmt.Errorf("confd: read rendered output: %w", err)
	}
	return string(out), nil
}

// renderResourceTOML builds the confd template-resource TOML that drives a
// single render.
func renderResourceTOML(dest string, o renderOptions) string {
	keys := make([]string, len(o.keys))
	for i, k := range o.keys {
		keys[i] = tomlQuote(k)
	}
	sort.Strings(keys) // deterministic output for tests
	var b strings.Builder
	b.WriteString("[template]\n")
	b.WriteString("src = \"render.tmpl\"\n")
	fmt.Fprintf(&b, "dest = %s\n", tomlQuote(dest))
	b.WriteString("mode = \"0644\"\n")
	fmt.Fprintf(&b, "keys = [%s]\n", strings.Join(keys, ", "))
	if o.outputFormat != "" {
		fmt.Fprintf(&b, "output_format = %s\n", tomlQuote(o.outputFormat))
	}
	return b.String()
}

// tomlQuote quotes a string as a TOML basic string.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
