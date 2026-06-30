package cmd

import (
	"testing"
	"time"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func newResp(id string) *client.CommandResponse {
	return &client.CommandResponse{CommandId: id, Success: true}
}

func TestDeduperRememberAndGet(t *testing.T) {
	d := newCommandDeduper()
	t0 := time.Unix(1_000_000, 0)

	if _, ok := d.get("abc", t0); ok {
		t.Fatal("unseen id must not be a hit")
	}

	resp := newResp("abc")
	d.remember("abc", resp, t0)

	got, ok := d.get("abc", t0.Add(time.Minute))
	if !ok {
		t.Fatal("remembered id should hit within TTL")
	}
	if got != resp {
		t.Fatal("should return the exact cached response pointer")
	}
}

func TestDeduperExpiry(t *testing.T) {
	d := newCommandDeduper()
	t0 := time.Unix(2_000_000, 0)
	d.remember("abc", newResp("abc"), t0)

	if _, ok := d.get("abc", t0.Add(commandDedupTTL+time.Second)); ok {
		t.Fatal("entry past its TTL must not be a hit")
	}
}

func TestDeduperEvictsExpiredOnRemember(t *testing.T) {
	d := newCommandDeduper()
	t0 := time.Unix(3_000_000, 0)
	d.remember("old", newResp("old"), t0)

	// A later remember past the first entry's TTL should evict it.
	d.remember("new", newResp("new"), t0.Add(commandDedupTTL+time.Second))

	d.mu.Lock()
	_, oldPresent := d.entries["old"]
	_, newPresent := d.entries["new"]
	d.mu.Unlock()
	if oldPresent {
		t.Error("expired 'old' entry should have been evicted")
	}
	if !newPresent {
		t.Error("'new' entry should be present")
	}
}

func TestDeduperNonDedupableIDs(t *testing.T) {
	d := newCommandDeduper()
	t0 := time.Unix(4_000_000, 0)

	for _, id := range []string{"", "initial_connection"} {
		d.remember(id, newResp(id), t0)
		if _, ok := d.get(id, t0); ok {
			t.Errorf("id %q must never be deduped", id)
		}
	}
}

func TestDeduperDisabled(t *testing.T) {
	d := newCommandDeduper()
	d.enabled = false
	t0 := time.Unix(5_000_000, 0)

	d.remember("abc", newResp("abc"), t0)
	if _, ok := d.get("abc", t0); ok {
		t.Fatal("disabled deduper must never hit")
	}
}

func TestDedupEnabledEnv(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"1":     true,
		"true":  true,
		"0":     false,
		"false": false,
		"OFF":   false,
		"no":    false,
	}
	for v, want := range cases {
		t.Setenv(commandDedupEnv, v)
		if got := dedupEnabled(); got != want {
			t.Errorf("dedupEnabled() with %q = %v, want %v", v, got, want)
		}
	}
}
