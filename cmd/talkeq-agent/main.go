// Command talkeq-agent connects one EQEMU server to a TalkEQ hub.
//
// The agent reports local chat upward and injects what the hub sends back. It
// holds no Discord credentials and no other server's token, and it dials out,
// so the machine it runs on needs no inbound ports and no fixed address.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"github.com/xackery/talkeq/client"
	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/setup"
	"github.com/xackery/talkeq/tlog"
)

// Version is set at build time.
var Version string

func main() {
	if Version == "" {
		Version = "1.x.x EXPERIMENTAL"
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %s\n", err)
		waitOnWindows()
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]

	if len(args) > 0 {
		tlog.Init(nil, os.Stdout)

		switch args[0] {
		case "setup":
			if err := setup.RunAgent(context.Background(), setup.NewPrompter(), config.DefaultPath); err != nil {
				return err
			}
			waitOnWindows()
			return nil
		case "version", "--version", "-v":
			fmt.Printf("talkeq-agent %s\n", Version)
			return nil
		case "help", "--help", "-h":
			usage()
			return nil
		default:
			usage()
			return fmt.Errorf("unknown command %q", args[0])
		}
	}

	if !config.Exists(config.DefaultPath) {
		tlog.Init(nil, os.Stdout)
		fmt.Printf("No %s found, starting setup.\n", config.DefaultPath)
		if err := setup.RunAgent(context.Background(), setup.NewPrompter(), config.DefaultPath); err != nil {
			return err
		}
		waitOnWindows()
		return nil
	}

	return serve()
}

func serve() error {
	w, err := os.Create("talkeq.log")
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer w.Close()
	tlog.Init(w, os.Stdout)

	tlog.Infof("starting talkeq-agent %s", Version)

	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.Relay.Mode != config.ModeAgent {
		return fmt.Errorf("talkeq.conf has relay.mode = %q; run 'talkeq-agent setup' to configure this box as an agent", cfg.Relay.Mode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	c, err := client.New(ctx)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	if err := c.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	select {
	case <-ctx.Done():
	case <-signalChan:
		if err := c.Disconnect(ctx); err != nil {
			return fmt.Errorf("disconnect: %w", err)
		}
		tlog.Infof("exiting, interrupt signal sent")
	}

	tlog.Sync()
	return nil
}

func usage() {
	fmt.Println("usage: talkeq-agent [command]")
	fmt.Println()
	fmt.Println("With no command, connects to the hub and relays chat.")
	fmt.Println()
	fmt.Println("  setup     interactive configuration and enrollment")
	fmt.Println("  version   print the version")
}

func waitOnWindows() {
	if runtime.GOOS != "windows" {
		return
	}
	fmt.Println("\nPress enter to close.")
	fmt.Scanln()
}
