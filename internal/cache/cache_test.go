package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func sampleEntry() Entry {
	return Entry{
		QuoteID: "partner-flaky-abc", PremiumCents: 84210, Currency: "BRL", CoverageCents: 5000000,
		ValidForSeconds: 300, FetchedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
}

func TestKeyIsTheContractWrittenInTheSAD(t *testing.T) {
	got := Key("corretora-a", "partner-flaky", "9f2c")
	if want := "quote:v1:corretora-a:partner-flaky:9f2c"; got != want {
		t.Fatalf("key %q, want %q", got, want)
	}
}

func TestRiskHashDoesNotCollideWhenFieldsShiftBetweenPositions(t *testing.T) {
	if RiskHash("ab", "c") == RiskHash("a", "bc") {
		t.Fatal("different fields produced the same hash")
	}
}

func TestEntryIsServedUntilTheTTLEnds(t *testing.T) {
	server := miniredis.RunT(t)
	store := NewRedis(server.Addr())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	if err := store.Set(ctx, "key", sampleEntry(), 300*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	server.FastForward(299 * time.Second)
	got, found, err := store.Get(ctx, "key")
	if err != nil || !found || got != sampleEntry() {
		t.Fatalf("before the TTL: entry %+v found=%v err=%v", got, found, err)
	}

	server.FastForward(2 * time.Second)
	if _, found, err := store.Get(ctx, "key"); err != nil || found {
		t.Fatalf("after the TTL: found=%v err=%v, expected a plain miss", found, err)
	}
}

func TestStoreFailsWhenRedisIsDown(t *testing.T) {
	server := miniredis.RunT(t)
	store := NewRedis(server.Addr())
	defer func() { _ = store.Close() }()
	server.Close()

	if _, _, err := store.Get(context.Background(), "key"); err == nil {
		t.Fatal("Get with Redis down returned no error")
	}
}
