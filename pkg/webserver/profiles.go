package webserver

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const (
	browserUserAgent             = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/111.0.0.0 Safari/537.36"
	defaultWebThreadCount        = 1000
	currentSettingsSchemaVersion = 5
)

type Profile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Extensions  []string `json:"extensions"`
}

var profiles = []Profile{
	{ID: "aspm", Name: "ASP Minimal", Description: "Common ASP.NET content", Extensions: []string{"aspx", "htm", "html", "js"}},
	{ID: "asp", Name: "ASP / ASP.NET", Description: "Expanded ASP and ASP.NET content", Extensions: []string{"aspx", "asmx", "html", "htm", "js", "ashx", "asp", "svc"}},
	{ID: "php", Name: "PHP", Description: "Common PHP content", Extensions: []string{"php", "html", "htm", "js"}},
	{ID: "jsp", Name: "JSP / Java", Description: "Common Java web content", Extensions: []string{"jsp", "htm", "html", "do", "action", "js"}},
	{ID: "dir", Name: "Directories", Description: "Directories and common HTML content", Extensions: []string{"html"}},
}

func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	return out
}

func profileByID(id string) (Profile, bool) {
	for _, p := range profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func DefaultSettings() Settings {
	opts := ffuf.NewConfigOptions()
	applyProfile(opts, profiles[0])
	opts.Input.InputMode = "pitchfork"
	opts.General.Noninteractive = true
	// Regular HTTP scans need enough workers to sustain the web console's
	// historical throughput. Keep this value non-explicit so automatic HTTP/3
	// preflight remains free to replace it with capacity-derived workers.
	opts.General.Threads = defaultWebThreadCount
	// Body-consuming scrapers make automatic HEAD/Range qualification ineligible.
	// The web results explorer remains useful without them, so keep the default
	// high-throughput path deterministic; operators can still opt into a scraper.
	opts.General.Scrapers = ""
	return Settings{SchemaVersion: currentSettingsSchemaVersion, Defaults: Defaults{Wordlist: "final.txt", Profile: profiles[0].ID, Options: *opts,
		ExplicitFlags: []string{"D", "e", "mc", "fc", "H"}}}
}

func applyProfile(opts *ffuf.ConfigOptions, profile Profile) {
	opts.General.Colors = true
	opts.General.Noninteractive = true
	userAgent := "User-Agent: " + browserUserAgent
	headers := make([]string, 0, len(opts.HTTP.Headers)+1)
	for _, header := range opts.HTTP.Headers {
		if !strings.EqualFold(strings.TrimSpace(strings.SplitN(header, ":", 2)[0]), "User-Agent") {
			headers = append(headers, header)
		}
	}
	opts.HTTP.Headers = append(headers, userAgent)
	opts.Input.DirSearchCompat = true
	opts.Input.Extensions = strings.Join(profile.Extensions, ",")
	opts.Matcher.Status = "all"
	opts.Filter.Status = "404"
}

func normalizeCreate(req CreateSessionRequest, settings Settings) (Session, error) {
	target := strings.TrimSpace(req.Target)
	if target == "" {
		return Session{}, fmt.Errorf("target is required")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Session{}, fmt.Errorf("target must be an absolute http or https URL")
	}
	profileID := req.Profile
	if profileID == "" {
		profileID = settings.Defaults.Profile
	}
	profile, ok := profileByID(profileID)
	if !ok {
		return Session{}, fmt.Errorf("unknown profile %q", profileID)
	}
	wordlist := strings.TrimSpace(req.Wordlist)
	if wordlist == "" {
		wordlist = settings.Defaults.Wordlist
	}
	if wordlist == "" {
		wordlist = "final.txt"
	}
	explicit := append([]string(nil), settings.Defaults.ExplicitFlags...)
	if req.ExplicitFlags != nil {
		explicit = append([]string(nil), req.ExplicitFlags...)
	}
	explicit = uniqueStrings(explicit)
	if err := rejectWebProfilingFlags(explicit); err != nil {
		return Session{}, err
	}

	var opts ffuf.ConfigOptions
	if req.Options == nil {
		if err := rejectWebProfilingOptions(&settings.Defaults.Options); err != nil {
			return Session{}, err
		}
		opts = cloneOptions(settings.Defaults.Options)
		applyProfile(&opts, profile)
	} else {
		if err := rejectWebProfilingOptions(req.Options); err != nil {
			return Session{}, err
		}
		opts = cloneOptions(*req.Options)
	}
	// ExplicitFlags is the web API's source of truth for whether a value was
	// configured by the operator. Keep the embedded provenance in lockstep so a
	// stale options object cannot accidentally turn automatic HTTP/3 worker
	// sizing into a fixed policy (or vice versa).
	syncWebOptionProvenance(&opts, explicit)
	opts.General.Noninteractive = true
	opts.HTTP.URL = target
	if containsString(explicit, "w") && len(opts.Input.Wordlists) > 0 {
		wordlist = opts.Input.Wordlists[0]
	} else {
		opts.Input.Wordlists = []string{wordlist}
	}
	// The web console owns persistence and exports, never ffuf's per-process files.
	opts.Output.OutputFile = ""
	opts.Output.OutputDirectory = ""
	opts.Output.OutputFormat = "json"
	opts.Output.OutputSkipEmptyFile = false
	opts.Output.AuditLog = ""
	opts.Output.DebugLog = ""
	opts.Output.RunTelemetry = ""
	if err := sanitizeWebOptions(&opts); err != nil {
		return Session{}, err
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = u.Host
	}
	return Session{Name: name, Target: target, Wordlist: wordlist, Profile: profileID,
		Status: StatusIdle, Options: opts, ExplicitFlags: explicit,
		PreflightReport: PreflightReport{State: PreflightNotRun}, ErrorDiagnostics: []ErrorDiagnostic{}}, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func syncWebOptionProvenance(opts *ffuf.ConfigOptions, explicit []string) {
	if opts == nil {
		return
	}
	opts.Provenance = ffuf.ConfigOptionProvenance{}
	ffuf.MarkExplicitOptions(opts, explicit...)
}

func sanitizeWebOptions(opts *ffuf.ConfigOptions) error {
	if err := rejectWebProfilingOptions(opts); err != nil {
		return err
	}
	if opts.HTTP.Http3Fingerprint {
		return fmt.Errorf("http3-fingerprint is a standalone command-line action and cannot be used as a web scan")
	}
	if opts.General.ConfigFile != "" {
		return fmt.Errorf("config files are process-level; provide structured session options instead")
	}
	opts.General.Noninteractive = true
	opts.General.Colors = false
	opts.General.Json = false
	opts.General.Quiet = false
	opts.General.Verbose = false
	opts.Output.AuditLog = ""
	opts.Output.DebugLog = ""
	opts.Output.OutputDirectory = ""
	opts.Output.OutputFile = ""
	opts.Output.OutputFormat = "json"
	opts.Output.OutputSkipEmptyFile = false
	opts.Output.RunTelemetry = ""
	return nil
}

func rejectWebProfilingOptions(opts *ffuf.ConfigOptions) error {
	if opts.HTTP.Http3HighRate || opts.HTTP.Http3HighRateAcknowledge ||
		(opts.HTTP.Http3TargetRPS != 0 && opts.HTTP.Http3TargetRPS != ffuf.DefaultHTTP3TargetRPS) || opts.HTTP.Http3LatencyMS != 0 ||
		opts.Provenance.Http3HighRate || opts.Provenance.Http3HighRateAcknowledge ||
		opts.Provenance.Http3TargetRPS || opts.Provenance.Http3LatencyMS {
		return fmt.Errorf("HTTP/3 high-rate policy options are command-line only and cannot be used by web sessions")
	}
	if opts.General.HarnessRunID != "" {
		return fmt.Errorf("harness markers are command-line only and cannot be used by web sessions")
	}
	if opts.Output.ProfileCPU != "" || opts.Output.ProfileHeap != "" ||
		opts.Output.ProfileMutex != "" || opts.Output.ProfileBlock != "" {
		return fmt.Errorf("runtime profiling paths are command-line only and cannot be used by web sessions")
	}
	if opts.Output.QuicTelemetry || opts.Output.QuicQlogDir != "" {
		return fmt.Errorf("QUIC diagnostics are command-line only and cannot be used by web sessions")
	}
	return nil
}

func rejectWebProfilingFlags(explicit []string) error {
	for _, name := range explicit {
		switch strings.TrimLeft(strings.TrimSpace(name), "-") {
		case "harness-run-id", "profile-cpu", "profile-heap", "profile-mutex", "profile-block", "quic-telemetry", "quic-qlog-dir",
			"http3-high-rate", "http3-high-rate-acknowledge", "http3-target-rps", "http3-latency-ms":
			return fmt.Errorf("-%s is command-line only and cannot be imported into a web session", strings.TrimLeft(strings.TrimSpace(name), "-"))
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
