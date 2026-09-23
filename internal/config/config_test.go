package config

import "testing"

func TestExposurePort(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want int
	}{
		{"app without proxy", Config{AppPort: 3000}, 3000},
		{"proxy enabled", Config{AppPort: 3000, ProxyEnabled: true, ProxyPort: 5000}, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ExposurePort(); got != tc.want {
				t.Fatalf("ExposurePort() = %d, want %d", got, tc.want)
			}
		})
	}
}
