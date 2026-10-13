package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/server"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "Adresse, auf der der Relay lauscht")
	maxViewers := flag.Int("max-viewers", 8, "maximale Zuschauer pro Raum")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	logger.Printf("mss-relay %s lauscht auf %s", version.Version, *addr)

	srv := server.New(server.Config{MaxViewers: *maxViewers, Log: logger})
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(web.FS()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		logger.Println("beende Relay")
		httpSrv.Close()
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal(err)
	}
}
