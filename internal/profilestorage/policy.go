// Package profilestorage decides where profile pictures and avatars are placed.
// Appwrite is the preferred tier; Instabox VPS is the automatic overflow tier.
package profilestorage

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

type Tier string

const (
	TierAppwrite Tier = "appwrite"
	TierInstabox Tier = "instabox-vps"
)

const DefaultAppwriteCapacityBytes int64 = 2 * 1024 * 1024 * 1024
const DefaultSafetyReserveBytes int64 = 256 * 1024 * 1024

type Policy struct {
	AppwriteCapacityBytes int64
	SafetyReserveBytes    int64
}

func DefaultPolicy() Policy {
	return Policy{
		AppwriteCapacityBytes: DefaultAppwriteCapacityBytes,
		SafetyReserveBytes: DefaultSafetyReserveBytes,
	}
}

// AppwriteLimit is the high-water mark. The safety reserve leaves room for
// provider-side accounting lag and files created outside Testagram.
func (p Policy) AppwriteLimit() (int64, error) {
	if p.AppwriteCapacityBytes <= 0 || p.SafetyReserveBytes < 0 || p.SafetyReserveBytes >= p.AppwriteCapacityBytes {
		return 0, errors.New("invalid Appwrite profile storage capacity or safety reserve")
	}
	return p.AppwriteCapacityBytes - p.SafetyReserveBytes, nil
}

// ChooseTier includes bytes already used, bytes reserved by concurrent
// uploads, and the proposed upload. The caller must serialize reservations
// per bucket in a database transaction/advisory lock before calling this.
func (p Policy) ChooseTier(appwriteUsed, appwriteReserved, uploadBytes int64) (Tier, error) {
	limit, err := p.AppwriteLimit()
	if err != nil { return "", err }
	if appwriteUsed < 0 || appwriteReserved < 0 || uploadBytes <= 0 {
		return "", errors.New("storage usage, reservation and upload size must be valid")
	}
	if appwriteUsed > math.MaxInt64-appwriteReserved || appwriteUsed+appwriteReserved > math.MaxInt64-uploadBytes {
		return "", errors.New("storage usage arithmetic overflow")
	}
	if appwriteUsed+appwriteReserved+uploadBytes <= limit {
		return TierAppwrite, nil
	}
	return TierInstabox, nil
}

func ParsePolicy(capacity, reserve string) (Policy, error) {
	p := DefaultPolicy()
	if strings.TrimSpace(capacity) != "" {
		if value, err := strconv.ParseInt(strings.TrimSpace(capacity), 10, 64); err != nil { return Policy{}, errors.New("invalid Appwrite capacity bytes") } else { p.AppwriteCapacityBytes = value }
	}
	if strings.TrimSpace(reserve) != "" {
		if value, err := strconv.ParseInt(strings.TrimSpace(reserve), 10, 64); err != nil { return Policy{}, errors.New("invalid Appwrite safety reserve bytes") } else { p.SafetyReserveBytes = value }
	}
	if _, err := p.AppwriteLimit(); err != nil { return Policy{}, err }
	return p, nil
}
