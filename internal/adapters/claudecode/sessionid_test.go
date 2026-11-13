package claudecode

import "testing"

func TestParseSessionID(t *testing.T) {
	const u = "4e59c1bd-4333-4a71-875d-cba9301bd4a9"
	got, err := ParseSessionID("claudecode:" + u)
	if err != nil || got != u {
		t.Fatalf("valid id rejected: %q %v", got, err)
	}
	if id, err := FormatSessionID(u); err != nil || id != "claudecode:"+u {
		t.Fatalf("format: %q %v", id, err)
	}
	for _, bad := range []string{
		"", "claudecode:", "opencode:ses_abc", "claudecode:" + u + "\n", "claudecode:" + u + " ", " claudecode:" + u,
		"claudecode:4E59C1BD-4333-4A71-875D-CBA9301BD4A9", // uppercase
		"claudecode:4e59c1bd43334a71875dcba9301bd4a9",     // no dashes
		"claudecode:4e59c1bd-4333-4a71-875d-cba9301bd4a",  // short
		"claudecode:4e59c1bd-4333-4a71-875d-cba9301bd4a99",
		"claudecode:4e59c1bd-4333-4a71-875d-cba9301bd4g9", // non-hex
		"claudecode:../4e59c1bd-4333-4a71-875d-cba9301bd4a9",
		"Claudecode:" + u, "claudecode:" + u + ":x", "claudecode:\x00" + u,
	} {
		if _, err := ParseSessionID(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := FormatSessionID("not-a-uuid"); err == nil {
		t.Error("format accepted a bad uuid")
	}
}
