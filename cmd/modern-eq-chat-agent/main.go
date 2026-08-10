// Command modern-eq-chat-agent connects one EQEMU server to a Modern EQ Chat hub.
//
// The agent reports local chat upward and injects what the hub sends back. It
// holds no Discord credentials and no other server's token, and it dials out,
// so the machine it runs on needs no inbound ports and no fixed address.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/Zero-Hex/modern-eq-chat/client"
	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/service"
	"github.com/Zero-Hex/modern-eq-chat/setup"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
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

	migrateLegacyFiles()

	if len(args) > 0 {
		tlog.Init(nil, os.Stdout)

		switch args[0] {
		case "setup":
			if err := setup.RunAgent(context.Background(), setup.NewPrompter(), config.DefaultPath); err != nil {
				return err
			}
			waitOnWindows()
			return nil
		case "service":
			return runServiceCommand(args[1:])
		case "version", "--version", "-v":
			fmt.Printf("modern-eq-chat-agent %s\n", Version)
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

// serve runs the agent until the stop channel closes. service.Run supplies
// that channel from signals on Linux or from the Windows Service Control
// Manager, so a console run and a service run share one code path.
func serve() error {
	return service.Run(serviceName, func(stop <-chan struct{}) error {
		w, err := os.Create("modern-eq-chat.log")
		if err != nil {
			return fmt.Errorf("create log: %w", err)
		}
		defer w.Close()
		tlog.Init(w, os.Stdout)

		tlog.Infof("starting modern-eq-chat-agent %s", Version)

		cfg, err := config.Load(config.DefaultPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if cfg.Relay.Mode != config.ModeAgent {
			return fmt.Errorf("modern-eq-chat.conf has relay.mode = %q; run 'modern-eq-chat-agent setup' to configure this box as an agent", cfg.Relay.Mode)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c, err := client.New(ctx)
		if err != nil {
			return fmt.Errorf("new client: %w", err)
		}
		if err := c.Connect(ctx); err != nil {
			return fmt.Errorf("connect: %w", err)
		}

		select {
		case <-ctx.Done():
		case <-stop:
			if err := c.Disconnect(ctx); err != nil {
				return fmt.Errorf("disconnect: %w", err)
			}
			tlog.Infof("exiting, stop requested")
		}

		tlog.Sync()
		return nil
	})
}

// migrateLegacyFiles moves a pre-rename installation onto the current
// filenames, reporting anything it changed.
//
// This runs before any command, including setup: an operator upgrading from
// TalkEQ should find their existing configuration picked up, not be dropped
// into a fresh setup wizard because the file is named differently now.
func migrateLegacyFiles() {
	actions, err := config.Migrate(config.DefaultPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not migrate files from the previous name: %s\n", err)
		return
	}
	for _, action := range actions {
		fmt.Printf("migrated: %s\n", action)
	}
}

func usage() {
	fmt.Println("usage: modern-eq-chat-agent [command]")
	fmt.Println()
	fmt.Println("With no command, connects to the hub and relays chat.")
	fmt.Println()
	fmt.Println("  setup                              interactive configuration and enrollment")
	fmt.Println("  service install|start|stop|status  run as a background service")
	fmt.Println("  version                            print the version")
}

func waitOnWindows() {
	if runtime.GOOS != "windows" {
		return
	}
	fmt.Println("\nPress enter to close.")
	fmt.Scanln()
}
