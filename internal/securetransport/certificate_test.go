package securetransport

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

type pairingVector struct {
	Pairing struct {
		Code           string `json:"code"`
		InstallationID string `json:"installationId"`
		RootPublicKey  string `json:"rootPublicKey"`
		Origin         string `json:"origin"`
		Fingerprint    string `json:"fingerprint"`
	} `json:"pairing"`
	Certificate struct {
		Hex            string `json:"hex"`
		Epoch          uint32 `json:"epoch"`
		NotBefore      int64  `json:"notBefore"`
		NotAfter       int64  `json:"notAfter"`
		WorkingKey     string `json:"workingKey"`
		NextRootDigest string `json:"nextRootDigest"`
	} `json:"certificate"`
}

func seq(start byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func loadVector(t *testing.T) pairingVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/pairing-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v pairingVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func vectorCertificate(t *testing.T) (WorkingCertificate, ed25519.PrivateKey) {
	t.Helper()
	root := ed25519.NewKeyFromSeed(seq(0x01))
	next := ed25519.NewKeyFromSeed(seq(0x21)).Public().(ed25519.PublicKey)
	working, err := ecdh.X25519().NewPrivateKey(seq(0x41))
	if err != nil {
		t.Fatal(err)
	}
	return WorkingCertificate{
		InstallationID: testInstallationID,
		Epoch:          7,
		NotBefore:      time.Unix(1767225600, 0).UTC(),
		NotAfter:       time.Unix(1767225600+7*86400, 0).UTC(),
		WorkingKey:     [32]byte(working.PublicKey().Bytes()),
		NextRootDigest: RootDigest(next),
	}, root
}

func TestSharedVectorMatchesGo(t *testing.T) {
	v := loadVector(t)
	cert, root := vectorCertificate(t)
	rootPublic := root.Public().(ed25519.PublicKey)

	code, err := PairingCode(v.Pairing.Origin, testInstallationID, [32]byte(rootPublic))
	if err != nil || code != v.Pairing.Code {
		t.Fatalf("pairing code = %q, %v", code, err)
	}
	if hex.EncodeToString(rootPublic) != v.Pairing.RootPublicKey || hex.EncodeToString(testInstallationID[:]) != v.Pairing.InstallationID {
		t.Fatal("pairing identity mismatch")
	}
	if got := KeyFingerprint(rootPublic); got != v.Pairing.Fingerprint {
		t.Fatalf("fingerprint = %q", got)
	}
	raw, err := cert.Sign(root)
	if err != nil || hex.EncodeToString(raw) != v.Certificate.Hex {
		t.Fatalf("certificate = %x, %v", raw, err)
	}
	parsed, err := ParseWorkingCertificate(raw, rootPublic, testInstallationID)
	if err != nil || parsed != cert {
		t.Fatalf("parse = %+v, %v", parsed, err)
	}
	if parsed.Epoch != v.Certificate.Epoch || parsed.NotBefore.Unix() != v.Certificate.NotBefore || parsed.NotAfter.Unix() != v.Certificate.NotAfter ||
		hex.EncodeToString(parsed.WorkingKey[:]) != v.Certificate.WorkingKey || hex.EncodeToString(parsed.NextRootDigest[:]) != v.Certificate.NextRootDigest {
		t.Fatal("certificate fields do not match the shared vector")
	}
}

func TestParseWorkingCertificateRejectsTampering(t *testing.T) {
	cert, root := vectorCertificate(t)
	rootPublic := root.Public().(ed25519.PublicKey)
	raw, err := cert.Sign(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0x01
		if _, err := ParseWorkingCertificate(bad, rootPublic, testInstallationID); err == nil {
			t.Fatalf("accepted a certificate with byte %d flipped", i)
		}
	}
	if _, err := ParseWorkingCertificate(raw[:len(raw)-1], rootPublic, testInstallationID); err == nil {
		t.Fatal("accepted a truncated certificate")
	}
	if _, err := ParseWorkingCertificate(append(raw, 0), rootPublic, testInstallationID); err == nil {
		t.Fatal("accepted a certificate with trailing data")
	}
	other := ed25519.NewKeyFromSeed(seq(0x61)).Public().(ed25519.PublicKey)
	if _, err := ParseWorkingCertificate(raw, other, testInstallationID); err == nil {
		t.Fatal("accepted a certificate signed by another root")
	}
	if _, err := ParseWorkingCertificate(raw, rootPublic, [16]byte{1}); err == nil {
		t.Fatal("accepted a certificate of another installation")
	}
}

func TestSignRejectsMalformedCertificates(t *testing.T) {
	base, root := vectorCertificate(t)
	for name, mutate := range map[string]func(*WorkingCertificate){
		"zero installation": func(c *WorkingCertificate) { c.InstallationID = [16]byte{} },
		"zero epoch":        func(c *WorkingCertificate) { c.Epoch = 0 },
		"zero key":          func(c *WorkingCertificate) { c.WorkingKey = [32]byte{} },
		"zero next root":    func(c *WorkingCertificate) { c.NextRootDigest = [32]byte{} },
		"inverted":          func(c *WorkingCertificate) { c.NotAfter = c.NotBefore.Add(-time.Second) },
		"too long":          func(c *WorkingCertificate) { c.NotAfter = c.NotBefore.Add(MaxCertificateLifetime + time.Second) },
		"before epoch":      func(c *WorkingCertificate) { c.NotBefore = time.Unix(0, 0) },
	} {
		c := base
		mutate(&c)
		if _, err := c.Sign(root); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	if _, err := base.Sign(ed25519.PrivateKey{1}); err == nil {
		t.Error("signed with a malformed root key")
	}
}

func TestWorkingCertificateValidAt(t *testing.T) {
	cert, _ := vectorCertificate(t)
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{cert.NotBefore.Add(-ClockSkew - time.Second), false},
		{cert.NotBefore.Add(-ClockSkew), true},
		{cert.NotBefore.Add(24 * time.Hour), true},
		{cert.NotAfter.Add(ClockSkew - time.Second), true},
		{cert.NotAfter.Add(ClockSkew), false},
	} {
		if got := cert.ValidAt(tc.at); got != tc.want {
			t.Errorf("ValidAt(%v) = %v, want %v", tc.at, got, tc.want)
		}
	}
}
