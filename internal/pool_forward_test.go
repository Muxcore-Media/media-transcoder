package internal

import (
	"os"
	"testing"
)

func TestPoolEnabled(t *testing.T) {
	t.Setenv("TRANSCODER_USE_POOL", "1")
	m := NewModule(Config{})
	if !m.poolEnabled() {
		t.Fatal("expected pool enabled")
	}
}

func TestPoolInsecureMode(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	if !poolInsecureMode() {
		t.Fatal("expected insecure mode")
	}
	opts := NewModule(Config{}).poolDialOptions()
	if len(opts) != 1 {
		t.Fatalf("opts=%d", len(opts))
	}
}

func TestEnqueueFallsBackWhenPoolDisabled(t *testing.T) {
	t.Setenv("TRANSCODER_USE_POOL", "0")
	m := NewModule(Config{})
	if m.poolEnabled() {
		t.Fatal("pool should be disabled")
	}
}

func TestPoolDialOptionsSecure(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "false")
	t.Setenv("MUXCORE_GRPC_INSECURE", "false")
	opts := NewModule(Config{}).poolDialOptions()
	if len(opts) != 1 {
		t.Fatalf("expected TLS dial option, got %d", len(opts))
	}
}

func TestPoolAddrFromEnv(t *testing.T) {
	t.Setenv("TRANSCODER_POOL_ADDR", "127.0.0.1:9530")
	m := NewModule(Config{})
	addr, err := m.poolAddr(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:9530" {
		t.Fatalf("addr=%q", addr)
	}
}

func TestPoolAddrMissingMesh(t *testing.T) {
	m := NewModule(Config{})
	_, err := m.poolAddr(t.Context())
	if err == nil {
		t.Fatal("expected error without mesh client")
	}
}

func TestPoolEnabledVariants(t *testing.T) {
	for _, v := range []string{"true", "yes", "TRUE"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TRANSCODER_USE_POOL", v)
			if !NewModule(Config{}).poolEnabled() {
				t.Fatalf("expected enabled for %q", v)
			}
		})
	}
	t.Setenv("TRANSCODER_USE_POOL", "")
	if NewModule(Config{DBPath: os.DevNull}).poolEnabled() {
		t.Fatal("expected disabled by default")
	}
}
