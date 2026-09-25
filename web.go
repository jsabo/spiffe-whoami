package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
	"json": func(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) },
}).Parse(indexHTML))

// Server serves the page on plain HTTP (for the browser, behind Teleport app
// access) and the JSON on an mTLS listener (for peers, who must present a SVID).
type Server struct {
	cfg Config
	id  *Identity
	log *slog.Logger
}

func NewServer(cfg Config, id *Identity, log *slog.Logger) *Server {
	return &Server{cfg: cfg, id: id, log: log}
}

// Report is the whole page as data; /whoami.json returns it verbatim.
type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Pod         PodInfo      `json:"pod"`
	X509        *X509Info    `json:"x509"`
	X509Error   string       `json:"x509_error,omitempty"`
	JWT         *JWTInfo     `json:"jwt"`
	JWTError    string       `json:"jwt_error,omitempty"`
	AWS         *AWSResult   `json:"aws"`
	Peers       []PeerResult `json:"peers"`
}

// PodInfo is what the pod knows about itself from the Downward API. It is shown
// next to the attested facts in the SVID so the audience can see they agree.
type PodInfo struct {
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"service_account"`
	Name           string `json:"name"`
	Node           string `json:"node"`
}

// report builds the page. full is true for the browser and false when a peer
// asks over mTLS: a peer has already verified our X.509 SVID at the handshake
// and reads only the SPIFFE ID, so fetching a JWT for it would be a wasted
// issuance in the audit log, and calling peers back would loop forever.
func (s *Server) report(ctx context.Context, full bool) *Report {
	r := &Report{
		GeneratedAt: time.Now().UTC(),
		Pod: PodInfo{
			Namespace:      os.Getenv("POD_NAMESPACE"),
			ServiceAccount: os.Getenv("POD_SERVICE_ACCOUNT"),
			Name:           os.Getenv("POD_NAME"),
			Node:           os.Getenv("NODE_NAME"),
		},
	}
	var err error
	if r.X509, err = s.id.X509Info(); err != nil {
		r.X509Error = err.Error()
	}
	if !full {
		return r
	}
	// One JWT per page: the token shown in the JWT section is the same one
	// handed to STS below, so a visit is one spiffe.svid.issued event.
	var token string
	if r.JWT, err = s.id.JWTInfo(ctx, "sts.amazonaws.com"); err != nil {
		r.JWTError = err.Error()
	} else {
		token = r.JWT.Raw
	}

	// AWS and peers are network calls; run them together.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.AWS = callAWS(ctx, s.cfg, s.id, token) }()
	if len(s.cfg.Peers) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); r.Peers = callPeers(ctx, s.id, s.cfg.Peers) }()
	}
	wg.Wait()
	return r
}

func (s *Server) handleIndex(w http.ResponseWriter, req *http.Request) {
	// "/" is Go's catch-all pattern; without this, a browser's /favicon.ico
	// request would render a second full report, with a second JWT issuance.
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	r := s.report(req.Context(), true)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, r); err != nil {
		s.log.Error("render", "err", err)
	}
}

// handleJSON serves the report. On the mTLS listener the caller is a peer that
// already passed the same-project authorizer at the handshake; we skip the peer
// fan-out there so two apps do not ping-pong forever.
func (s *Server) handleJSON(full bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r := s.report(req.Context(), full)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if _, err := s.id.SVID(); err != nil {
		http.Error(w, "no SVID: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) Run(ctx context.Context) error {
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/", s.handleIndex)
	httpMux.HandleFunc("/whoami.json", s.handleJSON(true))
	httpMux.HandleFunc("/healthz", s.handleHealth)
	httpSrv := &http.Server{Addr: s.cfg.HTTPAddr, Handler: httpMux, ReadHeaderTimeout: 10 * time.Second}

	mtlsMux := http.NewServeMux()
	mtlsMux.HandleFunc("/whoami.json", s.handleJSON(false))
	mtlsSrv := &http.Server{Addr: s.cfg.MTLSAddr, Handler: mtlsMux, TLSConfig: mtlsServerConfig(s.id), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	go func() {
		s.log.Info("http listening", "addr", s.cfg.HTTPAddr)
		errc <- httpSrv.ListenAndServe()
	}()
	go func() {
		ln, err := net.Listen("tcp", s.cfg.MTLSAddr)
		if err != nil {
			errc <- err
			return
		}
		s.log.Info("mtls listening", "addr", s.cfg.MTLSAddr)
		// Certificates come from the SVID source via TLSConfig.GetCertificate.
		errc <- mtlsSrv.ServeTLS(ln, "", "")
	}()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
		_ = mtlsSrv.Shutdown(shutdown)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
