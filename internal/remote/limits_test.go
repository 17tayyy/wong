package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"100M": 100 << 20, "2mb": 2 << 20, "512K": 512 << 10, "1G": 1 << 30, "4096": 4096} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "M", "10X", "-1M"} {
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) accepted it", in)
		}
	}
}

func TestLimitsRefuseWhatDoesNotFit(t *testing.T) {
	store, _, desktop, _ := setup(t)
	ctx := context.Background()
	small := []byte("---\nname: a\ndescription: d\n---\nShort.\n")
	if _, _, err := desktop.client.Push(ctx, []Upload{{Path: "me/a.md", Sha: Sha(small), Content: small}}); err != nil {
		t.Fatal(err)
	}

	store.SetLimits(Limits{File: 1 << 10})
	big := []byte(strings.Repeat("x", 2<<10))
	_, _, err := desktop.client.Push(ctx, []Upload{{Path: "me/big.md", Sha: Sha(big), Content: big}})
	if err == nil || !strings.Contains(err.Error(), "413") || !strings.Contains(err.Error(), "me/big.md takes 2 KB, more than the 1 KB a file may take") {
		t.Errorf("a file over the limit: %v", err)
	}

	size, _ := store.Size()
	store.SetLimits(Limits{Size: size + 1<<10})
	_, _, err = store.Apply("desktop", []Upload{{Path: "me/big.md", Sha: Sha(big), Content: big}})
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "the change does not fit") {
		t.Errorf("a change past the brain's size: %v", err)
	}
	if accepted, _, err := store.Apply("desktop", []Upload{{Path: "me/a.md", Base: Sha(small)}}); err != nil || len(accepted) != 1 {
		t.Errorf("a full brain refused a deletion: %v, %v", accepted, err)
	}
}

func TestLimitsPaceRequests(t *testing.T) {
	store, _, desktop, laptop := setup(t)
	store.SetLimits(Limits{Requests: 3})
	ctx := context.Background()
	for i := range 3 {
		if _, _, err := desktop.client.Changes(ctx, 0); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if _, _, err := laptop.client.Changes(ctx, 0); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("a request past the brain's pace, from another of its machines: %v", err)
	}
}
