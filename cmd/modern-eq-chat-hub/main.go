// Command modern-eq-chat-hub runs the central relay hub: the box that holds the
// Discord bot, routes chat between game servers, and authorizes agents.
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

	// Administrative subcommands log to the console only. They are short-lived
	// and should not contend with a running hub for modern-eq-chat.log.
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
		case "service":
			return runServiceCommand(args[1:])
		case "firewall":
			return runFirewallCommand(args[1:])
		case "unban":
			return runUnbanCommand(args[1:])
		case "web":
			return runWebCommand(args[1:])
		case "version", "--version", "-v":
			fmt.Printf("modern-eq-chat-hub %s\n", Version)
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

// serve runs the hub until the stop channel closes.
//
// service.Run supplies that channel from whichever source applies: SIGINT and
// SIGTERM on Linux, or a Windows Service Control Manager stop request. Wiring
// it this way means one code path serves a console run and a service run.
func serve() error {
	return service.Run(serviceName, func(stop <-chan struct{}) error {
		w, err := os.Create("modern-eq-chat.log")
		if err != nil {
			return fmt.Errorf("create log: %w", err)
		}
		defer w.Close()
		tlog.Init(w, os.Stdout)

		tlog.Infof("starting modern-eq-chat-hub %s", Version)

		cfg, err := config.Load(config.DefaultPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if cfg.Relay.Mode != config.ModeHub {
			return fmt.Errorf("modern-eq-chat.conf has relay.mode = %q; run 'modern-eq-chat-hub setup' to configure this box as a hub", cfg.Relay.Mode)
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

// runUnbanCommand lifts a temporary block. Bans live in the running hub's
// memory, so this only reports how to clear one.
func runUnbanCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: modern-eq-chat-hub unban <address>")
	}
	fmt.Printf("Blocks are held in memory by the running hub and are cleared by\n")
	fmt.Printf("restarting it:\n\n")
	fmt.Printf("    modern-eq-chat-hub service stop && modern-eq-chat-hub service start\n\n")
	fmt.Printf("To stop %s being blocked again, add it to allowed_networks in\n", args[0])
	fmt.Printf("modern-eq-chat.conf, or raise auth_failures_before_ban.\n")
	return nil
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
	fmt.Println("usage: modern-eq-chat-hub [command]")
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
	fmt.Println("  service install|start|stop|status  run as a background service")
	fmt.Println("  firewall [--apply]                 show or apply the rule opening the hub port")
	fmt.Println("  unban <address>                    lift a temporary block early")
	fmt.Println("  web password|enable|disable        local management interface")
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
