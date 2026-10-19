package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/pairing"
	"omstudioit.com/nut-ups-agent/internal/runner"
	"omstudioit.com/nut-ups-agent/internal/state"
)

var version = "0.2.0"

func main() { os.Exit(run()) }
func run() int {
	path := flag.String("config", "/etc/nut-ups-agent/config.json", "Configuration file")
	once := flag.Bool("once", false, "Read once, persist and attempt one upload")
	check := flag.Bool("check-config", false, "Validate configuration without network access")
	status := flag.Bool("status", false, "Show locally saved readings and queue status, without network access")
	showVersion := flag.Bool("version", false, "Print agent version")
	verbose := flag.Bool("verbose", false, "Include routine progress in logs")
	pairCode := flag.String("pair-code", "", "One-time code issued by the owner of a remote workspace")
	pairURL := flag.String("pair-url", "", "HTTPS API base URL for pairing")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	c, err := config.Load(*path)
	if err != nil {
		log.Error(err.Error())
		return 1
	}
	if *check {
		fmt.Println("Configuration is valid. No network requests were made.")
		return 0
	}
	if *pairCode == "" && *pairURL == "" {
		if err = c.LoadPairedConnection(); err != nil {
			log.Error(err.Error())
			return 1
		}
	}
	if *status {
		d, err := state.Read(c.StateDir)
		if err != nil {
			log.Error("Cannot read saved agent state")
			return 1
		}
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		if e.Encode(d.Summary()) != nil {
			return 1
		}
		return 0
	}
	store, err := state.Open(c.StateDir)
	if err != nil {
		log.Error(err.Error())
		return 1
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *pairCode != "" || *pairURL != "" {
		if *pairCode == "" || *pairURL == "" {
			log.Error("Provide pair-code and pair-url together")
			return 1
		}
		if err = pairing.Claim(ctx, c, *pairURL, *pairCode, store.AgentID()); err != nil {
			log.Error(err.Error())
			return 1
		}
		fmt.Println("Agent paired. Its private token was saved separately. Start or restart the agent to send readings.")
		return 0
	}
	log.Info("NUT UPS agent started", "version", version, "sources", len(c.Sources), "remote_delivery_enabled", c.Backend.URL != "")
	r := runner.Runner{Config: c, Store: store, Log: log}
	err = r.Run(ctx, *once)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error(err.Error())
		return 1
	}
	return 0
}
