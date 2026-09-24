package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// Identity wraps the two go-spiffe sources. The X509Source keeps the SVID and
// trust bundle current in memory; the JWTSource fetches a JWT SVID per audience
// on demand and caches the bundle for validation. Both talk to the Workload API
// socket that the node's tbot serves; tbot attests this process before the Auth
// Service issues anything.
type Identity struct {
	x509 *workloadapi.X509Source
	jwt  *workloadapi.JWTSource
}

func NewIdentity(ctx context.Context, socket string) (*Identity, error) {
	opt := workloadapi.WithClientOptions(workloadapi.WithAddr(socket))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	xs, err := workloadapi.NewX509Source(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("x509 source: %w", err)
	}
	js, err := workloadapi.NewJWTSource(ctx, opt)
	if err != nil {
		xs.Close()
		return nil, fmt.Errorf("jwt source: %w", err)
	}
	return &Identity{x509: xs, jwt: js}, nil
}

func (i *Identity) Close() {
	i.x509.Close()
	i.jwt.Close()
}

// SVID returns the current X.509 SVID (renewed in the background by go-spiffe).
func (i *Identity) SVID() (*x509svid.SVID, error) { return i.x509.GetX509SVID() }

func (i *Identity) SPIFFEID() spiffeid.ID {
	svid, err := i.SVID()
	if err != nil {
		return spiffeid.ID{}
	}
	return svid.ID
}

// JWT fetches a fresh JWT SVID for the audience. Teleport caps its lifetime at
// the resource's jwt.maximum_ttl.
func (i *Identity) JWT(ctx context.Context, audience string) (*jwtsvid.SVID, error) {
	return i.jwt.FetchJWTSVID(ctx, jwtsvid.Params{Audience: audience})
}

// X509Info is what the page shows about the certificate.
type X509Info struct {
	SPIFFEID    string    `json:"spiffe_id"`
	TrustDomain string    `json:"trust_domain"`
	Hint        string    `json:"hint,omitempty"`
	Serial      string    `json:"serial"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	TTL         string    `json:"ttl"`
	ExpiresIn   string    `json:"expires_in"`
	Issuer      string    `json:"issuer"`
	DNSSANs     []string  `json:"dns_sans,omitempty"`
}

func (i *Identity) X509Info() (*X509Info, error) {
	svid, err := i.SVID()
	if err != nil {
		return nil, err
	}
	leaf := svid.Certificates[0]
	return &X509Info{
		SPIFFEID:    svid.ID.String(),
		TrustDomain: svid.ID.TrustDomain().Name(),
		Hint:        svid.Hint,
		Serial:      formatSerial(leaf),
		NotBefore:   leaf.NotBefore.UTC(),
		NotAfter:    leaf.NotAfter.UTC(),
		TTL:         leaf.NotAfter.Sub(leaf.NotBefore).Round(time.Second).String(),
		ExpiresIn:   time.Until(leaf.NotAfter).Round(time.Second).String(),
		Issuer:      leaf.Issuer.String(),
		DNSSANs:     leaf.DNSNames,
	}, nil
}

// JWTInfo is a decoded JWT SVID: header and claims as the page prints them.
type JWTInfo struct {
	Audience  string          `json:"audience"`
	Header    json.RawMessage `json:"header"`
	Claims    json.RawMessage `json:"claims"`
	ExpiresAt time.Time       `json:"expires_at"`
	TTL       string          `json:"ttl"`
	Raw       string          `json:"-"`
}

func (i *Identity) JWTInfo(ctx context.Context, audience string) (*JWTInfo, error) {
	svid, err := i.JWT(ctx, audience)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(svid.Marshal(), ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("unexpected JWT shape")
	}
	hdr, err := decodeSegment(parts[0])
	if err != nil {
		return nil, err
	}
	claims, err := decodeSegment(parts[1])
	if err != nil {
		return nil, err
	}
	var c struct {
		Iat int64 `json:"iat"`
		Exp int64 `json:"exp"`
	}
	_ = json.Unmarshal(claims, &c)
	return &JWTInfo{
		Audience:  audience,
		Header:    hdr,
		Claims:    claims,
		ExpiresAt: svid.Expiry.UTC(),
		TTL:       (time.Duration(c.Exp-c.Iat) * time.Second).String(),
		Raw:       svid.Marshal(),
	}, nil
}

func decodeSegment(s string) (json.RawMessage, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var pretty map[string]any
	if err := json.Unmarshal(b, &pretty); err != nil {
		return nil, err
	}
	return json.MarshalIndent(pretty, "", "  ")
}

func formatSerial(c *x509.Certificate) string {
	h := fmt.Sprintf("%x", c.SerialNumber)
	if len(h)%2 == 1 {
		h = "0" + h
	}
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}
