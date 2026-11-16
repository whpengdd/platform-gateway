package cklogs

import (
	"regexp"
	"strings"
)

// IO-042 SMTP template: normalize transport line separators only for matching.
// Original errors remain untouched in the response.
var spamAdviceLine = regexp.MustCompile(`(?:rn|\r?\n)[45][0-9]{2}[- ]`)
var spamAdviceSpace = regexp.MustCompile(`\s+`)
var spamClassified = regexp.MustCompile(`\bwas classified as spam\b`)
var spamCheckAdvice = regexp.MustCompile(`\b(?:also,? )?ask your it dept to make sure your mailserver has reversedns, spf, dkim, and is not on any black lists\b`)
var spamRemainingBlacklist = regexp.MustCompile(`blacklist|black list|blocklist|block list|\brbl\b|\bdnsbl\b|spamhaus|\bspf_reject\b`)

func isSpamAdviceRejection(raw string) bool {
	normalized := strings.TrimSpace(spamAdviceSpace.ReplaceAllString(spamAdviceLine.ReplaceAllString(strings.ToLower(raw), " "), " "))
	if !spamClassified.MatchString(normalized) || !spamCheckAdvice.MatchString(normalized) {
		return false
	}
	return !spamRemainingBlacklist.MatchString(spamCheckAdvice.ReplaceAllString(normalized, ""))
}
