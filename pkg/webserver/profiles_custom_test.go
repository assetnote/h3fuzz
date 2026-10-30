package webserver

import (
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestCustomProfilePreservesConfiguredExtensions(t *testing.T) {
	profile, ok := profileByID("custom")
	if !ok {
		t.Fatal("custom profile is not available")
	}
	opts := ffuf.NewConfigOptions()
	opts.Input.Extensions = "graphql,json,xml"

	applyProfile(opts, profile)

	if got, want := opts.Input.Extensions, "graphql,json,xml"; got != want {
		t.Fatalf("custom extensions = %q, want %q", got, want)
	}
	if !opts.Input.DirSearchCompat {
		t.Fatal("custom profile should retain DirSearch extension expansion")
	}
}

func TestNormalizeCreateAcceptsCustomProfileFromSavedDefaults(t *testing.T) {
	settings := DefaultSettings()
	settings.Defaults.Profile = "custom"
	settings.Defaults.Options.Input.Extensions = "graphql,json,xml"

	session, err := normalizeCreate(CreateSessionRequest{
		Target:  "https://example.test/FUZZ",
		Profile: "custom",
	}, settings)
	if err != nil {
		t.Fatalf("normalizeCreate: %v", err)
	}
	if got, want := session.Profile, "custom"; got != want {
		t.Fatalf("profile = %q, want %q", got, want)
	}
	if got, want := session.Options.Input.Extensions, "graphql,json,xml"; got != want {
		t.Fatalf("extensions = %q, want %q", got, want)
	}
}
