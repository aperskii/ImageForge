// Package healthcheck probes a running service over HTTP.
//
// It exists so a container image can check its own health without shipping
// curl. The API image is distroless and has no shell at all, and adding one to
// a production image so that a healthcheck can run is a poor trade: the binary
// already knows how to make an HTTP request.
package healthcheck

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Arg is the first argument that puts a binary into probe mode.
const Arg = "healthcheck"

// timeout bounds the probe. A service that cannot answer a request to its own
// loopback interface in this long is unhealthy whatever the reason.
const timeout = 3 * time.Second

// Requested reports whether the command line asks for a probe rather than for
// the service itself.
func Requested(args []string) bool {
	return len(args) > 0 && args[0] == Arg
}

// Run probes http://<addr>/healthz and returns nil when it answers 2xx.
func Run(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	url := "http://" + normalizeAddr(addr) + "/healthz"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("healthcheck: build a request for %s: %w", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck: %s answered %s", url, resp.Status)
	}
	return nil
}

// Main runs a probe and exits, which is all a binary in probe mode should do.
func Main(addr string) {
	if err := Run(addr); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

// normalizeAddr turns a listen address into one that can be dialed.
//
// A server listening on ":8080" or "0.0.0.0:8080" is reachable from inside its
// own container on the loopback interface; naming that explicitly avoids
// relying on an empty host resolving usefully.
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "127.0.0.1:8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}

	host, port, found := strings.Cut(addr, ":")
	if found && (host == "" || host == "0.0.0.0" || host == "[::]") {
		return "127.0.0.1:" + port
	}
	return addr
}
