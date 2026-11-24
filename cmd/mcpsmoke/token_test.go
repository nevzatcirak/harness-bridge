package main

import "testing"

// ADR-026 section 2.1 / Q18 (D29): the dev tool reads CIRAK_TOKEN first and falls back to the
// legacy BRIDGE_TOKEN; it sits outside the daemon's cirakEnv shim, so the rule is only the order.
func TestSmokeTokenOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"neither", nil, ""},
		{"new only", map[string]string{"CIRAK_TOKEN": "new"}, "new"},
		{"legacy only", map[string]string{"BRIDGE_TOKEN": "old"}, "old"},
		{"both: the new name wins", map[string]string{"CIRAK_TOKEN": "new", "BRIDGE_TOKEN": "old"}, "new"},
		{"new empty falls back", map[string]string{"CIRAK_TOKEN": "", "BRIDGE_TOKEN": "old"}, "old"},
	} {
		if got := smokeToken(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
