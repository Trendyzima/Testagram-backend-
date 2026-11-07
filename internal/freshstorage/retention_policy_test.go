package freshstorage

import ("testing"; "time")

func TestRetentionDecisionsRespectCutoffAndPreservePermanentPosts(t *testing.T) {
	cutoff:=time.Date(2026,11,7,0,0,0,0,time.UTC)
	p:=RetentionPolicy{Enabled:true,StartAt:cutoff,StoryTTL:24*time.Hour,ArchiveAfter:30*24*time.Hour,RecoveryWindow:30*24*time.Hour}
	now:=cutoff.Add(31*24*time.Hour)
	cases:=[]struct{name string;c RetentionCandidate;want RetentionAction}{
		{"legacy excluded",RetentionCandidate{ObjectID:"old",Class:"story",Scope:"story_media",CreatedAt:cutoff.Add(-time.Second)},""},
		{"permanent post archived not purged",RetentionCandidate{ObjectID:"post",Class:"permanent",Scope:"post_media",CreatedAt:cutoff},RetentionArchive},
		{"story expires",RetentionCandidate{ObjectID:"story",Class:"story",Scope:"story_media",CreatedAt:cutoff},RetentionExpireStory},
		{"fresh deletion event applies to older media",RetentionCandidate{ObjectID:"deleted",Class:"user_deleted",Scope:"post_media",CreatedAt:cutoff.Add(-time.Hour),DeletedAt:cutoff},RetentionPurgeDeleted},
		{"unknown class ignored",RetentionCandidate{ObjectID:"avatar",Class:"avatar",CreatedAt:cutoff},""},
		{"profile media excluded",RetentionCandidate{ObjectID:"profile",Class:"permanent",Scope:"profile_media",CreatedAt:cutoff},""},
		{"direct messages excluded",RetentionCandidate{ObjectID:"dm",Class:"user_deleted",Scope:"direct_message",CreatedAt:cutoff,DeletedAt:cutoff},""},
	}
	for _,tc:=range cases{t.Run(tc.name,func(t *testing.T){got,ok:=p.Decide(tc.c,now);if tc.want==""{if ok{t.Fatalf("unexpected decision: %#v",got)};return};if !ok||got.Action!=tc.want||got.JobKey==""{t.Fatalf("got=%#v ok=%v want=%s",got,ok,tc.want)}})}
}
func TestRetentionExactBoundaryAndStableJobKey(t *testing.T){
	start:=time.Date(2026,1,1,0,0,0,0,time.UTC);p:=RetentionPolicy{Enabled:true,StartAt:start,StoryTTL:24*time.Hour}
	c:=RetentionCandidate{ObjectID:"story1",Class:"story",Scope:"story_media",CreatedAt:start}
	if _,ok:=p.Decide(c,start.Add(24*time.Hour-time.Nanosecond));ok{t.Fatal("expired before 24 hours")}
	a,ok:=p.Decide(c,start.Add(24*time.Hour));if !ok||a.Action!=RetentionExpireStory{t.Fatalf("exact boundary not selected: %#v",a)}
	b,ok:=p.Decide(c,start.Add(48*time.Hour));if !ok||a.JobKey!=b.JobKey{t.Fatal("job key is not stable across retries")}
}
func TestRetentionJobKeySeparatesActionAndVersion(t *testing.T){
	a:=RetentionJobKey("obj",RetentionArchive,"v1")
	if a==RetentionJobKey("obj",RetentionPurgeDeleted,"v1")||a==RetentionJobKey("obj",RetentionArchive,"v2")||a!=RetentionJobKey("obj",RetentionArchive,"v1"){t.Fatal("job key must be stable and scoped")}
}
