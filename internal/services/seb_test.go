package services

import "testing"

func TestSEBUserAgent(t *testing.T) {
	for _, tc := range []struct {
		agent    string
		detected bool
	}{
		{"Mozilla/5.0 Chrome/139.0 SEB/3.9.0", true},
		{"Mozilla/5.0 (Macintosh) SEB/3.5", true},
		{"SEB/3.3.2", true},
		{"Mozilla/5.0 (SEB/3.2; iOS)", true},
		{"Mozilla/5.0 seb/3.3.2", true},
		{"Mozilla/5.0 Chrome/139.0", false},
		{"Mozilla/5.0 NotSEB/3.9", false},
		{"SEB/unknown", false},
		{"", false},
	} {
		if got := sebUserAgent.MatchString(tc.agent); got != tc.detected {
			t.Errorf("%q: detected=%v", tc.agent, got)
		}
	}
}
