package securetransport

import (
	"testing"
	"time"
)

func TestRateLimiterSurvivesClockGoingBack(t *testing.T) {
	l := newRateLimiter(60, 2, 10)
	now := time.Unix(1_800_000_000, 0)
	if !l.allow("a", now) || !l.allow("a", now) || l.allow("a", now) {
		t.Fatal("burst not enforced")
	}
	if !l.allow("a", now.Add(time.Second)) {
		t.Fatal("refill not applied")
	}
	if l.allow("a", now.Add(-time.Hour)) {
		t.Fatal("going back in time must not refill")
	}
	if !l.allow("a", now.Add(3*time.Second)) {
		t.Fatal("clock going back drained the bucket")
	}
	for i := range 10 {
		l.allow(string(rune('b'+i)), now)
	}
	if l.allow("new", now) {
		t.Fatal("full table must refuse new keys instead of evicting active ones")
	}
}
