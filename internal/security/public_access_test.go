package security

import (
	"net/url"
	"strings"
	"testing"
)

func TestPublicAccessClosedByDefault(t *testing.T) {
	var nilAccess *PublicAccess
	if nilAccess.NavidromeWebOpen() || (&PublicAccess{}).NavidromeWebOpen() {
		t.Fatal("Navidrome web must be closed by default")
	}
	a := &PublicAccess{}
	a.SetNavidromeWebOpen(true)
	if !a.NavidromeWebOpen() {
		t.Fatal("open setting not applied")
	}
	a.SetNavidromeWebOpen(false)
	if a.NavidromeWebOpen() {
		t.Fatal("close setting not applied")
	}
}

func TestIsSubsonicPath(t *testing.T) {
	allowed := []string{"/rest/ping.view", "/rest/ping", "/rest/stream", "/rest/getCoverArt.view", "/rest/poisqCastZones", "/rest/get_open-subsonic.view"}
	for _, raw := range allowed {
		u, err := url.Parse(raw + "?u=a&t=b")
		if err != nil || !IsSubsonicPath(u.Path, u.RawPath) {
			t.Errorf("rejected %q", raw)
		}
	}
	rejected := []string{
		"/", "/app", "/app/", "/api/song", "/auth/login", "/share/abc", "/rest", "/rest/",
		"/REST/ping", "/Rest/ping", "//rest/ping", "/rest//ping", "/rest/./ping", "/rest/../app", "/rest/ping/..",
		"/rest/a/b", "/rest/%2e%2e/app", "/rest%2Fping", "/rest/ping%2F..%2Fapp", "/%72est/ping", "/rest/ping%20",
		"/rest/ping\\..\\app", "/rest/ping;x", "/subsonic/rest/ping", "/rest/" + strings.Repeat("a", 260),
	}
	for _, raw := range rejected {
		u, err := url.Parse(raw)
		if err != nil {
			continue // такой путь не дойдёт до обработчика
		}
		if IsSubsonicPath(u.Path, u.RawPath) {
			t.Errorf("accepted %q (path %q raw %q)", raw, u.Path, u.RawPath)
		}
	}
}
