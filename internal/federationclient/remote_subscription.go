package federationclient

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

type ResolveRemoteAuthorRequest struct {
	LocalUserID string `json:"local_user_id"`
	Protocol    string `json:"protocol"`
	Identity    string `json:"identity"`
}

type ResolveRemoteAuthorResult struct {
	Protocol            string `json:"protocol"`
	Identity            string `json:"identity"`
	Handle              string `json:"handle"`
	VerificationStatus  string `json:"verification_status"`
}

// ResolveRemoteAuthor asks the private Federation service to resolve the
// canonical remote actor/DID and persist the verified result through Backend's
// private callback. It never creates a native account.
func (c *Client) ResolveRemoteAuthor(ctx context.Context, input ResolveRemoteAuthorRequest) (ResolveRemoteAuthorResult, error) {
	if c == nil {
		return ResolveRemoteAuthorResult{}, errors.New("Federation client is not configured")
	}
	input.LocalUserID = strings.TrimSpace(input.LocalUserID)
	input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	input.Identity = strings.TrimSpace(input.Identity)
	if len(input.LocalUserID) != 36 || input.Protocol != "activitypub" && input.Protocol != "bluesky" || len(input.Identity) < 12 || len(input.Identity) > 2048 {
		return ResolveRemoteAuthorResult{}, errors.New("invalid remote author resolution request")
	}
	switch input.Protocol {
	case "activitypub":
		u, err := url.Parse(input.Identity)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return ResolveRemoteAuthorResult{}, errors.New("ActivityPub actor must be a canonical HTTPS URI")
		}
	case "bluesky":
		if !validATProtoDID(input.Identity) {
			return ResolveRemoteAuthorResult{}, errors.New("Bluesky identity must be a valid DID")
		}
	}
	var envelope struct {
		OK   bool                     `json:"ok"`
		Data ResolveRemoteAuthorResult `json:"data"`
	}
	if err := c.postInternalJSON(ctx, "/internal/v1/remote-subscriptions/verify", input, &envelope); err != nil {
		return ResolveRemoteAuthorResult{}, err
	}
	if !envelope.OK || envelope.Data.Protocol != input.Protocol || envelope.Data.Identity != input.Identity ||
		envelope.Data.Handle == "" || envelope.Data.VerificationStatus != "verified" {
		return ResolveRemoteAuthorResult{}, errors.New("Federation returned an invalid remote author verification result")
	}
	return envelope.Data, nil
}
