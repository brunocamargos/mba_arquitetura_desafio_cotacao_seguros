package quotation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type memoryCache struct {
	entries map[string]cache.Entry
	ttls    map[string]time.Duration
	failing bool
}

func newMemoryCache() *memoryCache {
	return &memoryCache{entries: map[string]cache.Entry{}, ttls: map[string]time.Duration{}}
}

func (m *memoryCache) Get(_ context.Context, key string) (cache.Entry, bool, error) {
	if m.failing {
		return cache.Entry{}, false, errors.New("redis down")
	}
	entry, found := m.entries[key]
	return entry, found, nil
}

func (m *memoryCache) Set(_ context.Context, key string, entry cache.Entry, ttl time.Duration) error {
	if m.failing {
		return errors.New("redis down")
	}
	m.entries[key], m.ttls[key] = entry, ttl
	return nil
}

type countingQuoter struct {
	calls    int
	failures map[string]partner.Kind
	validFor int64
}

func newCountingQuoter() *countingQuoter {
	return &countingQuoter{failures: map[string]partner.Kind{}, validFor: 300}
}

func (c *countingQuoter) Quote(_ context.Context, p platform.Partner, _ any) (partner.Quote, error) {
	c.calls++
	if kind, down := c.failures[p.Name]; down {
		return partner.Quote{}, &partner.Error{Partner: p.Name, Reason: string(kind), Kind: kind}
	}
	return partner.Quote{
		Partner: p.Name, QuoteID: p.Name + "-id", PremiumCents: defaultPremiums()[p.Name],
		Currency: "BRL", ValidForSeconds: c.validFor,
	}, nil
}

func resilient(quoter Quoter, store QuoteCache) *Service {
	return NewResilientService(threePartners, quoter, store, 300*time.Second)
}

func mustQuote(t *testing.T, service *Service, tenant string, request Request) Response {
	t.Helper()
	response, err := service.Quote(context.Background(), tenant, request)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	return response
}

func TestCacheNeverCrossesBrokers(t *testing.T) {
	quoter := newCountingQuoter()
	service := resilient(quoter, newMemoryCache())

	mustQuote(t, service, "corretora-a", validRequest())
	response := mustQuote(t, service, "corretora-b", validRequest())

	if quoter.calls != 6 {
		t.Fatalf("%d partner calls, expected 6: corretora-b must not reuse the entries of corretora-a", quoter.calls)
	}
	for _, quote := range response.Quotes {
		if quote.Source != SourcePartner {
			t.Errorf("%s for corretora-b came from %q", quote.Partner, quote.Source)
		}
	}
}

func TestEveryCacheKeyStartsWithTheBroker(t *testing.T) {
	store := newMemoryCache()
	mustQuote(t, resilient(newCountingQuoter(), store), "corretora-a", validRequest())

	if len(store.entries) != 3 {
		t.Fatalf("%d entries, expected one per partner", len(store.entries))
	}
	for key := range store.entries {
		if !strings.HasPrefix(key, "quote:v1:corretora-a:partner-") {
			t.Errorf("key %q does not start with the broker", key)
		}
	}
}

func TestRepeatedQuotationComesFromTheCacheKeepingTheOriginalQuoteID(t *testing.T) {
	quoter := newCountingQuoter()
	store := newMemoryCache()
	service := resilient(quoter, store)
	mustQuote(t, service, "corretora-a", validRequest())

	for key, entry := range store.entries {
		entry.FetchedAt = time.Now().Add(-100 * time.Second)
		store.entries[key] = entry
	}
	response := mustQuote(t, service, "corretora-a", validRequest())

	if quoter.calls != 3 {
		t.Fatalf("%d partner calls, expected the 3 of the first quotation", quoter.calls)
	}
	if response.Quotes[0].Partner != "partner-flaky" {
		t.Errorf("first proposal is from %q, expected the cheapest", response.Quotes[0].Partner)
	}
	for _, quote := range response.Quotes {
		if quote.Source != SourceCache || quote.AgeSeconds != 100 || quote.ValidForSeconds != 200 {
			t.Errorf("proposal %+v does not say it is 100 s old with 200 s left", quote)
		}
		if quote.QuoteID != quote.Partner+"-id" {
			t.Errorf("quote_id %q is not the original", quote.QuoteID)
		}
	}
}

func TestAnyFieldThatChangesThePriceChangesTheKey(t *testing.T) {
	quoter := newCountingQuoter()
	service := resilient(quoter, newMemoryCache())
	mustQuote(t, service, "corretora-a", validRequest())

	other := validRequest()
	other.Vehicle.ValueCents++
	mustQuote(t, service, "corretora-a", other)

	if quoter.calls != 6 {
		t.Fatalf("%d partner calls, expected 6: a different risk must not reuse the entries", quoter.calls)
	}
}

func TestTTLIsTheSmallerOfTheValidityAndTheConfiguredCeiling(t *testing.T) {
	cases := map[int64]time.Duration{60: 60 * time.Second, 300: 300 * time.Second, 900: 300 * time.Second}
	for validFor, want := range cases {
		quoter := newCountingQuoter()
		quoter.validFor = validFor
		store := newMemoryCache()
		mustQuote(t, resilient(quoter, store), "corretora-a", validRequest())
		for key, ttl := range store.ttls {
			if ttl != want {
				t.Errorf("valid_for %d: key %s got TTL %s, want %s", validFor, key, ttl, want)
			}
		}
	}
}

