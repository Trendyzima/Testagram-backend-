// Package profilestorage selects the first storage tier for profile images.
// Appwrite is preferred while its reserved capacity threshold is respected;
// Instabox VPS is the automatic overflow tier.
package profilestorage

import (
	"errors"
	"math"
)

type Tier string

const (
	TierAppwrite Tier = "appwrite"
	TierInstabox Tier = "instabox-vps"
)

type Capacity struct {
	LimitBytes        int64
	UsedBytes         int64
	ReservedBytes     int64
	SafetyReserveBytes int64
}

func (c Capacity) Validate() error {
	if c.LimitBytes <= 0 || c.UsedBytes < 0 || c.ReservedBytes < 0 ||
		c.SafetyReserveBytes < 0 || c.UsedBytes > c.LimitBytes ||
		c.UsedBytes > math.MaxInt64-c.ReservedBytes {
		return errors.New("invalid profile storage capacity")
	}
	if c.SafetyReserveBytes >= c.LimitBytes {
		return errors.New("profile storage safety reserve must be smaller than the limit")
	}
	return nil
}

// SelectTier reserves room for already-issued uploads and the new object.
// It switches before the provider quota is exhausted. The caller must persist
// the reservation transactionally before starting the upload.
func SelectTier(c Capacity, incomingBytes int64) (Tier, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if incomingBytes <= 0 {
		return "", errors.New("incoming profile image size must be positive")
	}
	threshold := c.LimitBytes - c.SafetyReserveBytes
	used := c.UsedBytes + c.ReservedBytes
	if used >= threshold || incomingBytes > threshold-used {
		return TierInstabox, nil
	}
	return TierAppwrite, nil
}
