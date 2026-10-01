package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	mode := flag.String("mode", "serve", "serve or stdio")
	user := flag.String("user", "external-mcp", "Issabel subject used in stdio mode")
	flag.Parse()

	cfg := loadConfig()
	if *mode != "serve" && *mode != "stdio" {
		log.Fatal("mode must be serve or stdio")
	}
	if err := cfg.validate(*mode); err != nil {
		log.Fatal(err)
	}
	pbx, err := newPBXClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	if *mode == "stdio" {
		if err := runStdio(context.Background(), os.Stdin, os.Stdout, pbx, *user); err != nil {
			log.Fatal(err)
		}
		return
	}

	store, err := newStore(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.cleanup(); err != nil {
		log.Printf("history cleanup: %v", err)
	}
	cleanupStop := make(chan struct{})
	defer close(cleanupStop)
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := store.cleanup(); err != nil {
					log.Printf("history cleanup: %v", err)
				}
			case <-cleanupStop:
				return
			}
		}
	}()

	server, err := newHTTPServer(cfg, store, pbx)
	if err != nil {
		log.Fatal(err)
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("issabel-mcp listening on %s", cfg.Listen)
		if err := server.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
			log.Printf("http server: %v", err)
			done <- syscall.SIGTERM
		}
	}()
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
