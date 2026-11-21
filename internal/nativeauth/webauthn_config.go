package nativeauth

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// WebAuthnConfigurationFingerprint binds the Backend and Admin Console to the
// same relying-party ID and exact allowed origins. Origin order is irrelevant;
// origin values themselves are not normalized beyond surrounding whitespace.
func WebAuthnConfigurationFingerprint(rpid string, origins []string) string {
	rpid = strings.ToLower(strings.TrimSpace(rpid))
	if rpid == "" || len(origins) == 0 {
		return ""
	}
	canonicalOrigins := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			return ""
		}
		canonicalOrigins = append(canonicalOrigins, origin)
	}
	sort.Strings(canonicalOrigins)
	sum := sha256.Sum256([]byte(rpid + "\n" + strings.Join(canonicalOrigins, "\n")))
	return hex.EncodeToString(sum[:])
}
