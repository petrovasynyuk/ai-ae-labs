// ДЗ 4: ADK REST навколо того самого графа ДЗ 3, без LLM і без ключів.
// ADK Go v2.5.0, станом на 09/2026.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	defaultAddr, err := listenAddress(os.Getenv("PORT"))
	if err != nil {
		log.Fatal(err)
	}
	addr := flag.String("addr", defaultAddr, "HTTP listen address (PORT binds all interfaces; otherwise loopback)")
	drainTimeout := flag.Duration("drain-timeout", 30*time.Second, "maximum graceful shutdown duration")
	pprofAddr := flag.String("pprof-addr", "", "opt-in pprof HTTP listen address (strictly 127.0.0.1:<port>; default disabled)")
	flag.Parse()
	if *drainTimeout <= 0 {
		log.Fatal("drain-timeout must be positive")
	}
	if *pprofAddr != "" {
		if err := validatePprofAddr(*pprofAddr); err != nil {
			log.Fatal(err)
		}
	}
	svc, err := newService()
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *pprofAddr != "" {
		pprofServer, pprofListener, err := newPprofServer(*pprofAddr)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("pprof listener active at http://%s/debug/pprof/ (loopback only)", pprofListener.Addr())
		go func() {
			if err := pprofServer.Serve(pprofListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("pprof server error: %v", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = pprofServer.Shutdown(shutdownCtx)
		}()
	}

	log.Printf("ADK service listening at %s (no model credentials required)", listener.Addr())
	if err := svc.serve(ctx, listener, *drainTimeout); err != nil {
		log.Fatal(err)
	}
	log.Print("ADK service drained and stopped")
}
