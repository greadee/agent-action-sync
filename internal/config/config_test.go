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
	share := cfg.Shares[0]
	if share.ScanIntervalSeconds != int(DefaultScanInterval.Seconds()) {
		t.Fatalf("default scan interval = %d", share.ScanIntervalSeconds)
	}
	if share.DeletionLimitCount != DefaultDeletionLimitCount || share.DeletionLimitPercent != DefaultDeletionLimitPercent {
		t.Fatalf("default deletion limits = %d/%d", share.DeletionLimitCount, share.DeletionLimitPercent)
	}
	if share.TargetDriftPolicy != DefaultTargetDriftPolicy {
		t.Fatalf("default target drift policy = %q", share.TargetDriftPolicy)
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

func TestConfigValidatesPerShareOneWaySettings(t *testing.T) {
	cfg := Config{
		DeviceName: "HOME-DESKTOP",
		DataDir:    `C:\SyncGate`,
		Shares: []ShareConfig{{
			ID:                   "drop",
			Name:                 "Drop",
			RootPath:             `C:\SyncGate\Drop`,
			Mode:                 "one_way_target",
			IgnorePatterns:       []string{" *.tmp ", "cache/**"},
			ScanIntervalSeconds:  30,
			DeletionLimitCount:   4,
			DeletionLimitPercent: 50,
			TargetDriftPolicy:    "preserve_conflict_copy",
		}},
	}

	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	if cfg.Shares[0].IgnorePatterns[0] != "*.tmp" {
		t.Fatalf("ignore pattern was not normalized: %#v", cfg.Shares[0].IgnorePatterns)
	}
}

func TestConfigRejectsUnsafePerShareSettings(t *testing.T) {
	tests := []struct {
		name  string
		share ShareConfig
	}{
		{
			name: "bad scan interval",
			share: ShareConfig{
				ScanIntervalSeconds: -1,
			},
		},
		{
			name: "bad deletion percent",
			share: ShareConfig{
				DeletionLimitPercent: 101,
			},
		},
		{
			name: "bad drift policy",
			share: ShareConfig{
				TargetDriftPolicy: "merge",
			},
		},
		{
			name: "unsafe ignore pattern",
			share: ShareConfig{
				IgnorePatterns: []string{"../private/**"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			share := ShareConfig{
				ID:       "drop",
				Name:     "Drop",
				RootPath: `C:\SyncGate\Drop`,
				Mode:     "one_way_source",
			}
			if test.share.ScanIntervalSeconds != 0 {
				share.ScanIntervalSeconds = test.share.ScanIntervalSeconds
			}
			if test.share.DeletionLimitPercent != 0 {
				share.DeletionLimitPercent = test.share.DeletionLimitPercent
			}
			if test.share.TargetDriftPolicy != "" {
				share.TargetDriftPolicy = test.share.TargetDriftPolicy
			}
			if test.share.IgnorePatterns != nil {
				share.IgnorePatterns = test.share.IgnorePatterns
			}

			cfg := Config{
				DeviceName: "HOME-DESKTOP",
				DataDir:    `C:\SyncGate`,
				Shares:     []ShareConfig{share},
			}
			if err := cfg.ApplyDefaultsAndValidate(); err == nil {
				t.Fatal("expected invalid per-share settings to be rejected")
			}
		})
	}
}
