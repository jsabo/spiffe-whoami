package main

import "testing"

func TestTargetNamespace(t *testing.T) {
	cases := map[string]string{
		"processor.analytics:8443":               "analytics",
		"ledger.payments:8443":                   "payments",
		"ledger.payments.svc.cluster.local:8443": "payments",
		"ledger.payments":                        "payments",
		"localhost:8443":                         "",
		"10.0.0.7:8443":                          "0",
	}
	for in, want := range cases {
		if got := targetNamespace(in); got != want {
			t.Errorf("targetNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}
