package note

import "testing"

func TestNormalizeTag(t *testing.T) {
	for in, want := range map[string]string{"#Deploy": "deploy", "Front End": "front-end", "infra/k8s": "infra/k8s", " api ": "api"} {
		if got, err := NormalizeTag(in); err != nil || got != want {
			t.Errorf("NormalizeTag(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"2026", "a,b", "/x", "", "#"} {
		if _, err := NormalizeTag(bad); err == nil {
			t.Errorf("NormalizeTag(%q) was accepted", bad)
		}
	}
}

func TestCloseTags(t *testing.T) {
	for _, pair := range [][2]string{{"deploy", "deployment"}, {"deploy", "deploys"}, {"test", "testing"}, {"dependency", "dependencies"},
		{"database", "databases"}, {"migrate", "migration"}, {"front-end", "frontend"}, {"kubernetes", "kubernets"}, {"despliegue", "despliegues"}} {
		if !CloseTags(pair[0], pair[1]) {
			t.Errorf("%q and %q should be close", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{{"api", "app"}, {"auth", "oauth"}, {"frontend", "backend"}, {"react", "redact"}, {"ci", "cd"}, {"deploy", "deploy"}} {
		if CloseTags(pair[0], pair[1]) {
			t.Errorf("%q and %q should not be close", pair[0], pair[1])
		}
	}
}
