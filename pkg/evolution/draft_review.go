package evolution

import (
	"regexp"
	"strings"
)

type DraftReviewResult struct {
	Status      DraftStatus
	Findings    []string
	ReviewNotes []string
}

func ReviewDraft(draft SkillDraft) DraftReviewResult {
	findings := append([]string(nil), ValidateDraft(draft)...)
	findings = append(findings, scanDraftContent(draft)...)

	result := DraftReviewResult{
		Status:      DraftStatusCandidate,
		Findings:    findings,
		ReviewNotes: []string{"local structural validation completed"},
	}
	if len(findings) > 0 {
		result.Status = DraftStatusQuarantined
	}
	return result
}

// draftSecretPatterns match on the lowercased body: fixed prefixes for
// common credential formats (OpenAI/Stripe/GitHub/Slack/AWS) plus PEM
// private-key headers including algorithm-specific variants
// (RSA/EC/OPENSSH/ENCRYPTED), which the previous plain-substring check
// missed. Substring obfuscation can still slip past — this is a cheap
// guardrail, not a real secret scanner.
var draftSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk[-_](live|test|proj)[-_]`),
	regexp.MustCompile(`akia[0-9a-z]{16}`),
	regexp.MustCompile(`gh[pousr]_[a-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[a-z0-9_]{20,}`),
	regexp.MustCompile(`xox[baprs]-[a-z0-9-]{10,}`),
	regexp.MustCompile(`-----begin [a-z ]*private key-----`),
	regexp.MustCompile(`(api[_-]?key|secret|token|password)["']?\s*[:=]\s*["'][a-z0-9_\-]{16,}`),
}

func scanDraftContent(draft SkillDraft) []string {
	body := strings.ToLower(draft.BodyOrPatch)
	findings := make([]string, 0, 2)

	if strings.Contains(body, "sk-live-") || strings.Contains(body, "sk_test_") || strings.Contains(body, "api_key=") {
		findings = append(findings, "secret-like token detected in body_or_patch")
	}
	for _, pattern := range draftSecretPatterns {
		if pattern.MatchString(body) {
			findings = append(findings, "credential-pattern detected in body_or_patch: "+pattern.String())
			break
		}
	}

	return findings
}
