package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNetworkRefreshAndBackgroundTrafficPolicy(t *testing.T) {
	for _, low := range []bool{false, true} {
		fallback := time.Minute
		if low {
			fallback = 2 * time.Minute
		}
		for _, tc := range []struct {
			raw  string
			want time.Duration
		}{{"", fallback}, {"soon", fallback}, {"45s", 45 * time.Second}, {"0", 0}, {"-1s", 0}} {
			c := &ArgonautConfig{Network: NetworkConfig{LowBandwidth: low, RefreshInterval: tc.raw}}
			if got := c.GetNetworkRefreshInterval(); got != tc.want {
				t.Errorf("low=%v raw=%q: %s != %s", low, tc.raw, got, tc.want)
			}
			if low && (c.IsUpdateCheckEnabled() || c.IsEventsAutoOpenEnabled() || c.GetEventsRefreshInterval() != 0) {
				t.Fatal("low-bandwidth mode still initiates unnecessary background requests")
			}
			if low && (c.GetRequestTimeout() != time.Minute || c.GetRequestTimeoutString() != "60s") {
				t.Fatal("slow connection needs a longer default request budget")
			}
		}
	}
}

func TestNetworkConfigSurvivesPreferenceSave(t *testing.T) {
	t.Setenv("ARGONAUT_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	c := GetDefaultConfig()
	c.Network = NetworkConfig{LowBandwidth: true, RefreshInterval: "3m"}
	if err := SaveArgonautConfig(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadArgonautConfig()
	if err != nil || !loaded.IsLowBandwidth() || loaded.GetNetworkRefreshInterval() != 3*time.Minute {
		t.Fatalf("lost network preferences: %+v %v", loaded, err)
	}
}
