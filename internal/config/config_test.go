package config

import "testing"

func TestConfigDefaultsAndValidate(t *testing.T) {
	cfg := Config{
		DeviceName: "HOME-DESKTOP",
		DataDir:    `C:\SyncGate`,
		Shares: []ShareConfig{{
			ID:       "drop",
			Name:     "Drop",
			RootPath: `C:\SyncGate\Drop`,
			Mode:     "upload_only",
		}},
	}

	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	if cfg.LocalAPI.Host != DefaultLocalAPIHost {
		t.Fatalf("default local API host = %q", cfg.LocalAPI.Host)
	}
	if cfg.Transfer.ChunkSizeBytes != 4*1024*1024 {
		t.Fatalf("default chunk size = %d", cfg.Transfer.ChunkSizeBytes)
	}
}

func TestConfigRejectsNonLoopbackLocalAPI(t *testing.T) {
	cfg := Config{
		DeviceName: "HOME-DESKTOP",
		DataDir:    `C:\SyncGate`,
		LocalAPI:   LocalAPIConfig{Host: "0.0.0.0", Port: 47820},
		Shares: []ShareConfig{{
			ID:       "drop",
			Name:     "Drop",
			RootPath: `C:\SyncGate\Drop`,
			Mode:     "upload_only",
		}},
	}

	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected non-loopback local API host to be rejected")
	}
}
