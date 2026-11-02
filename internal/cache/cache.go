package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const opTimeout = 50 * time.Millisecond

type Entry struct {
	QuoteID         string    `json:"quote_id"`
	PremiumCents    int64     `json:"premium_cents"`
	Currency        string    `json:"currency"`
	CoverageCents   int64     `json:"coverage_cents"`
	ValidForSeconds int64     `json:"valid_for_seconds"`
	FetchedAt       time.Time `json:"fetched_at"`
}

func Key(tenant, partner, riskHash string) string {
	return "quote:v1:" + tenant + ":" + partner + ":" + riskHash
}

func RiskHash(fields ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x1f")))
	return hex.EncodeToString(sum[:])
}

type Redis struct {
	client *redis.Client
}

func NewRedis(addr string) *Redis {
	return &Redis{client: redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  opTimeout,
		ReadTimeout:  opTimeout,
		WriteTimeout: opTimeout,
		PoolTimeout:  opTimeout,
		MaxRetries:   -1,
	})}
}

func (r *Redis) Get(ctx context.Context, key string) (Entry, bool, error) {
	raw, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}

	var entry Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

func (r *Redis) Set(ctx context.Context, key string, entry Entry, ttl time.Duration) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, raw, ttl).Err()
}

func (r *Redis) Close() error { return r.client.Close() }
