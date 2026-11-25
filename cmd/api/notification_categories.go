package main

import (
	"errors"
	"strings"
)

const notificationCategorySQL = `CASE
	WHEN n.kind = 'mention' OR n.kind LIKE '%.mention%' THEN 'mentions'
	WHEN n.kind LIKE 'wallet.%' THEN 'wallet'
	WHEN n.kind LIKE 'message.%' OR n.kind LIKE '%.message' THEN 'messages'
	WHEN n.kind LIKE '%follow%' THEN 'follows'
	WHEN n.kind LIKE '%reply%' OR n.kind LIKE '%comment%' THEN 'replies'
	WHEN n.kind LIKE '%repost%' OR n.kind LIKE '%share%' THEN 'reposts'
	WHEN n.kind LIKE '%reaction%' OR n.kind LIKE '%like%' THEN 'likes'
	ELSE 'system'
END`

var notificationCategories = map[string]struct{}{
	"all": {}, "mentions": {}, "likes": {}, "follows": {},
	"replies": {}, "reposts": {}, "messages": {}, "wallet": {}, "system": {},
}

func parseNotificationCategory(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "all"
	}
	if _, ok := notificationCategories[value]; !ok {
		return "", errors.New("unsupported notification category")
	}
	return value, nil
}

func notificationCategory(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch {
	case kind == "mention" || strings.Contains(kind, ".mention"):
		return "mentions"
	case strings.HasPrefix(kind, "wallet."):
		return "wallet"
	case strings.HasPrefix(kind, "message.") || strings.HasSuffix(kind, ".message"):
		return "messages"
	case strings.Contains(kind, "follow"):
		return "follows"
	case strings.Contains(kind, "reply") || strings.Contains(kind, "comment"):
		return "replies"
	case strings.Contains(kind, "repost") || strings.Contains(kind, "share"):
		return "reposts"
	case strings.Contains(kind, "reaction") || strings.Contains(kind, "like"):
		return "likes"
	default:
		return "system"
	}
}
