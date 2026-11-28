package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"uniclog.io/sonoryx/apps/webgateway/internal/config"
	"uniclog.io/sonoryx/apps/webgateway/internal/session"
	"uniclog.io/sonoryx/apps/webgateway/internal/webapi"
	"uniclog.io/sonoryx/internal/identity"
)

func main() {
	path := flag.String("config", "configs/webgateway.json", "path to HTTPS gateway configuration")
	account := flag.String("add-account", "", "provision this account ID and print its access code once")
	name := flag.String("name", "", "display name for a new account")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	if *account != "" {
		code, err := session.Provision(cfg.StateDir, *account, *name)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(code)
		return
	}
	if err := serve(cfg); err != nil {
		log.Fatal(err)
	}
}

func serve(cfg config.Config) error {
	if info, err := os.Stat(filepath.Join(cfg.WebRoot, "index.html")); err != nil || !info.Mode().IsRegular() {
		return errors.New("webRoot must contain the built apps/web index.html")
	}
	var accounts map[string]session.Account
	if cfg.LoginMode == "invite" {
		var err error
		accounts, err = session.LoadAccounts(cfg.StateDir)
		if err != nil {
			return err
		}
	} else if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return err
	}

	// Preinstall the verified operator pin before any session can send an auth
	// proof. The desktop's first-use and key-confirmation paths are never used.
	pinDir, err := os.MkdirTemp(cfg.StateDir, ".pins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(pinDir)
	pinPath := filepath.Join(pinDir, "server.json")
	if err := identity.CheckOrTrust(pinPath, cfg.Endpoint, cfg.Pin); err != nil {
		return err
	}

	manager := session.NewManager(cfg, pinPath, accounts)
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           webapi.New(cfg, manager),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       45 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(redactedHTTPLog{}, "", 0),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer manager.Close()
	go report(ctx, manager)
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServeTLS(cfg.TLSCertificate, cfg.TLSKey) }()
	log.Printf("web gateway starting at %s with login mode %s", cfg.Origin, cfg.LoginMode)
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		manager.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// Keep low-level HTTP errors from logging request data.
type redactedHTTPLog struct{}

func (redactedHTTPLog) Write(p []byte) (int, error) {
	log.Print("web gateway: HTTPS transport error")
	return len(p), nil
}

func report(ctx context.Context, manager *session.Manager) {
	clock := time.NewTicker(time.Minute)
	defer clock.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-clock.C:
			if data, err := json.Marshal(manager.Metrics()); err == nil {
				log.Printf("web gateway metrics %s", data)
			}
		}
	}
}
