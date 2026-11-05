package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"reasonix/internal/base/i18n"
	"reasonix/internal/contract/config"
	"reasonix/internal/platform/wsl"
)

const remoteWSLUsage = "usage: reasonix remote wsl list | wsl test <distribution> | wsl add <name> <distribution> [--workspace dir] [--serve-install auto|npm|upload|never] [--idle-timeout MINUTES]"

func remoteWSLCLI(args []string) int {
	if len(args) > 0 && args[0] == "add" {
		return remoteWSLAddCLI(args[1:])
	}
	if len(args) == 0 || (args[0] != "list" && args[0] != "test") || (args[0] == "list" && len(args) != 1) || (args[0] == "test" && len(args) != 2) {
		fmt.Fprintln(os.Stderr, remoteWSLUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if args[0] == "list" {
		list, err := wsl.List(ctx, wsl.System())
		if err != nil {
			return wslFailure(err)
		}
		if len(list) == 0 {
			fmt.Println("no WSL distributions are installed")
		}
		for _, d := range list {
			mark := " "
			if d.Default {
				mark = "*"
			}
			fmt.Printf("%s %s (WSL %d)\n", mark, d.Name, d.Version)
		}
		return 0
	}
	p, err := wsl.Check(ctx, wsl.System(), args[1])
	if err != nil {
		return wslFailure(err)
	}
	fmt.Printf("  distribution  %s (WSL %d)\n  machine       linux/%s, user %s\n", p.Distro.Name, p.Distro.Version, p.Arch, p.User)
	return 0
}

func wslFailure(err error) int {
	fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
	if errors.Is(err, wsl.ErrUnavailable) {
		return 3
	}
	return 1
}

// remoteWSLAddCLI records a WSL distribution as a machine in the host book. The
// entry is validated by the config layer, which refuses a name wsl.exe could
// read as an option, so nothing here has to spell the rule again.
func remoteWSLAddCLI(args []string) int {
	if commandHelpRequested(args, 2) {
		fmt.Fprintln(os.Stdout, remoteWSLUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, remoteWSLUsage)
		return 2
	}
	name, distro := args[0], args[1]
	fs := newFlagSet("remote wsl add")
	workspace := fs.String("workspace", "", "default folder inside the distribution")
	install := fs.String("serve-install", "auto", "kernel install strategy: auto|npm|upload|never")
	idleTimeout := fs.Int("idle-timeout", config.DefaultRemoteIdleTimeoutMinutes, "minutes a kernel may sit idle before it exits (1..1440, -1 = never)")
	if code, ok := parseCommandFlags(fs, args[2:]); !ok {
		return code
	}
	idleSet := flagWasSet(fs, "idle-timeout")
	var idle *int
	if idleSet {
		idle = idleTimeout
	}
	entry := config.RemoteHostEntry{Name: name, Kind: config.RemoteKindWSL, Distro: distro, Workspace: *workspace, ServeInstall: *install, IdleTimeoutMinutes: idle}
	if err := config.EditUserConfigWithCredentials(func(c *config.Config) ([]config.CredentialChange, error) {
		if existing, ok := c.RemoteHost(name); ok {
			if existing.HostKind() != config.RemoteKindWSL {
				return nil, fmt.Errorf("%q is a %s machine; remove it first to add a WSL one under that name", name, existing.HostKind())
			}
			// Re-adding replaces the row whole. The flag's absence means "leave
			// the idle timeout as it was", not "go back to the global default".
			if !idleSet {
				entry.IdleTimeoutMinutes = existing.IdleTimeoutMinutes
			}
		}
		return nil, c.UpsertRemoteHost(entry)
	}); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	fmt.Printf("added WSL machine %q (%s)\n", name, entry.Target())
	return 0
}
