package update

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.7.1", "0.7.0", true},
		{"0.10.0", "0.9.9", true},
		{"v1.0.0", "0.99.99", true},
		{"0.7.0", "0.7.0", false},
		{"0.6.2", "0.7.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, will %v", c.a, c.b, got, c.want)
		}
	}
}
