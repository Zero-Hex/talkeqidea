package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"github.com/Zero-Hex/modern-eq-chat/client"
	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

// Version is the build version
var Version string

func main() {
	// Relay administration lives in modern-eq-chat-hub. This binary is the original
	// single-server modern-eq-chat and stays that way.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "agent", "enroll", "setup":
			fmt.Printf("%q is a relay command. Use modern-eq-chat-hub or modern-eq-chat-agent instead.\n", os.Args[1])
			fmt.Println("This binary runs the original single-server modern-eq-chat.")
			os.Exit(1)
		}
	}

	if actions, err := config.Migrate(config.DefaultPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not migrate files from the previous name: %s\n", err)
	} else {
		for _, action := range actions {
			fmt.Printf("migrated: %s\n", action)
		}
	}

	w, err := os.Create("modern-eq-chat.log")
	if err != nil {
		fmt.Println(err)
		if runtime.GOOS == "windows" {
			option := ""
			fmt.Println("press a key then enter to exit.")
			fmt.Scan(&option)
		}
		os.Exit(1)
	}
	defer w.Close()
	tlog.Init(w, os.Stdout)

	err = run(w)
	if err != nil {
		tlog.Errorf("run failed with error: %s", err)
		if runtime.GOOS == "windows" {
			option := ""
			fmt.Println("press a key then enter to exit.")
			fmt.Scan(&option)
		}
		tlog.Sync()
		os.Exit(1)
	}
	tlog.Infof("exited safely")
	tlog.Sync()
	os.Exit(0)
}

func run(w *os.File) (err error) {

	if Version == "" {
		Version = "1.x.x EXPERIMENTAL"
	}
	tlog.Infof("starting modern-eq-chat %s", Version)
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	tlog.Infof("working directory is %s", wd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	c, err := client.New(ctx)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}

	err = c.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	select {
	case <-ctx.Done():
	case <-signalChan:
		err = c.Disconnect(ctx)
		if err != nil {
			return fmt.Errorf("signal disconnect: %w", err)
		}
		tlog.Infof("exiting, interrupt signal sent")
	}
	return
}
