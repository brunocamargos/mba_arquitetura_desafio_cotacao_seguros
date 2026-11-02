package partner

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type State string

const (
	StateClosed   State = "closed"
	StateHalfOpen State = "half_open"
	StateOpen     State = "open"
)

func stateOf(s gobreaker.State) State {
	switch s {
	case gobreaker.StateOpen:
		return StateOpen
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	default:
		return StateClosed
	}
}

type BreakerSettings struct {
	ConsecutiveFailures uint32
	OpenFor             time.Duration
	HalfOpenRequests    uint32
}

type Quoter interface {
	Quote(ctx context.Context, p platform.Partner, request any) (Quote, error)
}

type Guard struct {
	next     Quoter
	breakers map[string]*gobreaker.CircuitBreaker[Quote]
}

func NewGuard(next Quoter, partners []platform.Partner, settings BreakerSettings) *Guard {
	guard := &Guard{next: next, breakers: make(map[string]*gobreaker.CircuitBreaker[Quote], len(partners))}

	for _, p := range partners {
		guard.breakers[p.Name] = gobreaker.NewCircuitBreaker[Quote](gobreaker.Settings{
			Name:        p.Name,
			MaxRequests: settings.HalfOpenRequests,
			Timeout:     settings.OpenFor,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= settings.ConsecutiveFailures
			},
			OnStateChange: func(name string, from, to gobreaker.State) {
				log.Printf("quotation-api: breaker partner=%s %s -> %s", name, stateOf(from), stateOf(to))
			},
		})
	}

	_, _ = otel.Meter("quotation").Int64ObservableGauge("quotation.breaker.state",
		metric.WithDescription("Circuit breaker state per partner: 0 closed, 1 half open, 2 open"),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			for name, breaker := range guard.breakers {
				observer.Observe(int64(breaker.State()), metric.WithAttributes(attribute.String("partner", name)))
			}
			return nil
		}),
	)

	return guard
}

func (g *Guard) Quote(ctx context.Context, p platform.Partner, request any) (Quote, error) {
	quote, err := g.breakers[p.Name].Execute(func() (Quote, error) {
		return g.next.Quote(ctx, p, request)
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return Quote{}, &Error{Partner: p.Name, Reason: err.Error(), Kind: KindCircuitOpen}
	}
	return quote, err
}

func (g *Guard) State(partner string) State {
	return stateOf(g.breakers[partner].State())
}
