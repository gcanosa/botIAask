package uploads

import "time"

// MaxExpiryDays is the longest expiry window accepted for a new upload (10 years).
const MaxExpiryDays = 3650

const maxExpiryDays = 36500

// AccessExpiresAt returns when public access ends (after approval + expires_in_days).
// ok is false when the item does not expire (no window or not yet approved).
func (u *Upload) AccessExpiresAt() (exp time.Time, ok bool) {
	if u == nil || u.ExpiresInDays <= 0 || !u.ApprovedAt.Valid {
		return time.Time{}, false
	}
	// time.Duration overflows past ~292 years; older rows may hold absurd values.
	days := min(u.ExpiresInDays, maxExpiryDays)
	return u.ApprovedAt.Time.Add(time.Duration(days) * 24 * time.Hour), true
}

// IsAccessExpired reports whether public access has ended at now.
func (u *Upload) IsAccessExpired(now time.Time) bool {
	exp, ok := u.AccessExpiresAt()
	if !ok {
		return false
	}
	return now.After(exp)
}
