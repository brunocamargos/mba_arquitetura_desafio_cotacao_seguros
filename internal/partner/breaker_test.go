package partner

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

const flakySequence = "...XX.XXX..X..X.X...XXX.....X..X.....XXXXX......XX" + "XXXXXXX.X."

var (
	flaky     = platform.Partner{Name: "partner-flaky", BaseURL: "http://flaky"}
	degrading = platform.Partner{Name: "partner-degrading", BaseURL: "http://degrading"}
)

func sadSettings() BreakerSettings {
	return BreakerSettings{ConsecutiveFailures: 5, OpenFor: 10 * time.Second, HalfOpenRequests: 2}
}

type scriptedQuoter struct {
	calls  int
	answer func(call int) error
}

func (s *scriptedQuoter) Quote(_ context.Context, p platform.Partner, _ any) (Quote, error) {
	s.calls++
	if err := s.answer(s.calls); err != nil {
		return Quote{}, err
	}
	return Quote{Partner: p.Name}, nil
}

func unavailable() error {
	return &Error{Partner: flaky.Name, Status: 503, Reason: "partner replied 503"}
}

func alwaysFailing() *scriptedQuoter {
	return &scriptedQuoter{answer: func(int) error { return unavailable() }}
}

func trip(guard *Guard, p platform.Partner) {
	for range 5 {
		_, _ = guard.Quote(context.Background(), p, nil)
	}
}

func TestBreakerOpensOnTheFifthConsecutiveFailureOfTheFlakySequence(t *testing.T) {
	next := &scriptedQuoter{answer: func(call int) error {
		if flakySequence[call-1] == 'X' {
			return unavailable()
		}
		return nil
	}}
	guard := NewGuard(next, []platform.Partner{flaky}, sadSettings())

	for request := 1; request <= len(flakySequence); request++ {
		_, _ = guard.Quote(context.Background(), flaky, nil)
		if guard.State(flaky.Name) == StateOpen {
			if request != 42 {
				t.Fatalf("opened on request %d, the smoke test says 42", request)
			}
			return
		}
	}
	t.Fatal("the breaker never opened on the published sequence")
}

func TestOpenBreakerDoesNotCallThePartner(t *testing.T) {
	next := alwaysFailing()
	guard := NewGuard(next, []platform.Partner{flaky}, sadSettings())
	trip(guard, flaky)

	_, err := guard.Quote(context.Background(), flaky, nil)

	if next.calls != 5 {
		t.Fatalf("%d calls reached the partner, expected 5: an open breaker must not call it", next.calls)
	}
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != KindCircuitOpen {
		t.Fatalf("error %v is not a circuit_open partner error", err)
	}
}

func TestBreakerWalksThroughTheThreeStates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failing := true
		next := &scriptedQuoter{answer: func(int) error {
			if failing {
				return unavailable()
			}
			return nil
		}}
		guard := NewGuard(next, []platform.Partner{flaky}, sadSettings())

		trip(guard, flaky)
		if got := guard.State(flaky.Name); got != StateOpen {
			t.Fatalf("state %q after 5 failures, expected open", got)
		}

		time.Sleep(10*time.Second + time.Millisecond)
		if got := guard.State(flaky.Name); got != StateHalfOpen {
			t.Fatalf("state %q just after 10 s, expected half_open", got)
		}

		failing = false
		_, _ = guard.Quote(context.Background(), flaky, nil)
		if got := guard.State(flaky.Name); got != StateHalfOpen {
			t.Fatalf("state %q after 1 successful probe, expected half_open", got)
		}

		_, _ = guard.Quote(context.Background(), flaky, nil)
		if got := guard.State(flaky.Name); got != StateClosed {
			t.Fatalf("state %q after 2 successful probes, expected closed", got)
		}
	})
}

func TestOneFailedProbeReopensTheBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		guard := NewGuard(alwaysFailing(), []platform.Partner{flaky}, sadSettings())
		trip(guard, flaky)
		time.Sleep(10*time.Second + time.Millisecond)

		_, _ = guard.Quote(context.Background(), flaky, nil)

		if got := guard.State(flaky.Name); got != StateOpen {
			t.Fatalf("state %q after a failed probe, expected open", got)
		}
	})
}

func TestTimeoutCountsAsAFailure(t *testing.T) {
	release := make(chan struct{})
	p, closeServer := testPartner(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer closeServer()
	defer close(release)

	client := NewClient()
	client.http.Timeout = 10 * time.Millisecond
	guard := NewGuard(client, []platform.Partner{p}, sadSettings())

	_, err := guard.Quote(context.Background(), p, map[string]string{})
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != KindTimeout {
		t.Fatalf("error %v is not a timeout partner error", err)
	}

	for range 4 {
		_, _ = guard.Quote(context.Background(), p, map[string]string{})
	}
	if got := guard.State(p.Name); got != StateOpen {
		t.Fatalf("state %q after 5 timeouts, expected open", got)
	}
}

func TestClientTimeoutIsTheOneDefendedInTheSAD(t *testing.T) {
	if got := NewClient().http.Timeout; got != 2*time.Second {
		t.Fatalf("timeout %s, expected 2s", got)
	}
}

func TestBreakersAreIndependentPerPartner(t *testing.T) {
	guard := NewGuard(alwaysFailing(), []platform.Partner{flaky, degrading}, sadSettings())
	trip(guard, flaky)

	if got := guard.State(flaky.Name); got != StateOpen {
		t.Fatalf("flaky state %q, expected open", got)
	}
	if got := guard.State(degrading.Name); got != StateClosed {
		t.Fatalf("degrading state %q, expected closed", got)
	}
}
