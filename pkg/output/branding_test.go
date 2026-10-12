package output

import (
	"strings"
	"testing"
)

func TestFileReportTemplatesUseH3fuzzBranding(t *testing.T) {
	for name, reportTemplate := range map[string]string{
		"html":     htmlTemplate,
		"markdown": markdownTemplate,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(reportTemplate, "h3fuzz Report") {
				t.Error("report template is missing the h3fuzz report heading")
			}
			if strings.Contains(reportTemplate, "FFUF Report") {
				t.Error("report template still contains the legacy FFUF report heading")
			}
		})
	}
}
