package updater

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "0.1.1", true},
		{"v0.1.1", "v0.1.1", false},
		{"0.1.9", "0.1.10", true},
		{"1.0", "1.0.0", false},
		{"dev", "0.1.0", true},
	}
	for _, tc := range cases {
		if got := IsNewer(tc.current, tc.latest); got != tc.want {
			t.Fatalf("IsNewer(%q,%q)=%v want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}
