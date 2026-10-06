package version

import "testing"

func TestParse(t *testing.T) {
	ok := []string{"v1.2.3", "1.2.3", "v0.0.0", "v10.20.30", " v0.1.0 "}
	for _, v := range ok {
		if _, got := Parse(v); !got {
			t.Errorf("Parse(%q) = not ok, want ok", v)
		}
	}
	bad := []string{"dev", "", "v1.2", "v1.2.3.4", "v1.2.x", "v1..2", "v01.2.3", "v-1.2.3", "v1.2.3-rc1", "v1.2.3+build"}
	for _, v := range bad {
		if _, got := Parse(v); got {
			t.Errorf("Parse(%q) = ok, want not ok", v)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v0.1.0", "v0.1.1", -1},
		{"v0.2.0", "v0.1.9", 1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.1.0", "0.1.0", 0},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if !ok {
			t.Fatalf("Compare(%q,%q) not ok", c.a, c.b)
		}
		if got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareUncomparable(t *testing.T) {
	for _, c := range [][2]string{{"dev", "v1.0.0"}, {"v1.0.0", "dev"}, {"", "v1.0.0"}} {
		if _, ok := Compare(c[0], c[1]); ok {
			t.Errorf("Compare(%q,%q) = ok, want not ok", c[0], c[1])
		}
	}
}
