package security

import (
	"testing"
	"time"
)

// #102 nit 2: the expiry boundary with an injected clock. now is read once, so
// expires == now+max is accepted, one nanosecond later is rejected, and an
// expiry at now itself is rejected.
func TestTokenExpiryBoundaryInjectedClock(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, max := range []time.Duration{time.Hour, DefaultTokenMaxLifetime, 168 * time.Hour, TokenMaxLifetimeCeiling} {
		cases := []struct {
			name    string
			expires time.Time
			ok      bool
		}{
			{"at_now", now, false},
			{"before_now", now.Add(-time.Nanosecond), false},
			{"just_after_now", now.Add(time.Nanosecond), true},
			{"at_max", now.Add(max), true},
			{"after_max", now.Add(max + time.Nanosecond), false},
		}
		for _, c := range cases {
			r, err := newRemoteAt("https://bridge.example", "operator", c.expires, max, now)
			if (err == nil) != c.ok {
				t.Errorf("max %s, %s: err = %v, want ok=%v", max, c.name, err, c.ok)
			}
			if c.ok && (r == nil || !r.expires.Equal(c.expires) || r.maxLifetime != max) {
				t.Errorf("max %s, %s: remote %+v", max, c.name, r)
			}
		}
	}
	// The default-lifetime error text is unchanged.
	if _, err := newRemoteAt("https://bridge.example", "operator", now.Add(DefaultTokenMaxLifetime+time.Nanosecond), DefaultTokenMaxLifetime, now); err == nil || err.Error() != "named principal and future token expiry within 24 hours required" {
		t.Fatalf("default text: %v", err)
	}
}
