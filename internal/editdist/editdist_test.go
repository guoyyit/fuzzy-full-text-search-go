package editdist

import "testing"

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		max  int
		want int
	}{
		{"kitten", "sitting", -1, 3},
		{"hello", "hello", 1, 0},
		{"hello", "hallo", 1, 1},
		{"hello", "xy", 1, 2}, // 超界：长度差 3 > 1
		{"abc", "xyz", 2, 3},  // 真实距离 3 > 2，返回 max+1
		{"", "abc", -1, 3},
		{"你好", "你坏", 1, 1},
	}
	for _, c := range cases {
		if got := Distance(c.a, c.b, c.max); got != c.want {
			t.Errorf("Distance(%q,%q,%d)=%d want %d", c.a, c.b, c.max, got, c.want)
		}
	}
}
