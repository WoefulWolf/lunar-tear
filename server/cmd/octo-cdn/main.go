package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"lunar-tear/server/internal/service"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:8080", "local bind address (host:port)")
	publicAddr := flag.String("public-addr", "127.0.0.1:8080", "externally-reachable host:port used for list.bin URL rewriting")
	publicScheme := flag.String("public-scheme", "http", "scheme clients use to reach the CDN: http or https")
	assetsDir := flag.String("assets-dir", ".", "root directory containing the assets/ tree")
	tlsCert := flag.String("tls-cert", "", "TLS certificate")
	tlsKey := flag.String("tls-key", "", "TLS key")
	flag.Parse()

	if *publicScheme != "http" && *publicScheme != "https" {
		log.Fatalf("--public-scheme must be \"http\" or \"https\", got %q", *publicScheme)
	}

	// Build resourcesBaseURL from scheme+public-addr (must be exactly 43 chars to fit in list.bin protobuf).
	prefix := *publicScheme + "://" + *publicAddr + "/"
	padLen := 43 - len(prefix)
	resourcesBaseURL := ""
	if padLen < 1 {
		log.Printf("[config] public-addr too long for 43-char resource URL; list.bin will be served unchanged")
	} else {
		resourcesBaseURL = prefix + strings.Repeat("r", padLen)
	}

	octoServer := service.NewOctoHTTPServer(resourcesBaseURL, *assetsDir)
	useTLS := *tlsCert != "" && *tlsKey != ""
	if useTLS && *publicScheme == "http" {
		log.Printf("[config] serving TLS but --public-scheme is http; clients will be told to use http URLs. Set --public-scheme=https.")
	}

	srv := &http.Server{Addr: *listen}
	if useTLS {
		srv.Handler = octoServer.Handler()
	} else {
		h2s := &http2.Server{}
		srv.Handler = h2c.NewHandler(octoServer.Handler(), h2s)
		http2.ConfigureServer(srv, h2s)
	}

	// Resolve actual listen address for logging (useful when port is 0).
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *listen, err)
	}
	log.Printf("Octo CDN listening on %s (HTTP/1.1 + h2c)", lis.Addr())
	log.Printf("public address: %s://%s", *publicScheme, *publicAddr)
	if *assetsDir != "." {
		log.Printf("assets directory: %s", *assetsDir)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		var serveErr error
		if useTLS {
			serveErr = srv.ServeTLS(lis, *tlsCert, *tlsKey)
		} else {
			serveErr = srv.Serve(lis)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", serveErr)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	srv.Shutdown(context.Background())
	log.Println("shutdown complete")
}
