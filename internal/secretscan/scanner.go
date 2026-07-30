package secretscan

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sessionmgr/sessionmgr/internal/domain"
)

const Version = "1"

type rule struct {
	id       string
	severity string
	pattern  *regexp.Regexp
}

var rules = []rule{
	{"private-key", "block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----`)},
	{"github-token", "block", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{30,}\b`)},
	{"openai-token", "block", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"aws-access-key", "block", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"gitlab-token", "block", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack-token", "block", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`)},
	{"credential-url", "block", regexp.MustCompile(`(?i)\bhttps?://[^/\s:@]+:[^/\s@]+@`)},
	{"secret-env", "warn", regexp.MustCompile(`(?i)^\s*(?:export\s+)?[A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|TOKEN|API_KEY|PRIVATE_KEY)[A-Z0-9_]*\s*=\s*\S+`)},
}

func Scan(source string, data []byte) []domain.SecurityFinding {
	if !utf8.Valid(data) || containsNUL(data) {
		return nil
	}
	var findings []domain.SecurityFinding
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 2*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		for _, candidate := range rules {
			match := candidate.pattern.FindString(text)
			if match == "" {
				continue
			}
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", candidate.id, source, line, match)))
			findings = append(findings, domain.SecurityFinding{
				ID:       "finding:" + hex.EncodeToString(sum[:8]),
				RuleID:   candidate.id,
				Source:   source,
				Line:     line,
				Preview:  mask(match),
				Severity: candidate.severity,
			})
		}
	}
	return findings
}

func Summarize(findings []domain.SecurityFinding) domain.SecurityReportSummary {
	summary := domain.SecurityReportSummary{
		ScannerVersion: Version,
		Findings:       findings,
	}
	for _, finding := range findings {
		switch finding.Severity {
		case "block":
			summary.Blocked++
		case "warn":
			summary.Warnings++
		default:
			summary.Info++
		}
	}
	return summary
}

func mask(value string) string {
	runes := []rune(value)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + strings.Repeat("*", min(16, len(runes)-8)) + string(runes[len(runes)-4:])
}

func containsNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
