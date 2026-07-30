package secretscan

import (
	"strings"
	"testing"
)

func TestScanMasksSecret(t *testing.T) {
	secret := "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	findings := Scan("fixture", []byte("TOKEN="+secret+"\n"))
	if len(findings) == 0 {
		t.Fatal("expected token finding")
	}
	for _, finding := range findings {
		if strings.Contains(finding.Preview, secret) {
			t.Fatal("finding leaked the complete secret")
		}
	}
	if Summarize(findings).Blocked == 0 {
		t.Fatal("expected a blocking finding")
	}
}
