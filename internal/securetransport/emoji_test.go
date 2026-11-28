package securetransport

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type emojiVector struct {
	Comment  string `json:"comment"`
	Label    string `json:"label"`
	Table    string `json:"table"`
	Examples []struct {
		RootPublicKey string `json:"rootPublicKey"`
		Emoji         string `json:"emoji"`
	} `json:"examples"`
}

func TestEmojiSharedVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/emoji-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v emojiVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Table != emojiTable || v.Label != emojiLabel {
		t.Fatal("emoji table or label differs from the shared vector")
	}
	seen := map[string]bool{}
	for _, e := range emojiList {
		if seen[e] {
			t.Fatalf("duplicate emoji %q", e)
		}
		seen[e] = true
	}
	for _, ex := range v.Examples {
		root, _ := hex.DecodeString(ex.RootPublicKey)
		if got := strings.Join(KeyEmoji(ed25519.PublicKey(root)), ""); got != ex.Emoji {
			t.Fatalf("KeyEmoji(%s) = %s, want %s", ex.RootPublicKey[:8], got, ex.Emoji)
		}
	}
}
