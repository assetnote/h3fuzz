package ffuf

import (
	"flag"
	"strings"
	"testing"
)

func TestHTTP1FlagParseAndHelpContract(t *testing.T) {
	options := NewConfigOptions()
	flags := flag.NewFlagSet("h3fuzz", flag.ContinueOnError)
	RegisterFlags(flags, options)

	definition := flags.Lookup("http1")
	if definition == nil {
		t.Fatal("-http1 is absent from the CLI flag registry")
	}
	if !strings.Contains(definition.Usage, "HTTP/1.1") {
		t.Fatalf("-http1 help = %q, want an explicit HTTP/1.1 description", definition.Usage)
	}
	if err := flags.Parse([]string{"-http1"}); err != nil {
		t.Fatalf("parse -http1: %v", err)
	}
	if !options.HTTP.Http1 {
		t.Fatal("-http1 did not set HTTPOptions.Http1")
	}
}
