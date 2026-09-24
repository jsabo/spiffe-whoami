// spiffe-whoami shows the SPIFFE identity Teleport Workload Identity issued to
// the pod it runs in, and uses that identity: it calls AWS through OIDC
// federation and its peers over mTLS. Everything comes from the SPIFFE Workload
// API socket; nothing is read from files or environment secrets.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := Config{
		SocketPath:  envOr("SPIFFE_ENDPOINT_SOCKET", "unix:///spiffe-workload-api/spiffe.sock"),
		HTTPAddr:    envOr("HTTP_ADDR", ":8080"),
		MTLSAddr:    envOr("MTLS_ADDR", ":8443"),
		AWSRoleARN:  os.Getenv("AWS_ROLE_ARN"),
		AWSRegion:   envOr("AWS_REGION", "us-east-2"),
		AWSSecretID: os.Getenv("AWS_SECRET_ID"),
		Peers:       splitList(os.Getenv("PEERS")),
	}

	id, err := NewIdentity(ctx, cfg.SocketPath)
	if err != nil {
		log.Error("connecting to the SPIFFE Workload API", "socket", cfg.SocketPath, "err", err)
		os.Exit(1)
	}
	defer id.Close()
	log.Info("identity ready", "spiffe_id", id.SPIFFEID().String())

	srv := NewServer(cfg, id, log)
	if err := srv.Run(ctx); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
	// Give in-flight requests a moment to finish before the sources close.
	time.Sleep(200 * time.Millisecond)
}

// Config is everything the app takes from its environment. None of it is secret.
type Config struct {
	SocketPath  string
	HTTPAddr    string
	MTLSAddr    string
	AWSRoleARN  string
	AWSRegion   string
	AWSSecretID string
	Peers       []string
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
