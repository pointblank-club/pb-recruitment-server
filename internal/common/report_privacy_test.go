package common

import (
	"strings"
	"testing"
)

func TestRedactReportURLs(t *testing.T) {
	for _, value := range []string{
		`Get "https://provider.test/reports/private-capability/data/pairs.csv": timeout`,
		`failed to parse Location header "http://provider.test/reports/private-capability/%zz": invalid escape`,
		`https%3A%2F%2Fprovider.test%2Freports%2Fprivate-capability%2Fdata`,
	} {
		redacted := RedactReportURLs(value)
		if strings.Contains(redacted, "private-capability") || !strings.Contains(redacted, "[redacted]") {
			t.Fatal(redacted)
		}
	}
	if got := RedactReportURLs("independent failure: context deadline exceeded"); got != "independent failure: context deadline exceeded" {
		t.Fatal(got)
	}
}
