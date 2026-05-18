package main

import "testing"

func TestAccessForWayBlocksFootOnGenericAccessNo(t *testing.T) {
	flags := accessForWay(map[string]string{"access": "private"}, "residential")

	if flags.car || flags.motorcycle || flags.bus || flags.foot {
		t.Fatalf("private access should block all public routing flags, got %+v", flags)
	}
}

func TestAccessForWayAllowsExplicitFootOverride(t *testing.T) {
	flags := accessForWay(map[string]string{"access": "private", "foot": "yes"}, "residential")

	if !flags.foot {
		t.Fatalf("explicit foot=yes should restore pedestrian access, got %+v", flags)
	}
	if flags.car || flags.motorcycle || flags.bus {
		t.Fatalf("foot override should not restore vehicle access, got %+v", flags)
	}
}

func TestAccessForWayTreatsSidewalkAsPedestrianOnly(t *testing.T) {
	flags := accessForWay(nil, "sidewalk")

	if flags.car || flags.motorcycle || flags.bus || !flags.foot {
		t.Fatalf("sidewalk should be pedestrian-only, got %+v", flags)
	}
}
