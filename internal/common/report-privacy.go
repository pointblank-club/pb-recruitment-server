package common

import "regexp"

// Report paths grant access to source evidence, including in redirect error messages.
var reportURLPath = regexp.MustCompile(`(?i)(/reports/|%2freports%2f)[^\s"'<>\\]+`)

func RedactReportURLs(value string) string {
	return reportURLPath.ReplaceAllString(value, "/reports/[redacted]")
}
