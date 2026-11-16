package freshapi

import "context"

// ContentModeration is the Testagram-owned server-side policy boundary for post creation.
// No network-backed or third-party implementation is shipped or selected by configuration.
// Post creation must fail closed when no approved Testagram implementation is installed.
type ContentModeration interface {
	Analyze(context.Context, string) (ModerationResult, error)
}

type ModerationResult struct {
	OverallScore int            `json:"overall_score"`
	Categories   map[string]int `json:"categories"`
	Action       string         `json:"action"`
	Reason       string         `json:"reason"`
}
