// Command agent runs the dns-resolver-network agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/follow-th/dns-resolver-network/internal/agent"
	"github.com/follow-th/dns-resolver-network/internal/config"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", os.Getenv("DRN_CONFIG"), "path to the YAML configuration file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.LoadAgent(*cfgPath, nil)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("starting", "version", version, "name", cfg.Name, "dns", cfg.DNS, "ipv6", cfg.IPv6)
	a := agent.New(cfg, agent.NewResolver(cfg.DNS, cfg.IPv6), log)
	if err := a.Run(ctx); err != nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
