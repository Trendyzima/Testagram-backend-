package main

import (
	"context"
	"log"
	"time"
)

// enqueueWatchdogImpulse records only a coarse event type. It deliberately
// carries no user ID, email, IP address, access token, or session identifier.
// A five-minute global bucket bounds duplicate activity while allowing a new
// event to refresh the service lease in a later window.
func (s *service) enqueueWatchdogImpulse(eventType string) {
	if s.db == nil { return }
	switch eventType {
	case "login", "feed_open", "post_publish", "media_upload", "media_view", "federation_interaction":
	default:
		return
	}
	bucket := time.Now().UTC().Truncate(5*time.Minute).Format("20060102T1504")
	key := "testagram:"+eventType+":"+bucket
	ctx,cancel:=context.WithTimeout(context.Background(),750*time.Millisecond)
	defer cancel()
	var id int64
	if err:=s.db.QueryRow(ctx,"SELECT testagram_watchdog.enqueue_impulse($1,$2)",key,eventType).Scan(&id);err!=nil {
		// Watchdog failure must never break login, feed reads, publishing,
		// uploads, or federation. The durable queue is an acceleration path.
		log.Printf("activity watchdog enqueue failed event=%s: %v",eventType,err)
	}
}
