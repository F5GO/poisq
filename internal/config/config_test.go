package config

import (
	"testing"
	"time"
)

func TestBiographyFirstRequestWaitBounds(t *testing.T) {
	for name, raw := range map[string]string{"too short": "4s", "too long": "31s", "invalid": "later"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BIO_FIRST_REQUEST_WAIT", raw)
			cfg, err := Load()
			if err != nil || cfg.BiographyFirstRequestWait != 25*time.Second {
				t.Fatalf("raw=%q wait=%s err=%v", raw, cfg.BiographyFirstRequestWait, err)
			}
		})
	}
	for _, raw := range []string{"5s", "30s"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("BIO_FIRST_REQUEST_WAIT", raw)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want, _ := time.ParseDuration(raw)
			if cfg.BiographyFirstRequestWait != want {
				t.Fatalf("wait=%s want=%s", cfg.BiographyFirstRequestWait, want)
			}
		})
	}
}
