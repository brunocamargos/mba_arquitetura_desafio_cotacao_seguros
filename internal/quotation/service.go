package quotation

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type Quoter interface {
	Quote(ctx context.Context, p platform.Partner, request any) (partner.Quote, error)
}

type QuoteCache interface {
	Get(ctx context.Context, key string) (cache.Entry, bool, error)
	Set(ctx context.Context, key string, entry cache.Entry, ttl time.Duration) error
}

type Service struct {
	partners []platform.Partner
	quoter   Quoter
	cache    QuoteCache
	ttl      time.Duration
	lookups  metric.Int64Counter
}

func NewService(partners []platform.Partner, quoter Quoter) *Service {
	return &Service{partners: partners, quoter: quoter}
}

func NewResilientService(partners []platform.Partner, quoter Quoter, quoteCache QuoteCache, ttl time.Duration) *Service {
	lookups, _ := otel.Meter("quotation").Int64Counter("quotation.cache.lookups",
		metric.WithDescription("Quote cache lookups per partner, by result: hit, miss or error"))
	return &Service{partners: partners, quoter: quoter, cache: quoteCache, ttl: ttl, lookups: lookups}
}

type NoQuotesError struct {
	Missing []Missing
}

func (e *NoQuotesError) Error() string { return "no partner insurer available" }

func (s *Service) Quote(ctx context.Context, tenant string, request Request) (Response, error) {
	start := time.Now()
	forPartner := partnerRequest{Broker: tenant, Request: request}
	hash := riskHash(request)

	quotes := make([]Proposal, 0, len(s.partners))
	var missing []Missing
	for _, p := range s.partners {
		proposal, err := s.quoteOne(ctx, p, tenant, hash, forPartner)
		if err == nil {
			quotes = append(quotes, proposal)
			continue
		}
		if s.cache == nil {
			return Response{}, err
		}
		missing = append(missing, Missing{Partner: p.Name, Reason: reasonOf(err)})
	}

	if len(quotes) == 0 {
		return Response{}, &NoQuotesError{Missing: missing}
	}

	sort.Slice(quotes, func(i, j int) bool {
		return quotes[i].PremiumCents < quotes[j].PremiumCents
	})

	return Response{
		TenantID:  tenant,
		Quotes:    quotes,
		Degraded:  len(missing) > 0,
		Missing:   missing,
		ElapsedMs: time.Since(start).Milliseconds(),
	}, nil
}

func (s *Service) quoteOne(ctx context.Context, p platform.Partner, tenant, hash string, forPartner partnerRequest) (Proposal, error) {
	if s.cache == nil {
		quote, err := s.quoter.Quote(ctx, p, forPartner)
		return Proposal{Quote: quote, Source: SourcePartner}, err
	}

	ctx, span := otel.Tracer("quotation").Start(ctx, "partner.quote",
		trace.WithAttributes(attribute.String("quotation.partner", p.Name)))
	defer span.End()

	key := cache.Key(tenant, p.Name, hash)
	if proposal, hit := s.lookup(ctx, key, p.Name); hit {
		span.SetAttributes(attribute.String("quotation.outcome", string(SourceCache)))
		return proposal, nil
	}

	quote, err := s.quoter.Quote(ctx, p, forPartner)
	if err != nil {
		span.SetAttributes(attribute.String("quotation.outcome", string(reasonOf(err))))
		return Proposal{}, err
	}

	span.SetAttributes(attribute.String("quotation.outcome", string(SourcePartner)))
	s.store(ctx, key, quote)
	return Proposal{Quote: quote, Source: SourcePartner}, nil
}

func (s *Service) lookup(ctx context.Context, key, name string) (Proposal, bool) {
	entry, found, err := s.cache.Get(ctx, key)
	switch {
	case err != nil:
		s.countLookup(ctx, name, "error")
		return Proposal{}, false
	case !found:
		s.countLookup(ctx, name, "miss")
		return Proposal{}, false
	}

	s.countLookup(ctx, name, "hit")
	age := int64(time.Since(entry.FetchedAt).Seconds())
	return Proposal{
		Quote: partner.Quote{
			Partner: name, QuoteID: entry.QuoteID, PremiumCents: entry.PremiumCents, Currency: entry.Currency,
			CoverageCents: entry.CoverageCents, ValidForSeconds: entry.ValidForSeconds - age,
		},
		Source:     SourceCache,
		AgeSeconds: age,
	}, true
}

func (s *Service) store(ctx context.Context, key string, quote partner.Quote) {
	ttl := min(time.Duration(quote.ValidForSeconds)*time.Second, s.ttl)
	if ttl <= 0 {
		return
	}

	entry := cache.Entry{
		QuoteID: quote.QuoteID, PremiumCents: quote.PremiumCents, Currency: quote.Currency,
		CoverageCents: quote.CoverageCents, ValidForSeconds: quote.ValidForSeconds, FetchedAt: time.Now(),
	}
	if err := s.cache.Set(ctx, key, entry, ttl); err != nil {
		log.Printf("quotation-api: cache write failed for partner=%s: %v", quote.Partner, err)
	}
}

func (s *Service) countLookup(ctx context.Context, name, result string) {
	s.lookups.Add(ctx, 1, metric.WithAttributes(
		attribute.String("partner", name),
		attribute.String("result", result),
	))
}

func reasonOf(err error) partner.Kind {
	var failure *partner.Error
	if errors.As(err, &failure) && failure.Kind != "" {
		return failure.Kind
	}
	return partner.KindUnavailable
}

func riskHash(r Request) string {
	return cache.RiskHash(
		r.Driver.Document,
		strconv.Itoa(r.Driver.BirthYear),
		r.Vehicle.Plate,
		r.Vehicle.Model,
		strconv.Itoa(r.Vehicle.Year),
		strconv.FormatInt(r.Vehicle.ValueCents, 10),
		r.Coverage,
	)
}
