package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
)

// PeerResult is one row of the "using it: peers" table.
type PeerResult struct {
	Target   string `json:"target"`
	OK       bool   `json:"ok"`
	PeerID   string `json:"peer_spiffe_id,omitempty"`
	Error    string `json:"error,omitempty"`
	Elapsed  string `json:"elapsed"`
	Decision string `json:"decision"`
	// Namespace is the peer's project, read from its Service DNS name
	// (processor.analytics:8443 → analytics). SameProject says whether the peer
	// should accept us, so the page can tell an intended refusal from a fault.
	Namespace   string `json:"namespace,omitempty"`
	SameProject bool   `json:"same_project"`
}

// targetNamespace reads the namespace out of a Kubernetes Service address:
// <service>.<namespace>[.svc...][:port]. Empty when the address has no dot.
func targetNamespace(target string) string {
	host := target
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// sameProjectAuthorizer is the server-side policy: accept a client only if its
// SPIFFE ID is in our trust domain and under /svc/<our namespace>/. The
// namespace is the project, so "same project" is a prefix check on the path.
// Everything else is refused at the TLS handshake and never reaches a handler.
func sameProjectAuthorizer(self spiffeid.ID) tlsconfig.Authorizer {
	prefix := projectPrefix(self)
	return func(peer spiffeid.ID, _ [][]*x509.Certificate) error {
		if peer.TrustDomain() != self.TrustDomain() {
			return fmt.Errorf("peer %s is not in trust domain %s", peer, self.TrustDomain())
		}
		if !strings.HasPrefix(peer.Path(), prefix) {
			return fmt.Errorf("peer %s is outside %s", peer, prefix)
		}
		return nil
	}
}

// projectPrefix turns spiffe://td/svc/payments/processor into /svc/payments/.
func projectPrefix(id spiffeid.ID) string {
	parts := strings.Split(strings.Trim(id.Path(), "/"), "/")
	if len(parts) >= 2 {
		return "/" + parts[0] + "/" + parts[1] + "/"
	}
	return id.Path()
}

// callPeers connects to each target over mTLS with our SVID, verifying the
// peer's SVID against the trust bundle. The peer applies its own
// sameProjectAuthorizer to us, so a refusal here is the peer's decision.
func callPeers(ctx context.Context, id *Identity, targets []string) []PeerResult {
	results := make([]PeerResult, 0, len(targets))
	for _, t := range targets {
		results = append(results, callPeer(ctx, id, t))
	}
	return results
}

func callPeer(ctx context.Context, id *Identity, target string) PeerResult {
	start := time.Now()
	res := PeerResult{Target: target, Namespace: targetNamespace(target)}
	// Our own project is the second path element of our ID: /svc/<project>/...
	if parts := strings.Split(strings.Trim(id.SPIFFEID().Path(), "/"), "/"); len(parts) >= 2 {
		res.SameProject = parts[1] == res.Namespace
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Client side: present our SVID, accept any peer in our trust domain. The
	// server decides whether to accept us; we only need to know who answered.
	tlsCfg := tlsconfig.MTLSClientConfig(id.x509, id.x509, tlsconfig.AuthorizeMemberOf(id.SPIFFEID().TrustDomain()))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: false}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+target+"/whoami.json", nil)
	if err != nil {
		res.Error, res.Decision = err.Error(), "error"
		res.Elapsed = time.Since(start).Round(time.Millisecond).String()
		return res
	}
	resp, err := client.Do(req)
	res.Elapsed = time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		res.Error = shortTLSError(err)
		res.Decision = "refused by peer"
		return res
	}
	defer resp.Body.Close()
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		if pid, err := spiffeid.FromURI(resp.TLS.PeerCertificates[0].URIs[0]); err == nil {
			res.PeerID = pid.String()
		}
	}
	var body struct {
		SPIFFEID string `json:"spiffe_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if res.PeerID == "" {
		res.PeerID = body.SPIFFEID
	}
	res.OK = resp.StatusCode == http.StatusOK
	res.Decision = "accepted"
	return res
}

func shortTLSError(err error) string {
	s := err.Error()
	// Trim the "Get https://...: " prefix the http client adds.
	if i := strings.Index(s, ": "); i > 0 && strings.HasPrefix(s, "Get ") {
		s = s[i+2:]
	}
	return s
}

// mtlsServerConfig is the server side of the mTLS listener: our SVID as the
// certificate, the client must present an SVID our bundle trusts, and the
// same-project authorizer decides.
func mtlsServerConfig(id *Identity) *tls.Config {
	return tlsconfig.MTLSServerConfig(id.x509, id.x509, sameProjectAuthorizer(id.SPIFFEID()))
}
