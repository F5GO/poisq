package securetransport

import (
	"encoding/base64"
	"strings"
	"testing"
)

var testInstallationID = [16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

func testPublicKey() [32]byte {
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestPairingCodeSwiftVector(t *testing.T) {
	const want = "poisq-secure:v1:00112233445566778899aabbccddeeff:AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA:aHR0cHM6Ly9tdXNpYy5leGFtcGxlLmNvbTo4NDQz"

	got, err := PairingCode(" https://Music.Example.com:8443/ ", testInstallationID, testPublicKey())
	if err != nil || got != want {
		t.Fatalf("PairingCode = %q, %v; want %q", got, err, want)
	}

	defaultPort, err := PairingCode("https://music.example.com:443/", testInstallationID, testPublicKey())
	if wantEnd := ":" + base64.RawURLEncoding.EncodeToString([]byte("https://music.example.com")); err != nil || !strings.HasSuffix(defaultPort, wantEnd) {
		t.Fatalf("default port was not normalized: %q, %v", defaultPort, err)
	}
}

func TestPairingCodeRejectsInvalidInputs(t *testing.T) {
	for _, origin := range []string{
		"http://music.example.com",
		"https://user@music.example.com",
		"https://music.example.com/subsonic",
		"https://music.example.com/%2F",
		"https://music.example.com?",
		"https://music.example.com#",
		"https://music.example.com:0",
		"https://music.example.com:65536",
		"https://music.example.com:0443",
		"https://music.example.com:",
		"https://bad_host.example.com",
		"https://K.example.com",
		"https://-host.example.com",
		"https://music.example.com.",
	} {
		t.Run(origin, func(t *testing.T) {
			if code, err := PairingCode(origin, testInstallationID, testPublicKey()); err == nil || code != "" {
				t.Fatalf("invalid origin produced %q, %v", code, err)
			}
		})
	}

	if _, err := PairingCode("https://music.example.com", [16]byte{}, testPublicKey()); err == nil {
		t.Error("accepted a zero installation ID")
	}
	if _, err := PairingCode("https://music.example.com", testInstallationID, [32]byte{}); err == nil {
		t.Error("accepted a zero public key")
	}
}
