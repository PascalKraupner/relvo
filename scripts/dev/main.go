// This development-only launcher gives Air's child foreground terminal access.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

func run() error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("just dev requires an interactive terminal: %w", err)
	}
	defer tty.Close()
	fd := int(tty.Fd())
	previous, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return err
	}
	// Taking foreground ownership from a background group otherwise stops us.
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	group := unix.Getpgrp()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, group); err != nil {
		return err
	}
	defer func() { _ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, previous) }()

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	cmd := exec.Command("./tmp/relvo", os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopping := false
	for {
		select {
		case err := <-done:
			if stopping {
				// Diagnostics printed during a build were in the TUI's alternate
				// screen. Replay them after Bubble Tea has restored the terminal.
				if output, err := os.ReadFile("./tmp/build.log"); err == nil && len(output) > 0 {
					_, _ = tty.Write(output)
				}
				return nil
			}
			return err
		case <-signals:
			stopping = true
			// Air may signal just the launcher or the entire process tree.
			// Relvo handles SIGTERM via context cancellation and cleans its socket.
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
