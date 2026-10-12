package ffuf

import (
	"encoding/json"
	"flag"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Section keys — the help groups, in display order (rendered by help.go).
const (
	SectionHTTP    = "http"
	SectionGeneral = "general"
	SectionCompat  = "compat"
	SectionMatcher = "matcher"
	SectionFilter  = "filter"
	SectionInput   = "input"
	SectionOutput  = "output"
)

// isKnownSection reports whether s is one of the defined help sections.
func isKnownSection(s string) bool {
	switch s {
	case SectionHTTP, SectionGeneral, SectionCompat, SectionMatcher, SectionFilter, SectionInput, SectionOutput:
		return true
	}
	return false
}

// RegisteredFlag is one flag's help metadata.
type RegisteredFlag struct {
	Name    string
	Section string
	Hidden  bool
}

// FlagMetadata is the machine-readable description of one canonical command
// line option. The web console consumes this instead of maintaining a second,
// inevitably stale list of ffuf flags.
type FlagMetadata struct {
	Name              string      `json:"name"`
	JSONPath          string      `json:"json_path,omitempty"`
	Section           string      `json:"section"`
	Type              string      `json:"type"`
	Default           interface{} `json:"default,omitempty"`
	Usage             string      `json:"usage"`
	Aliases           []string    `json:"aliases"`
	Repeatable        bool        `json:"repeatable"`
	Hidden            bool        `json:"hidden"`
	Choices           []string    `json:"choices,omitempty"`
	Min               *int        `json:"min,omitempty"`
	Max               *int        `json:"max,omitempty"`
	Available         bool        `json:"available"`
	UnavailableReason string      `json:"unavailable_reason,omitempty"`
}

// DescribeFlags derives API metadata from the same struct tags used by
// RegisterFlags. Defaults are copied through JSON so callers cannot mutate opts
// through slice values stored in the returned descriptions.
func DescribeFlags(opts *ConfigOptions) []FlagMetadata {
	if opts == nil {
		opts = NewConfigOptions()
	}
	var out []FlagMetadata
	seen := make(map[string]bool)
	root := reflect.ValueOf(opts).Elem()
	rootType := root.Type()
	for i := 0; i < root.NumField(); i++ {
		group := root.Field(i)
		groupType := group.Type()
		groupJSON := strings.Split(rootType.Field(i).Tag.Get("json"), ",")[0]
		for j := 0; j < groupType.NumField(); j++ {
			sf := groupType.Field(j)
			name := sf.Tag.Get("ffuf")
			if name == "" {
				continue
			}
			if sf.Tag.Get("cli_only") == "true" {
				seen[name] = true
				for _, alias := range splitAliases(sf.Tag.Get("alias")) {
					seen[alias] = true
				}
				continue
			}
			fieldJSON := strings.Split(sf.Tag.Get("json"), ",")[0]
			path := ""
			if fieldJSON != "" && fieldJSON != "-" {
				path = groupJSON + "." + fieldJSON
			}
			aliases := splitAliases(sf.Tag.Get("alias"))
			value := group.Field(j).Interface()
			// Break any reference to the caller's backing arrays.
			if b, err := json.Marshal(value); err == nil {
				copyValue := reflect.New(group.Field(j).Type()).Interface()
				if json.Unmarshal(b, copyValue) == nil {
					value = reflect.ValueOf(copyValue).Elem().Interface()
				}
			}
			meta := FlagMetadata{
				Name:       name,
				JSONPath:   path,
				Section:    sf.Tag.Get("section"),
				Type:       flagFieldType(group.Field(j).Kind()),
				Default:    value,
				Usage:      sf.Tag.Get("usage"),
				Aliases:    aliases,
				Repeatable: group.Field(j).Kind() == reflect.Slice,
				Available:  true,
			}
			decorateFlagMetadata(&meta)
			out = append(out, meta)
			seen[name] = true
			for _, alias := range aliases {
				seen[alias] = true
			}
		}
	}

	// Explicit flags cannot be represented as plain ConfigOptions fields. Use a
	// throwaway FlagSet to capture their authoritative usage strings.
	clone := *opts
	fs := flag.NewFlagSet("metadata", flag.ContinueOnError)
	reg := RegisterFlags(fs, &clone)
	fs.VisitAll(func(f *flag.Flag) {
		if seen[f.Name] {
			return
		}
		rf := reg.byName[f.Name]
		path := ""
		typeName := "string"
		repeatable := false
		var defaultValue interface{} = f.DefValue
		switch f.Name {
		case "preflight", "preflight-var", "preflight-var-auto":
			path, typeName, repeatable = "http.preflights", "object_array", true
			defaultValue = opts.HTTP.Preflights
		case "postflight", "postflight-var", "postflight-var-auto":
			path, typeName, repeatable = "http.postflights", "object_array", true
			defaultValue = opts.HTTP.Postflights
		case "compressed", "i", "k":
			typeName = "boolean"
		}
		meta := FlagMetadata{Name: f.Name, JSONPath: path, Section: rf.Section,
			Type: typeName, Default: defaultValue, Usage: f.Usage, Aliases: []string{},
			Repeatable: repeatable, Hidden: rf.Hidden, Available: true}
		if f.Name == "compressed" || f.Name == "i" || f.Name == "k" {
			meta.Available = false
			meta.UnavailableReason = "ignored command-line compatibility flag"
		}
		out = append(out, meta)
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section == out[j].Section {
			return out[i].Name < out[j].Name
		}
		return out[i].Section < out[j].Section
	})
	return out
}

func splitAliases(value string) []string {
	if value == "" {
		return []string{}
	}
	var aliases []string
	for _, alias := range strings.Split(value, ",") {
		if alias = strings.TrimSpace(alias); alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return aliases
}

func flagFieldType(kind reflect.Kind) string {
	switch kind {
	case reflect.Bool:
		return "boolean"
	case reflect.Int:
		return "integer"
	case reflect.Float64:
		return "number"
	case reflect.Slice:
		return "string_array"
	default:
		return "string"
	}
}

func decorateFlagMetadata(meta *FlagMetadata) {
	choices := map[string][]string{
		"mode": {"clusterbomb", "pitchfork", "sniper"}, "fmode": {"or", "and"},
		"mmode": {"or", "and"}, "recursion-strategy": {"default", "greedy"},
		"preflight-mode": {"per-request", "per-thread"}, "preflight-error": {"abort", "ignore"},
		"of": {"json", "ejson", "html", "md", "csv", "ecsv", "all"},
	}
	meta.Choices = choices[meta.Name]
	limits := map[string][2]int{
		"http3-connections":     {0, MaxHTTP3Connections},
		"http3-max-connections": {1, MaxHTTP3Connections},
		"http3-max-workers":     {1, MaxHTTP3Workers},
		"http3-streams":         {1, MaxHTTP3Streams},
		"http3-probe-timeout":   {1, MaxHTTP3ProbeTimeoutSecs},
	}
	if lim, ok := limits[meta.Name]; ok {
		min, max := lim[0], lim[1]
		meta.Min, meta.Max = &min, &max
	}
	unavailable := map[string]string{
		"config":              "web sessions use structured options and saved settings instead of process config files",
		"search":              "history hash search is a command-line process action",
		"V":                   "version display is a command-line process action",
		"http3-fingerprint":   "standalone fingerprint mode is a command-line process action",
		"o":                   "web results are persisted automatically; use the session export endpoint",
		"of":                  "choose JSON, text, or terminal format from the session export endpoint",
		"or":                  "web results are persisted automatically",
		"od":                  "response file output is unavailable in web sessions",
		"debug-log":           "per-process debug log files are unavailable in web sessions",
		"run-telemetry":       "per-process telemetry files are unavailable in web sessions",
		"audit-log":           "per-process audit log files are unavailable in web sessions",
		"noninteractive":      "web sessions are always non-interactive",
		"c":                   "the web console controls presentation",
		"json":                "the web console uses structured API data",
		"s":                   "the web console controls presentation",
		"v":                   "the web console always exposes the full result URL",
		"web":                 "controls the web server process, not an individual scan session",
		"web-listen":          "controls the web server process, not an individual scan session",
		"web-db":              "controls the web server process, not an individual scan session",
		"web-cert":            "controls the web server process, not an individual scan session",
		"web-key":             "controls the web server process, not an individual scan session",
		"web-plain-http":      "controls the web server process, not an individual scan session",
		"web-allow-remote":    "controls the web server process, not an individual scan session",
		"web-allow-input-cmd": "controls the web server process, not an individual scan session",
	}
	if reason := unavailable[meta.Name]; reason != "" {
		meta.Available = false
		meta.UnavailableReason = reason
	}
}

// FlagRegistry is the single source of truth describing every CLI flag, used to
// drive the segmented help output. It is produced by RegisterFlags.
type FlagRegistry struct {
	byName map[string]RegisteredFlag
}

// SectionOf returns the help section a flag belongs to.
func (r *FlagRegistry) SectionOf(name string) (string, bool) {
	f, ok := r.byName[name]
	return f.Section, ok
}

// --- custom flag.Value types, writing straight into the target []string field ---

// multiStringValue appends each occurrence verbatim (e.g. -H "A: b" -H "C: d").
type multiStringValue struct{ p *[]string }

func (m *multiStringValue) String() string { return "" }
func (m *multiStringValue) Set(v string) error {
	*m.p = append(*m.p, v)
	return nil
}

// wordlistValue appends, comma-splitting a single occurrence (e.g. -w a.txt,b.txt).
type wordlistValue struct{ p *[]string }

func (w *wordlistValue) String() string { return "" }
func (w *wordlistValue) Set(v string) error {
	if parts := strings.Split(v, ","); len(parts) > 1 {
		*w.p = append(*w.p, parts...)
	} else {
		*w.p = append(*w.p, v)
	}
	return nil
}

// csvReplaceValue drops any default on the first occurrence, then appends
// comma-split values. Matches the legacy -acs semantics (replace, not append).
type csvReplaceValue struct {
	p   *[]string
	set bool
}

func (c *csvReplaceValue) String() string { return "" }
func (c *csvReplaceValue) Set(v string) error {
	if !c.set {
		*c.p = []string{}
		c.set = true
	}
	*c.p = append(*c.p, strings.Split(v, ",")...)
	return nil
}

// extraFlag is the explicit supplement for flags that can't be expressed as a
// plain tagged field: cross-section aliases, dummy compatibility flags, and
// flags whose usage text contains a backtick (illegal inside a struct tag).
type extraFlag struct {
	Name    string
	Section string
	Hidden  bool
	Bind    func(fs *flag.FlagSet, o *ConfigOptions)
}

var extraFlags = []extraFlag{
	// Dummy `copy as curl` compat flags: accepted and ignored. They have no backing
	// ConfigOptions field to bind, so unlike every real flag (and its aliases) they
	// can't be expressed as a tagged field and are declared explicitly here.
	{"compressed", SectionCompat, true, func(fs *flag.FlagSet, o *ConfigOptions) {
		_ = fs.Bool("compressed", true, "Dummy flag for copy as curl functionality (ignored)")
	}},
	{"i", SectionCompat, true, func(fs *flag.FlagSet, o *ConfigOptions) {
		_ = fs.Bool("i", true, "Dummy flag for copy as curl functionality (ignored)")
	}},
	{"k", SectionCompat, true, func(fs *flag.FlagSet, o *ConfigOptions) {
		_ = fs.Bool("k", false, "Dummy flag for backwards compatibility")
	}},
	// Preflight/postflight files bind positionally: a -preflight-var attaches to the
	// preceding -preflight. That can't be a plain tagged field, so they are Func
	// flags whose callbacks append to the option struct in command-line order. Using
	// flag.Parse's own tokenization (rather than a separate os.Args scan) keeps them
	// consistent with every other flag and lets config-file values survive: the CLI
	// appends to whatever a config file loaded, it does not clobber it.
	{"preflight", SectionHTTP, false, func(fs *flag.FlagSet, o *ConfigOptions) {
		fs.Func("preflight", "Raw HTTP request file to run before each fuzzing request (repeatable, order matters). Inherits main config headers, including -H auth and -b cookies.", func(v string) error {
			if v == "" {
				return fmt.Errorf("-preflight requires a request file path")
			}
			o.HTTP.Preflights = append(o.HTTP.Preflights, PreflightConfig{RequestFile: v})
			return nil
		})
	}},
	{"preflight-var", SectionHTTP, false, func(fs *flag.FlagSet, o *ConfigOptions) {
		fs.Func("preflight-var", "Extract a variable from the preceding -preflight response: \"NAME:regex\", matched against the body, then the headers (repeatable)", func(v string) error {
			return appendFlightVar(&o.HTTP.Preflights, "-preflight-var", "-preflight", v)
		})
	}},
	{"postflight", SectionHTTP, false, func(fs *flag.FlagSet, o *ConfigOptions) {
		fs.Func("postflight", "Raw HTTP request file to run after each fuzzing request (repeatable, order matters). Inherits main config headers, including -H auth and -b cookies.", func(v string) error {
			if v == "" {
				return fmt.Errorf("-postflight requires a request file path")
			}
			o.HTTP.Postflights = append(o.HTTP.Postflights, PreflightConfig{RequestFile: v})
			return nil
		})
	}},
	{"postflight-var", SectionHTTP, false, func(fs *flag.FlagSet, o *ConfigOptions) {
		fs.Func("postflight-var", "Extract a variable from the preceding -postflight response: \"NAME:regex\", matched against the body, then the headers (repeatable)", func(v string) error {
			return appendFlightVar(&o.HTTP.Postflights, "-postflight-var", "-postflight", v)
		})
	}},
	{"preflight-var-auto", SectionHTTP, false, func(fs *flag.FlagSet, o *ConfigOptions) {
		fs.Func("preflight-var-auto", "Extract a variable from the preceding -preflight response by name: \"NAME:key\" tries form field, meta tag, cookie and header; \"NAME:[form|meta|cookie|header]key\" pins the source (repeatable)", func(v string) error {
			return appendFlightVarAuto(&o.HTTP.Preflights, "-preflight-var-auto", "-preflight", v)
		})
	}},
	{"postflight-var-auto", SectionHTTP, false, func(fs *flag.FlagSet, o *ConfigOptions) {
		fs.Func("postflight-var-auto", "Extract a variable from the preceding -postflight response by name, like -preflight-var-auto (repeatable)", func(v string) error {
			return appendFlightVarAuto(&o.HTTP.Postflights, "-postflight-var-auto", "-postflight", v)
		})
	}},
}

// appendFlightVarAuto attaches a "NAME:key" or "NAME:[source]key" extraction to
// the last entry in a preflight/postflight chain. The source tag lives only on
// this flag: "[header]X" is a valid regex, so -preflight-var can't take it.
func appendFlightVarAuto(chain *[]PreflightConfig, varFlag, fileFlag, spec string) error {
	if len(*chain) == 0 {
		return fmt.Errorf("%s %q has no preceding %s", varFlag, spec, fileFlag)
	}
	name, sel, ok := parseVarSpec(spec)
	if !ok {
		return fmt.Errorf("%s value %q must be \"NAME:key\" or \"NAME:[source]key\" with a non-empty name and key", varFlag, spec)
	}
	source, key, err := ParseVarSelector(sel)
	if err != nil {
		return fmt.Errorf("%s value %q: %s", varFlag, spec, err)
	}
	last := &(*chain)[len(*chain)-1]
	last.Vars = append(last.Vars, VarExtract{Name: name, Source: source, Key: key})
	return nil
}

// ParseVarSelector splits "[source]key" into its source and key, or returns
// VarSourceAuto for a bare key. The tag is a prefix because field names can end
// in brackets (PHP's "data[_Token][key]") and contain colons (JSF's
// "loginForm:token"); only the first "]" closes the tag.
func ParseVarSelector(sel string) (source, key string, err error) {
	if !strings.HasPrefix(sel, "[") {
		return VarSourceAuto, sel, nil
	}
	end := strings.IndexByte(sel, ']')
	if end < 0 {
		return "", "", fmt.Errorf("unterminated source tag, want [%s]key", strings.Join(VarSources, "|"))
	}
	source, key = sel[1:end], sel[end+1:]
	if err := ValidateVarSource(source, key); err != nil {
		return "", "", err
	}
	return source, key, nil
}

// ValidateVarSource checks a source/key pair, whether it came from the CLI or a
// config file.
func ValidateVarSource(source, key string) error {
	if !isVarSource(source) {
		return fmt.Errorf("unknown source %q, want one of %s", source, strings.Join(VarSources, ", "))
	}
	if key == "" {
		return fmt.Errorf("source %q needs a non-empty key", source)
	}
	return nil
}

// appendFlightVar attaches a "NAME:regex" extraction to the last entry in a
// preflight/postflight chain. It errors (so flag.Parse surfaces it) when there is
// no preceding file flag or the spec is malformed, rather than silently dropping.
func appendFlightVar(chain *[]PreflightConfig, varFlag, fileFlag, spec string) error {
	if len(*chain) == 0 {
		return fmt.Errorf("%s %q has no preceding %s", varFlag, spec, fileFlag)
	}
	name, regex, ok := parseVarSpec(spec)
	if !ok {
		return fmt.Errorf("%s value %q must be \"NAME:regex\" with a non-empty name and regex", varFlag, spec)
	}
	last := &(*chain)[len(*chain)-1]
	last.Vars = append(last.Vars, VarExtract{Name: name, Regex: regex})
	return nil
}

// parseVarSpec splits a "NAME:regex" spec on the FIRST colon, so the regex may
// itself contain colons. Both the name and the regex must be non-empty.
func parseVarSpec(spec string) (name, regex string, ok bool) {
	idx := strings.IndexByte(spec, ':')
	if idx <= 0 || idx == len(spec)-1 {
		return "", "", false
	}
	return spec[:idx], spec[idx+1:], true
}

// RegisterFlags is the single source of truth for CLI flag registration. It walks
// the `ffuf:`-tagged fields of ConfigOptions via reflection and registers each on
// fs — using the field's CURRENT value as the default, which preserves the
// config-file < command-line precedence — then applies the explicit supplement.
// It returns a FlagRegistry describing every flag for the segmented help.
func RegisterFlags(fs *flag.FlagSet, o *ConfigOptions) *FlagRegistry {
	reg := &FlagRegistry{byName: make(map[string]RegisteredFlag)}
	record := func(name, section string, hidden bool) {
		reg.byName[name] = RegisteredFlag{Name: name, Section: section, Hidden: hidden}
	}

	root := reflect.ValueOf(o).Elem()
	for i := 0; i < root.NumField(); i++ {
		group := root.Field(i) // an *Options sub-struct (addressable: o is a pointer)
		gt := group.Type()
		for j := 0; j < gt.NumField(); j++ {
			sf := gt.Field(j)
			name := sf.Tag.Get("ffuf")
			if name == "" {
				continue // not a flag
			}
			usage, section := sf.Tag.Get("usage"), sf.Tag.Get("section")
			if usage == "" {
				panic("h3fuzz: flag -" + name + " is missing a usage tag")
			}
			if !isKnownSection(section) {
				panic("h3fuzz: flag -" + name + " has unknown section \"" + section + "\"")
			}
			bindField(fs, name, usage, sf.Tag.Get("kind"), group.Field(j).Addr().Interface())
			record(name, section, false)

			// Compatibility aliases bind the SAME field, hidden in the compat section.
			for _, alias := range strings.Split(sf.Tag.Get("alias"), ",") {
				alias = strings.TrimSpace(alias)
				if alias == "" {
					continue
				}
				bindField(fs, alias, sf.Tag.Get("usage"), sf.Tag.Get("kind"), group.Field(j).Addr().Interface())
				record(alias, SectionCompat, true)
			}
		}
	}

	for _, ef := range extraFlags {
		ef.Bind(fs, o)
		record(ef.Name, ef.Section, ef.Hidden)
	}
	return reg
}

// bindField registers one reflected field pointer as a flag of the matching type.
// It panics on an unsupported type or a slice field without a valid kind — those
// are programming errors caught by the registration test, not runtime conditions.
func bindField(fs *flag.FlagSet, name, usage, kind string, ptr interface{}) {
	switch p := ptr.(type) {
	case *bool:
		fs.BoolVar(p, name, *p, usage)
	case *int:
		fs.IntVar(p, name, *p, usage)
	case *float64:
		fs.Float64Var(p, name, *p, usage)
	case *string:
		fs.StringVar(p, name, *p, usage)
	case *[]string:
		switch kind {
		case "multistring":
			fs.Var(&multiStringValue{p}, name, usage)
		case "wordlist":
			fs.Var(&wordlistValue{p}, name, usage)
		case "csvreplace":
			fs.Var(&csvReplaceValue{p: p}, name, usage)
		default:
			panic("h3fuzz: slice flag -" + name + " needs kind:multistring|wordlist|csvreplace (got " + kind + ")")
		}
	default:
		panic("h3fuzz: unsupported flag field type for -" + name)
	}
}

func isVarSource(source string) bool {
	if source == VarSourceAuto {
		return true
	}
	for _, s := range VarSources {
		if s == source {
			return true
		}
	}
	return false
}