func TestFailureIsNotCached(t *testing.T) {
	quoter := newCountingQuoter()
	quoter.failures["partner-flaky"] = partner.KindUnavailable
	store := newMemoryCache()
	mustQuote(t, resilient(quoter, store), "corretora-a", validRequest())

	if len(store.entries) != 2 {
		t.Fatalf("%d entries, expected only the 2 partners that answered", len(store.entries))
	}
}

func TestCacheDownDoesNotBringTheQuotationDown(t *testing.T) {
	store := newMemoryCache()
	store.failing = true
	response := mustQuote(t, resilient(newCountingQuoter(), store), "corretora-a", validRequest())

	if len(response.Quotes) != 3 || response.Degraded {
		t.Fatalf("response %+v, expected the 3 proposals from the partners", response)
	}
}

func TestPartialResponseSaysWhoIsMissingAndWhy(t *testing.T) {
	quoter := newCountingQuoter()
	quoter.failures["partner-flaky"] = partner.KindCircuitOpen
	response := mustQuote(t, resilient(quoter, newMemoryCache()), "corretora-a", validRequest())

	if !response.Degraded || len(response.Missing) != 1 {
		t.Fatalf("response %+v does not say it is degraded", response)
	}
	if missing := response.Missing[0]; missing.Partner != "partner-flaky" || missing.Reason != partner.KindCircuitOpen {
		t.Errorf("missing %+v, expected partner-flaky with circuit_open", missing)
	}
	if len(response.Quotes) != 2 || response.Quotes[0].PremiumCents > response.Quotes[1].PremiumCents {
		t.Errorf("quotes %+v, expected 2 sorted by premium", response.Quotes)
	}
}

func TestCacheCoversAPartnerThatWentDown(t *testing.T) {
	quoter := newCountingQuoter()
	service := resilient(quoter, newMemoryCache())
	mustQuote(t, service, "corretora-a", validRequest())

	quoter.failures["partner-flaky"] = partner.KindCircuitOpen
	response := mustQuote(t, service, "corretora-a", validRequest())

	if response.Degraded || len(response.Quotes) != 3 {
		t.Fatalf("response %+v, expected the 3 proposals from the cache", response)
	}
}

func TestPartialResponseIs200AndEchoesTheBroker(t *testing.T) {
	quoter := newCountingQuoter()
	quoter.failures["partner-flaky"] = partner.KindTimeout
	api := NewAPI(resilient(quoter, newMemoryCache()), []string{"corretora-a"}).Routes()

	response := postQuotes(api, "corretora-a", validBody)

	if response.Code != http.StatusOK || response.Header().Get(TenantHeader) != "corretora-a" {
		t.Fatalf("status %d, broker %q: %s", response.Code, response.Header().Get(TenantHeader), response.Body)
	}
}

func TestNoPartnerAnsweringIsAnExplicitBusinessError(t *testing.T) {
	quoter := newCountingQuoter()
	for _, p := range threePartners {
		quoter.failures[p.Name] = partner.KindTimeout
	}
	api := NewAPI(resilient(quoter, newMemoryCache()), []string{"corretora-a"}).Routes()

	response := postQuotes(api, "corretora-a", validBody)

	var body noQuotesBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if response.Code != http.StatusServiceUnavailable || body.Message != "no partner insurer available" || len(body.Missing) != 3 {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
}

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func attributesOf(span sdktrace.ReadOnlySpan) map[string]string {
	found := map[string]string{}
	for _, a := range span.Attributes() {
		found[string(a.Key)] = a.Value.Emit()
	}
	return found
}

func TestTraceMarksTheShortCircuitedPartner(t *testing.T) {
	recorder := recordSpans(t)
	quoter := newCountingQuoter()
	quoter.failures["partner-flaky"] = partner.KindCircuitOpen
	mustQuote(t, resilient(quoter, newMemoryCache()), "corretora-a", validRequest())

	for _, span := range recorder.Ended() {
		attrs := attributesOf(span)
		if attrs["quotation.partner"] == "partner-flaky" && attrs["quotation.outcome"] == "circuit_open" {
			return
		}
	}
	t.Fatal("no span marks the short circuit of partner-flaky")
}

func TestTraceMarksTheCachedAnswer(t *testing.T) {
	recorder := recordSpans(t)
	service := resilient(newCountingQuoter(), newMemoryCache())
	mustQuote(t, service, "corretora-a", validRequest())

	before := len(recorder.Ended())
	mustQuote(t, service, "corretora-a", validRequest())

	second := recorder.Ended()[before:]
	if len(second) != 3 {
		t.Fatalf("%d spans in the cached request, expected 3", len(second))
	}
	for _, span := range second {
		if outcome := attributesOf(span)["quotation.outcome"]; outcome != "cache" {
			t.Errorf("outcome %q, expected cache", outcome)
		}
	}
}

func TestNoPersonalDataInSpanAttributes(t *testing.T) {
	recorder := recordSpans(t)
	service := resilient(newCountingQuoter(), newMemoryCache())
	request := validRequest()
	mustQuote(t, service, "corretora-a", request)
	mustQuote(t, service, "corretora-a", request)

	forbidden := []string{request.Driver.Document, request.Vehicle.Plate, request.Vehicle.Model, "-id", riskHash(request)}
	for _, span := range recorder.Ended() {
		for key, value := range attributesOf(span) {
			for _, secret := range forbidden {
				if strings.Contains(value, secret) {
					t.Errorf("span %s attribute %s=%q carries forbidden data", span.Name(), key, value)
				}
			}
		}
	}
}
