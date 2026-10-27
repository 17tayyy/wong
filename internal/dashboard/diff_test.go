package dashboard

import "testing"

func TestDiffLines(t *testing.T) {
	got := diffLines("a\nb\nc\n", "a\nc\nd\n")
	want := []diffLine{{same, "a"}, {removed, "b"}, {same, "c"}, {added, "d"}}
	if len(got) != len(want) {
		t.Fatalf("diff = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if plus, minus := diffStat(got); plus != 1 || minus != 1 {
		t.Errorf("stat = +%d -%d", plus, minus)
	}
	if got := diffLines("", "x\n"); len(got) != 1 || got[0].Op != added {
		t.Errorf("diff from nothing = %+v", got)
	}
}
