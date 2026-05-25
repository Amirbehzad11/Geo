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

func TestRailCandidateFromWayAcceptsRailwayTags(t *testing.T) {
	candidate, ok := railCandidateFromWay(10, []int64{1, 2, 3}, map[string]string{
		"railway":  "rail",
		"name":     "Tehran-Mashhad",
		"maxspeed": "160",
	})

	if !ok {
		t.Fatal("expected railway=rail to be routable")
	}
	if candidate.railway != "rail" || candidate.name != "Tehran-Mashhad" || candidate.speedKmH != 160 {
		t.Fatalf("unexpected rail candidate: %+v", candidate)
	}
	if !candidate.bidirectional {
		t.Fatalf("rail candidate should default to bidirectional: %+v", candidate)
	}
}

func TestRailCandidateFromWayReversesNegativeOneway(t *testing.T) {
	candidate, ok := railCandidateFromWay(10, []int64{1, 2, 3}, map[string]string{
		"railway": "subway",
		"oneway":  "-1",
		"ref":     "M1",
	})

	if !ok {
		t.Fatal("expected railway=subway to be routable")
	}
	if candidate.bidirectional {
		t.Fatalf("negative oneway should be directional: %+v", candidate)
	}
	if candidate.refs[0] != 3 || candidate.refs[2] != 1 {
		t.Fatalf("expected reversed refs, got %v", candidate.refs)
	}
	if candidate.name != "M1" {
		t.Fatalf("expected ref fallback name, got %q", candidate.name)
	}
}

func TestRailCandidateFromWayRejectsNonTrackRailway(t *testing.T) {
	if _, ok := railCandidateFromWay(10, []int64{1, 2}, map[string]string{"railway": "station"}); ok {
		t.Fatal("railway=station should not become a routable rail edge")
	}
}

func TestInvalidDSNFlagValueCatchesMissingPowerShellEnvExpansion(t *testing.T) {
	for _, dsn := range []string{"", "   ", "-table-region"} {
		if !invalidDSNFlagValue(dsn) {
			t.Fatalf("expected %q to be invalid", dsn)
		}
	}
	if invalidDSNFlagValue("host=localhost port=5432 user=geo dbname=geodb sslmode=disable") {
		t.Fatal("expected keyword/value DSN to be valid")
	}
	if invalidDSNFlagValue("postgres://geo:secret@localhost:5432/geodb?sslmode=disable") {
		t.Fatal("expected URL DSN to be valid")
	}
}
