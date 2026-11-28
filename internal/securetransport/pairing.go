package securetransport

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// PairingCode encodes the public identity of one poisq Studio installation:
// its ID, the Ed25519 root public key that signs working-key certificates and its public HTTPS origin.
func PairingCode(publicOrigin string, installationID [16]byte, rootPublicKey [32]byte) (string, error) {
	origin, err := canonicalPairingOrigin(publicOrigin)
	if err != nil {
		return "", err
	}
	if installationID == [16]byte{} || rootPublicKey == [32]byte{} {
		return "", errors.New("secure pairing requires an installation ID and a root public key")
	}
	return "poisq-secure:v1:" + hex.EncodeToString(installationID[:]) + ":" +
		base64.RawURLEncoding.EncodeToString(rootPublicKey[:]) + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(origin)), nil
}

func canonicalPairingOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || strings.ContainsAny(raw, "?#") ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return "", errors.New("secure pairing requires an HTTPS origin without credentials, path, query or fragment")
	}
	host, port, hasPort := strings.Cut(u.Host, ":")
	if !validPairingHostname(host) {
		return "", errors.New("secure pairing requires an ASCII hostname")
	}
	host = strings.ToLower(host)
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", errors.New("secure pairing requires a valid TCP port")
		}
		if n != 443 {
			host += ":" + port
		}
	}
	return "https://" + host, nil
}

func validPairingHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
