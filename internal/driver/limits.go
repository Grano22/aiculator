package driver

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A harness that runs out of plan usage, credit or rate allowance fails every task the same
// way until the limit resets, so retrying only burns tasks. These are the words harnesses
// use when that happens; the swarm then hands the task back with no penalty and leaves the
// agent alone until the reset.
var limitPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?i)usage limit|limit reached|(5|five)[- ]hour limit|weekly limit|plan limit`), "usage_limit"},
	{regexp.MustCompile(`(?i)credit balance is too low|out of credits?|insufficient[_ ]credits?|no credits? (left|remaining)`), "credit_exhausted"},
	{regexp.MustCompile(`(?i)insufficient_quota|quota (exceeded|exhausted)|exceeded your current quota`), "quota_exceeded"},
	{regexp.MustCompile(`(?i)billing|payment required|\b402\b`), "billing"},
	{regexp.MustCompile(`(?i)rate[ _-]?limit|too many requests|\b429\b|overloaded`), "rate_limited"},
}

var (
	// Claude Code says "...limit reached|1730000000", with the reset as a Unix time.
	epochRE = regexp.MustCompile(`\|(\d{10})\b`)
	rfcRE   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	// "try again in 2h 30m", "retry after 60 seconds", "resets in 45 minutes".
	inRE   = regexp.MustCompile(`(?i)(?:in|after)\s+((?:\d+\s*(?:h|hours?|m|min|minutes?|s|sec|seconds?)\s*)+)`)
	partRE = regexp.MustCompile(`(?i)(\d+)\s*(h|hours?|m|min|minutes?|s|sec|seconds?)`)
)

// maxLimitWait bounds how long a reset time read from a message is believed.
const maxLimitWait = 8 * 24 * time.Hour

// limitIn reads a usage limit from what a harness said, and when it resets if it said.
func limitIn(text string, now time.Time) (reason string, until time.Time, ok bool) {
	for _, p := range limitPatterns {
		if p.re.MatchString(text) {
			reason, ok = p.reason, true
			break
		}
	}
	if !ok {
		return "", time.Time{}, false
	}
	switch {
	case epochRE.MatchString(text):
		n, _ := strconv.ParseInt(epochRE.FindStringSubmatch(text)[1], 10, 64)
		until = time.Unix(n, 0)
	case rfcRE.MatchString(text):
		until, _ = time.Parse(time.RFC3339, rfcRE.FindString(text))
	case inRE.MatchString(text):
		var d time.Duration
		for _, m := range partRE.FindAllStringSubmatch(inRE.FindStringSubmatch(text)[1], -1) {
			n, _ := strconv.Atoi(m[1])
			switch u := strings.ToLower(m[2]); {
			case strings.HasPrefix(u, "h"):
				d += time.Duration(n) * time.Hour
			case strings.HasPrefix(u, "m"):
				d += time.Duration(n) * time.Minute
			default:
				d += time.Duration(n) * time.Second
			}
		}
		until = now.Add(d)
	}
	if !until.IsZero() && (until.Before(now) || until.Sub(now) > maxLimitWait) {
		until = time.Time{} // Not believable, so not used.
	}
	if until.IsZero() && reason == "rate_limited" {
		until = now.Add(time.Minute) // Rate limits pass quickly; try again soon.
	}
	return reason, until, true
}

// strictOutput are the phrasings that only a harness reporting its own limit uses. The text an
// agent writes about its task is checked against these alone: a task about rate limiting or
// billing would otherwise read as the harness running out.
var strictOutput = regexp.MustCompile(`(?i)limit reached\|\d{10}|credit balance is too low|insufficient_quota|you've hit your (usage )?limit`)

// limitInOutput is limitIn for an agent's own text, which only counts on a strict match.
func limitInOutput(text string, now time.Time) (reason string, until time.Time, ok bool) {
	if !strictOutput.MatchString(text) {
		return "", time.Time{}, false
	}
	return limitIn(text, now)
}

// limitOf checks errors and stderr fully, and the agent's text strictly.
func limitOf(errs, output string, now time.Time) (string, time.Time) {
	if r, u, ok := limitIn(errs, now); ok {
		return r, u
	}
	r, u, _ := limitInOutput(output, now)
	return r, u
}
