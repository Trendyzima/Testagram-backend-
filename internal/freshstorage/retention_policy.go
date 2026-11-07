package freshstorage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

const RetentionPolicyVersion = "2026-11-v1"

type RetentionAction string

const (
	RetentionExpireStory RetentionAction = "expire_story"
	RetentionArchive RetentionAction = "archive"
	RetentionPurgeDeleted RetentionAction = "purge_deleted"
)

type RetentionPolicy struct {
	Enabled bool
	DryRun bool
	StartAt time.Time
	StoryTTL time.Duration
	ArchiveAfter time.Duration
	RecoveryWindow time.Duration
	BatchSize int
}

type RetentionCandidate struct {
	ObjectID string
	Class string
	Scope string // post_media or story_media; direct messages and profile media are excluded
	CreatedAt time.Time
	DeletedAt time.Time
	StoryExpiry time.Time
}

type RetentionDecision struct {
	Action RetentionAction
	DueAt time.Time
	JobKey string
}

// RetentionPolicyFromEnv fails closed: destructive work requires an explicit
// enable flag, dry-run=false, and a timezone-qualified start cutoff.
func RetentionPolicyFromEnv() (RetentionPolicy, error) {
	p := RetentionPolicy{DryRun:true, StoryTTL:24*time.Hour, ArchiveAfter:30*24*time.Hour, RecoveryWindow:30*24*time.Hour, BatchSize:100}
	p.Enabled = strings.EqualFold(strings.TrimSpace(os.Getenv("TESTAGRAM_RETENTION_ENABLED")), "true")
	if raw:=strings.TrimSpace(os.Getenv("TESTAGRAM_RETENTION_DRY_RUN")); raw!="" {
		v,err:=strconv.ParseBool(raw); if err!=nil{return p,errors.New("TESTAGRAM_RETENTION_DRY_RUN must be boolean")}; p.DryRun=v
	}
	if raw:=strings.TrimSpace(os.Getenv("TESTAGRAM_RETENTION_START_AT")); raw!="" {
		v,err:=time.Parse(time.RFC3339,raw); if err!=nil{return p,errors.New("TESTAGRAM_RETENTION_START_AT must be RFC3339 with timezone")}; p.StartAt=v.UTC()
	}
	for _,item:=range []struct{name string; dst *time.Duration; days bool; max int}{
		{"TESTAGRAM_STORY_TTL_HOURS",&p.StoryTTL,false,24},
		{"TESTAGRAM_MEDIA_ARCHIVE_AFTER_DAYS",&p.ArchiveAfter,true,3650},
		{"TESTAGRAM_USER_DELETION_RECOVERY_DAYS",&p.RecoveryWindow,true,3650},
	}{
		if raw:=strings.TrimSpace(os.Getenv(item.name)); raw!="" {
			n,err:=strconv.Atoi(raw); if err!=nil||n<1||n>item.max{return p,errors.New(item.name+" is outside the allowed positive range")}
			unit:=time.Hour; if item.days {unit=24*time.Hour}; *item.dst=time.Duration(n)*unit
		}
	}
	if raw:=strings.TrimSpace(os.Getenv("TESTAGRAM_RETENTION_BATCH_SIZE")); raw!="" {
		n,err:=strconv.Atoi(raw); if err!=nil||n<1||n>100{return p,errors.New("TESTAGRAM_RETENTION_BATCH_SIZE must be between 1 and 100")}; p.BatchSize=n
	}
	if p.Enabled && p.StartAt.IsZero(){return p,errors.New("retention cannot be enabled without TESTAGRAM_RETENTION_START_AT cutoff")}
	return p,nil
}

// Decide is pure and side-effect-free. Existing records are excluded from
// archive/story expiry by CreatedAt. A fresh user deletion event is eligible
// even when the underlying post predates the rollout cutoff.
func (p RetentionPolicy) Decide(c RetentionCandidate, now time.Time) (RetentionDecision,bool) {
	if !p.Enabled||p.StartAt.IsZero()||c.ObjectID=="" {return RetentionDecision{},false}
	var action RetentionAction
	var due,eligibilityTime time.Time
	switch c.Class {
	case "permanent":
		if c.Scope != "post_media" { return RetentionDecision{}, false }
		action=RetentionArchive; eligibilityTime=c.CreatedAt; due=c.CreatedAt.UTC().Add(p.ArchiveAfter)
	case "story":
		if c.Scope != "story_media" { return RetentionDecision{}, false }
		action=RetentionExpireStory; eligibilityTime=c.CreatedAt; due=c.StoryExpiry
		if due.IsZero(){due=c.CreatedAt.UTC().Add(p.StoryTTL)}
	case "user_deleted":
		if c.Scope != "post_media" && c.Scope != "story_media" { return RetentionDecision{}, false }
		if c.DeletedAt.IsZero(){return RetentionDecision{},false}
		action=RetentionPurgeDeleted; eligibilityTime=c.DeletedAt; due=c.DeletedAt.UTC().Add(p.RecoveryWindow)
	default:
		return RetentionDecision{},false
	}
	if eligibilityTime.IsZero()||eligibilityTime.Before(p.StartAt)||due.IsZero()||now.UTC().Before(due.UTC()){return RetentionDecision{},false}
	return RetentionDecision{Action:action,DueAt:due.UTC(),JobKey:RetentionJobKey(c.ObjectID,action,RetentionPolicyVersion)},true
}

func RetentionJobKey(objectID string, action RetentionAction, version string) string {
	sum:=sha256.Sum256([]byte(strings.TrimSpace(objectID)+"\x00"+string(action)+"\x00"+strings.TrimSpace(version)))
	return hex.EncodeToString(sum[:])
}
