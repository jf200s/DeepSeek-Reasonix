package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"reasonix/internal/base/i18n"
	"reasonix/internal/contract/config"
	"reasonix/internal/platform/remote"
	"reasonix/internal/platform/remote/attach"
)

// wslEntry returns the book's entry for name when it is a WSL machine. Those
// are reached through the attach pool, like the desktop's, and never through an
// SSH client.
func wslEntry(name string) (config.RemoteHostEntry, bool) {
	cfg, err := config.Load()
	if err != nil {
		return config.RemoteHostEntry{}, false
	}
	entry, ok := cfg.RemoteHost(name)
	return entry, ok && entry.HostKind() == config.RemoteKindWSL
}

var newWSLPool = func(ctx context.Context, install, version string) *attach.Pool {
	return attach.NewPool(ctx, attach.Options{
		Install:         install,
		LocalBinary:     currentExecutable(),
		Version:         version,
		FetchBinary:     fetchRemoteCLIBinary,
		ResolveDownload: resolveRemoteCLIDownload,
	})
}

func remoteWSLTest(name string, entry config.RemoteHostEntry, version string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newWSLPool(ctx, entry.ServeInstallMode(), version)
	defer pool.Close()
	rep, err := pool.Probe(ctx, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	fmt.Println("connection OK")
	printRemoteReport(rep)
	if !rep.Ready() {
		return 1
	}
	return 0
}

// remoteWSLConnect brings up the kernel in the distribution and prints where to
// reach it. The link is held while this command runs; the kernel outlives it
// and ends on its own idle timeout, or on `reasonix remote stop`.
func remoteWSLConnect(name string, entry config.RemoteHostEntry, syntax remoteConnectSyntax, version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ws := syntax.workspace
	if ws == "" {
		ws = entry.Workspace
	}
	pool := newWSLPool(ctx, entry.ServeInstallMode(), version)
	defer pool.Close()
	ep, err := pool.Attach(ctx, name, ws, attach.Call{
		Progress: func(step, detail string) { fmt.Fprintf(os.Stderr, i18n.M.RemoteBootstrapStepFmt+"\n", step, detail) },
		OnStatus: func(ev remote.StatusEvent) {
			switch ev.Status {
			case remote.StatusConnecting:
				fmt.Fprintf(os.Stderr, i18n.M.RemoteConnectingFmt+"\n", name)
			case remote.StatusConnected:
				fmt.Fprintf(os.Stderr, i18n.M.RemoteConnectedFmt+"\n", name)
			case remote.StatusReconnecting:
				fmt.Fprintf(os.Stderr, i18n.M.RemoteReconnectingFmt+"\n", name, ev.Attempt)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	defer ep.Release()
	url := fmt.Sprintf("http://%s/#token=%s", ep.Addr, ep.Token)
	fmt.Printf(i18n.M.RemoteServeReadyFmt+"\n", url)
	if syntax.open {
		_ = openInBrowser(url)
	}
	fmt.Fprintln(os.Stderr, "Press Ctrl-C to disconnect (the kernel keeps running for reconnect, then exits after its idle timeout).")
	<-ctx.Done()
	fmt.Fprintln(os.Stderr, i18n.M.RemoteDisconnected)
	return 0
}
