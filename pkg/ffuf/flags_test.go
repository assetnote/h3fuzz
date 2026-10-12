package ffuf

import (
	"flag"
	"testing"
)

// TestRegisterFlags_Wellformed runs RegisterFlags against the real ConfigOptions
// struct so any malformed declaration — a slice field missing its `kind`, an
// unsupported field type, an unexported tagged field, or a duplicate flag name —
// fails here in CI instead of panicking at runtime. This is what buys back the
// compile-time safety that reflection-based registration gives up.
func TestRegisterFlags_Wellformed(t *testing.T) {
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	opts := NewConfigOptions()

	var reg *FlagRegistry
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("RegisterFlags panicked (malformed tag or unsupported field type): %v", r)
			}
		}()
		reg = RegisterFlags(fs, opts)
	}()

	validSection := map[string]bool{
		SectionHTTP: true, SectionGeneral: true, SectionCompat: true, SectionMatcher: true,
		SectionFilter: true, SectionInput: true, SectionOutput: true,
	}

	// The full expected flag surface. Unlike a bare count, this catches a net-zero
	// add-and-remove, a rename, or a duplicate — update it DELIBERATELY when the CLI
	// surface changes. 100 visible flags + 7 hidden compat (4 aliases + 3 dummies).
	expected := map[string]bool{
		// HTTP
		"H": true, "X": true, "b": true, "cc": true, "ck": true, "d": true,
		"http1": true, "http2": true, "http3": true, "http3-connections": true,
		"http3-fingerprint": true, "http3-max-connections": true, "http3-max-workers": true,
		"http3-probe-timeout": true, "http3-streams": true, "http3-high-rate": true,
		"http3-high-rate-acknowledge": true, "http3-target-rps": true, "http3-latency-ms": true,
		"ignore-body": true,
		"no-http3":    true, "no-response-optimization": true, "r": true, "raw": true, "recursion": true,
		"recursion-depth": true, "recursion-strategy": true, "replay-proxy": true,
		"sni": true, "timeout": true, "u": true, "x": true,
		"preflight-mode": true, "preflight-error": true, "preflight-anyhost": true,
		"preflight": true, "preflight-var": true, "postflight": true, "postflight-var": true,
		"preflight-var-auto": true, "postflight-var-auto": true,
		// General
		"V": true, "ac": true, "acc": true, "ach": true, "ack": true, "acs": true,
		"c": true, "config": true, "harness-run-id": true, "json": true, "maxtime": true, "maxtime-job": true,
		"noninteractive": true, "p": true, "rate": true, "s": true, "sa": true,
		"scraperfile": true, "scrapers": true, "se": true, "search": true, "sf": true,
		"t": true, "v": true,
		"web": true, "web-listen": true, "web-db": true,
		"web-cert": true, "web-key": true, "web-plain-http": true,
		"web-allow-remote": true, "web-allow-input-cmd": true,
		// Matcher
		"mc": true, "ml": true, "mmode": true, "mr": true, "ms": true, "mt": true, "mw": true,
		// Filter
		"fc": true, "fl": true, "fmode": true, "fr": true, "fs": true, "ft": true, "fw": true,
		// Input
		"D": true, "e": true, "enc": true, "ic": true, "input-cmd": true, "input-num": true,
		"input-shell": true, "mode": true, "request": true, "request-proto": true, "w": true,
		// Output
		"audit-log": true, "debug-log": true, "o": true, "od": true, "of": true, "or": true,
		"profile-block": true, "profile-cpu": true, "profile-heap": true, "profile-mutex": true,
		"quic-qlog-dir": true, "quic-telemetry": true, "run-telemetry": true,
		// Compat aliases
		"cookie": true, "data": true, "data-ascii": true, "data-binary": true,
		// Compat dummies
		"compressed": true, "i": true, "k": true,
	}

	registered := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		registered[f.Name] = true
		if f.Usage == "" {
			t.Errorf("flag -%s has an empty usage string", f.Name)
		}
		section, ok := reg.SectionOf(f.Name)
		if !ok {
			t.Errorf("flag -%s is registered but missing from the registry (no section)", f.Name)
			return
		}
		if !validSection[section] {
			t.Errorf("flag -%s has unknown section %q", f.Name, section)
		}
	})

	for name := range expected {
		if !registered[name] {
			t.Errorf("expected flag -%s is not registered (dropped or renamed?)", name)
		}
	}
	for name := range registered {
		if !expected[name] {
			t.Errorf("unexpected flag -%s registered — add it to the expected set deliberately", name)
		}
	}
}

func TestProfilingFlagsPopulateCLIOnlyPaths(t *testing.T) {
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	opts := NewConfigOptions()
	RegisterFlags(fs, opts)

	if err := fs.Parse([]string{
		"-profile-cpu", "cpu.pprof",
		"-profile-heap", "heap.pprof",
		"-profile-mutex", "mutex.pprof",
		"-profile-block", "block.pprof",
	}); err != nil {
		t.Fatal(err)
	}
	if opts.Output.ProfileCPU != "cpu.pprof" || opts.Output.ProfileHeap != "heap.pprof" ||
		opts.Output.ProfileMutex != "mutex.pprof" || opts.Output.ProfileBlock != "block.pprof" {
		t.Fatalf("profiling paths were not recorded: %#v", opts.Output)
	}
}

func TestDescribeFlagsOmitsCLIOnlyDiagnosticFlags(t *testing.T) {
	diagnostics := map[string]bool{
		"harness-run-id": true,
		"profile-cpu":    true, "profile-heap": true, "profile-mutex": true, "profile-block": true,
		"quic-qlog-dir": true, "quic-telemetry": true,
	}
	for _, metadata := range DescribeFlags(NewConfigOptions()) {
		if diagnostics[metadata.Name] {
			t.Errorf("CLI-only diagnostic flag -%s leaked into the web flag registry", metadata.Name)
		}
	}
}

func TestWebTLSFlagsPopulateProcessOptions(t *testing.T) {
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	opts := NewConfigOptions()
	RegisterFlags(fs, opts)
	if err := fs.Parse([]string{
		"-web-cert", "/secure/web.crt",
		"-web-key", "/secure/web.key",
		"-web-plain-http",
	}); err != nil {
		t.Fatal(err)
	}
	if opts.General.WebTLSCertificate != "/secure/web.crt" {
		t.Fatalf("web certificate = %q", opts.General.WebTLSCertificate)
	}
	if opts.General.WebTLSPrivateKey != "/secure/web.key" {
		t.Fatalf("web private key = %q", opts.General.WebTLSPrivateKey)
	}
	if !opts.General.WebPlainHTTP {
		t.Fatal("web plain HTTP flag was not recorded")
	}
}
