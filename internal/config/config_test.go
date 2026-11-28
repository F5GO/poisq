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

func TestTrustedProxies(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.5, ::1")
	cfg, err := Load()
	if err != nil || len(cfg.TrustedProxies) != 3 || cfg.TrustedProxies[1].String() != "192.168.1.5/32" {
		t.Fatalf("cfg=%v err=%v", cfg.TrustedProxies, err)
	}
	t.Setenv("TRUSTED_PROXIES", "none")
	if cfg, err := Load(); err != nil || len(cfg.TrustedProxies) != 0 {
		t.Fatalf("none: %v %v", cfg.TrustedProxies, err)
	}
	t.Setenv("TRUSTED_PROXIES", "example.com")
	if _, err := Load(); err == nil {
		t.Fatal("мусор в TRUSTED_PROXIES должен останавливать запуск")
	}
}

func TestLANListenAddrIsOptIn(t *testing.T) {
	t.Setenv("LAN_LISTEN_ADDR", "")
	cfg, err := Load()
	if err != nil || cfg.LANListenAddr != "" {
		t.Fatalf("LAN port must be off by default: %q %v", cfg.LANListenAddr, err)
	}
	t.Setenv("LAN_LISTEN_ADDR", " :4536 ")
	if cfg, err := Load(); err != nil || cfg.LANListenAddr != ":4536" {
		t.Fatalf("LAN_LISTEN_ADDR: %q %v", cfg.LANListenAddr, err)
	}
}
