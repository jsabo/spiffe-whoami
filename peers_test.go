package main

import (
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

func TestProjectPrefix(t *testing.T) {
	cases := map[string]string{
		"spiffe://td.example/svc/payments/processor": "/svc/payments/",
		"spiffe://td.example/svc/analytics/ledger":   "/svc/analytics/",
		"spiffe://td.example/single":                 "/single",
	}
	for in, want := range cases {
		id := spiffeid.RequireFromString(in)
		if got := projectPrefix(id); got != want {
			t.Errorf("projectPrefix(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestSameProjectAuthorizer(t *testing.T) {
	self := spiffeid.RequireFromString("spiffe://td.example/svc/payments/processor")
	authorize := sameProjectAuthorizer(self)

	accept := []string{
		"spiffe://td.example/svc/payments/ledger",
		"spiffe://td.example/svc/payments/processor",
	}
	refuse := []string{
		"spiffe://td.example/svc/analytics/processor", // other project
		"spiffe://td.example/svc/paymentsx/ledger",    // prefix must end at the slash
		"spiffe://other.example/svc/payments/ledger",  // other trust domain
	}
	for _, s := range accept {
		if err := authorize(spiffeid.RequireFromString(s), nil); err != nil {
			t.Errorf("expected %s accepted: %v", s, err)
		}
	}
	for _, s := range refuse {
		if err := authorize(spiffeid.RequireFromString(s), nil); err == nil {
			t.Errorf("expected %s refused", s)
		}
	}
}

func TestMask(t *testing.T) {
	if got := mask("payments-db-password"); got != "pa****************rd" {
		t.Errorf("mask = %q", got)
	}
	if got := mask("abc"); got != "***" {
		t.Errorf("mask short = %q", got)
	}
}
