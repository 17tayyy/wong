package note

import "testing"

func TestReaches(t *testing.T) {
	for _, c := range []struct {
		ref  string
		want int
	}{
		{"app/x/models.py", 2}, {"./app/x/models.py:12", 2}, {"api/app/x/models.py", 2}, {"x/models.py", 2},
		{"app/x", 1}, {"app/x/", 1}, {"api/app", 1}, {"/home/u/api/app/x", 1}, {"~/api/app/x/models.py", 2},
		{"app", 0}, {"app/", 0}, {"models.py", 0}, {"app/y", 0}, {"app/*.py", 0}, {"/home/u/other/app/x", 0}, {"/home/u/api", 0},
	} {
		if got := reaches(c.ref, "app/x/models.py", "api", "/home/u/api", "/home/u"); got != c.want {
			t.Errorf("reaches(%q) = %d, want %d", c.ref, got, c.want)
		}
	}
	for ref, want := range map[string]int{"ai_core/": 1, "ai_core": 0, "src/": 0} {
		if got := reaches(ref, "ai_core/prompts.py", "api", "/home/u/api", "/home/u"); got != want {
			t.Errorf("reaches(%q) on a top-level folder = %d, want %d", ref, got, want)
		}
	}
}
