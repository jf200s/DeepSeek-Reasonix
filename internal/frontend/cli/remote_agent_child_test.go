package cli

import (
	"testing"

	"reasonix/internal/base/secrets"
	"reasonix/internal/contract/config"
)

// Every remote subcommand that writes the machine book or opens a link is a
// person's, and a subcommand nobody has classified is too.
func TestRemoteCommandsThatChangeOrOpenAreRefusedForAnAgentChild(t *testing.T) {
	isolateCLIConfigHome(t)
	if code := remoteAddCLI([]string{"box", "ada@10.0.0.4"}); code != 0 {
		t.Fatal(code)
	}
	t.Setenv(secrets.AgentChildEnv, "1")
	for _, args := range [][]string{
		{"add", "x", "ada@10.0.0.5"},
		{"remove", "box"}, {"rm", "box"},
		{"import", "--all"},
		{"wsl", "add", "dev", "Ubuntu"},
		{"test", "box"},
		{"connect", "box"}, {"open", "box"},
		{"status"}, {"status", "box"},
		{"forward", "add", "box", "-L", "8080:127.0.0.1:80"}, {"forward", "rm", "box", "x"}, {"forward", "ls", "box"},
		{"serve", "start", "box"}, {"serve", "stop", "box"},
		{"stop", "box"},
		{"fs", "ls", "box:/"}, {"fs", "get", "box:/x"}, {"fs", "put", "x", "box:/x"},
		{"attach-workspace"}, {"runtime-workbench"}, {"a-subcommand-added-later"},
	} {
		if code := remoteCommand(args, "test"); code != exitAgentChild {
			t.Errorf("remote %v = %d, want %d", args, code, exitAgentChild)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Remote.Hosts) != 1 || cfg.Remote.Hosts[0].Name != "box" {
		t.Fatalf("a refused command changed the book: %+v", cfg.Remote.Hosts)
	}
	for _, args := range [][]string{{"list"}, {"ls"}, {"help"}, {"wsl", "list"}, {"wsl", "test", "no-such-distribution"}} {
		if code := remoteCommand(args, "test"); code == exitAgentChild {
			t.Errorf("remote %v was refused, it only reads", args)
		}
	}
}

func TestRemoteCommandsAreNotRefusedForAPerson(t *testing.T) {
	isolateCLIConfigHome(t)
	t.Setenv(secrets.AgentChildEnv, "")
	if code := remoteCommand([]string{"add", "box", "ada@10.0.0.4"}, "test"); code != 0 {
		t.Fatalf("add = %d", code)
	}
}
