package main

import (
	"testing"

	"geo-service/config"
)

func TestShouldLoadInternalGraphAtStartupSkipsForOSRM(t *testing.T) {
	cfg := &config.Config{
		RoutingBackend:       "osrm",
		InternalGraphEnabled: true,
	}
	if shouldLoadInternalGraphAtStartup(cfg) {
		t.Fatal("OSRM-primary mode must not load the internal road graph at startup")
	}
}

func TestShouldLoadInternalGraphAtStartupHonorsInternalFlags(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{
			name: "internal eager enabled",
			cfg: config.Config{
				RoutingBackend:        "internal",
				InternalGraphEnabled:  true,
				InternalGraphLazyLoad: false,
			},
			want: true,
		},
		{
			name: "internal lazy enabled",
			cfg: config.Config{
				RoutingBackend:        "internal",
				InternalGraphEnabled:  true,
				InternalGraphLazyLoad: true,
			},
			want: false,
		},
		{
			name: "internal disabled",
			cfg: config.Config{
				RoutingBackend:       "internal",
				InternalGraphEnabled: false,
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldLoadInternalGraphAtStartup(&tc.cfg); got != tc.want {
				t.Fatalf("shouldLoadInternalGraphAtStartup() = %v, want %v", got, tc.want)
			}
		})
	}
}
