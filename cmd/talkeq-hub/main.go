// Command talkeq-hub runs the central relay hub: the box that holds the
// Discord bot, routes chat between game servers, and authorizes agents.
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

	// Administrative subcommands log to the console only. They are short-lived
	// and should not contend with a running hub for talkeq.log.
	if len(args) > 0 {
		tlog.Init(nil, os.Stdout)

		switch args[0] {
		case "setup":
			return setup.RunHub(context.Background(), setup.NewPrompter(), config.DefaultPath)
		case "agent":
			return runAgentCommand(args[1:])
		case "enroll":
			return runEnrollCommand(args[1:])
		case "status":
			return runStatusCommand()
		case "version", "--version", "-v":
			fmt.Printf("talkeq-hub %s\n", Version)
			return nil
		case "help", "--help", "-h":
			usage()
			return nil
		default:
			usage()
			return fmt.Errorf("unknown command %q", args[0])
		}
	}

	// No arguments: run the hub, or walk a first-time operator through setup.
	if !config.Exists(config.DefaultPath) {
		tlog.Init(nil, os.Stdout)
		fmt.Printf("No %s found, starting setup.\n", config.DefaultPath)
		if err := setup.RunHub(context.Background(), setup.NewPrompter(), config.DefaultPath); err != nil {
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

	tlog.Infof("starting talkeq-hub %s", Version)

	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.Relay.Mode != config.ModeHub {
		return fmt.Errorf("talkeq.conf has relay.mode = %q; run 'talkeq-hub setup' to configure this box as a hub", cfg.Relay.Mode)
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
	fmt.Println("usage: talkeq-hub [command]")
	fmt.Println()
	fmt.Println("With no command, runs the hub.")
	fmt.Println()
	fmt.Println("  setup                              interactive configuration")
	fmt.Println("  enroll <server-key> [display]      issue a code for a new server")
	fmt.Println("  enroll list                        show outstanding codes")
	fmt.Println("  enroll revoke <id>                 cancel an outstanding code")
	fmt.Println("  agent list                         show authorized servers")
	fmt.Println("  agent add <server-key> [display]   authorize a server, print a join code")
	fmt.Println("  agent rotate <server-key>          issue a new token")
	fmt.Println("  agent remove <server-key>          revoke a server")
	fmt.Println("  agent disable|enable <server-key>  block or restore a server")
	fmt.Println("  status                             show configured servers")
	fmt.Println("  version                            print the version")
}

// waitOnWindows keeps a double-clicked console window open long enough to be
// read.
func waitOnWindows() {
	if runtime.GOOS != "windows" {
		return
	}
	fmt.Println("\nPress enter to close.")
	fmt.Scanln()
}
