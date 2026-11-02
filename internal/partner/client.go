package partner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type Quote struct {
	Partner         string `json:"partner"`
	QuoteID         string `json:"quote_id"`
	PremiumCents    int64  `json:"premium_cents"`
	Currency        string `json:"currency"`
	CoverageCents   int64  `json:"coverage_cents"`
	ValidForSeconds int64  `json:"valid_for_seconds"`
}

type Client struct {
	http *http.Client
}

const Timeout = 2 * time.Second

func NewClient() *Client {
	return &Client{http: &http.Client{
		Timeout:   Timeout,
		Transport: platform.InstrumentTransport(http.DefaultTransport),
	}}
}

const responseLimit = 1 << 20

func (c *Client) Quote(ctx context.Context, p platform.Partner, request any) (Quote, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: fmt.Sprintf("invalid request: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/quotes", bytes.NewReader(body))
	if err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(req)
	if err != nil {
		failure := &Error{Partner: p.Name, Reason: err.Error()}
		if os.IsTimeout(err) {
			failure.Kind = KindTimeout
		}
		return Quote{}, failure
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseLimit))
		return Quote{}, &Error{
			Partner: p.Name,
			Status:  response.StatusCode,
			Reason:  fmt.Sprintf("partner replied %d", response.StatusCode),
		}
	}

	var quote Quote
	if err := json.NewDecoder(io.LimitReader(response.Body, responseLimit)).Decode(&quote); err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: fmt.Sprintf("unreadable response: %v", err)}
	}

	quote.Partner = p.Name
	return quote, nil
}

type Kind string

const (
	KindUnavailable Kind = "unavailable"
	KindTimeout     Kind = "timeout"
	KindCircuitOpen Kind = "circuit_open"
)

type Error struct {
	Partner string
	Status  int
	Reason  string
	Kind    Kind
}

func (e *Error) Error() string {
	return fmt.Sprintf("partner %s: %s", e.Partner, e.Reason)
}
