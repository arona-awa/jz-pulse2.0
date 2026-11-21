package models

import "testing"

func TestLevelForExp(t *testing.T) {
	cases := []struct {
		exp    int
		level  int
		remain int
		max    bool
		minPct int
	}{
		{0, 1, 101, false, 0},
		{100, 1, 1, false, 99},
		{101, 2, 149, false, 0},
		{249, 2, 1, false, 99},
		{250, 3, 230, false, 0},
		{479, 3, 1, false, 99},
		{480, 4, 510, false, 0},
		{989, 4, 1, false, 99},
		{990, 5, 460, false, 0},
		{1449, 5, 1, false, 99},
		{1450, 6, 0, true, 100},
		{9999, 6, 0, true, 100},
	}
	for _, c := range cases {
		got := LevelForExp(c.exp)
		if got.Num != c.level || got.Remaining != c.remain || got.Max != c.max || got.Progress < c.minPct {
			t.Fatalf("exp=%d got=%+v want level=%d remain=%d max=%v pct>=%d", c.exp, got, c.level, c.remain, c.max, c.minPct)
		}
	}
}
