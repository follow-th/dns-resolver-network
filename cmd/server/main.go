// Command server runs the dns-resolver-network server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/follow-th/dns-resolver-network/internal/auth"
	"github.com/follow-th/dns-resolver-network/internal/config"
	"github.com/follow-th/dns-resolver-network/internal/server"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		key, hash, err := auth.GenerateKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("key:  %s\nhash: %s\n\n", key, hash)
		fmt.Println("Give the key to the client or agent. Put only the hash in the server config")
		fmt.Println("(client_keys or agent_keys). The key cannot be recovered from the hash.")
		return
	}

	cfgPath := flag.String("config", os.Getenv("DRN_CONFIG"), "path to the YAML configuration file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: server [-config file]\n       server keygen\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.LoadServer(*cfgPath, nil)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := server.New(ctx, cfg, log, server.Options{})
	if err != nil {
		log.Error("cannot start", "error", err)
		os.Exit(2)
	}
	log.Info("starting", "version", version)
	if err := srv.Run(ctx); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
