package repos

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "repos.yaml"))
	if err != nil || len(r) != 0 {
		t.Errorf("Load() = %v, %v", r, err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wong", "repos.yaml")
	r := Registry{}
	r.Add("acme", "/home/dev/acme/acme-web")
	r.Add("acme", "/home/dev/acme/acme-api")
	r.Add("acme", "/home/dev/acme/acme-web")
	r.Add("side-project", "/home/dev/side-project")
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/home/dev/acme/acme-api", "/home/dev/acme/acme-web"}; !slices.Equal(got["acme"], want) {
		t.Errorf("acme = %v", got["acme"])
	}
	if !slices.Equal(got["side-project"], []string{"/home/dev/side-project"}) {
		t.Errorf("side-project = %v", got["side-project"])
	}
}
