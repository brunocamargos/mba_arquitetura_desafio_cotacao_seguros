package quotation

import (
	"fmt"
	"strings"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
)

type Request struct {
	Driver   Driver  `json:"driver"`
	Vehicle  Vehicle `json:"vehicle"`
	Coverage string  `json:"coverage"`
}

type Driver struct {
	Document  string `json:"document"`
	BirthYear int    `json:"birth_year"`
}

type Vehicle struct {
	Plate      string `json:"plate"`
	Model      string `json:"model"`
	Year       int    `json:"year"`
	ValueCents int64  `json:"value_cents"`
}

var coverages = map[string]bool{"comprehensive": true, "third_party": true}

const defaultCoverage = "comprehensive"

func (r *Request) Normalize() error {
	r.Coverage = strings.TrimSpace(r.Coverage)
	if r.Coverage == "" {
		r.Coverage = defaultCoverage
	}

	r.Driver.Document = strings.TrimSpace(r.Driver.Document)
	r.Vehicle.Plate = strings.ToUpper(strings.TrimSpace(r.Vehicle.Plate))
	r.Vehicle.Model = strings.TrimSpace(r.Vehicle.Model)

	switch {
	case r.Driver.Document == "":
		return fmt.Errorf("driver.document is required")
	case r.Driver.BirthYear < 1900:
		return fmt.Errorf("driver.birth_year is required and must be a valid year")
	case r.Vehicle.Plate == "":
		return fmt.Errorf("vehicle.plate is required")
	case r.Vehicle.Year < 1900:
		return fmt.Errorf("vehicle.year is required and must be a valid year")
	case r.Vehicle.ValueCents <= 0:
		return fmt.Errorf("vehicle.value_cents must be greater than zero")
	case !coverages[r.Coverage]:
		return fmt.Errorf("coverage must be comprehensive or third_party")
	}
	return nil
}

type partnerRequest struct {
	Broker string `json:"broker"`
	Request
}

type Response struct {
	TenantID  string     `json:"tenant_id"`
	Quotes    []Proposal `json:"quotes"`
	Degraded  bool       `json:"degraded"`
	Missing   []Missing  `json:"missing,omitempty"`
	ElapsedMs int64      `json:"elapsed_ms"`
}

type Source string

const (
	SourcePartner Source = "partner"
	SourceCache   Source = "cache"
)

type Proposal struct {
	partner.Quote
	Source     Source `json:"source"`
	AgeSeconds int64  `json:"age_seconds"`
}

type Missing struct {
	Partner string       `json:"partner"`
	Reason  partner.Kind `json:"reason"`
}
