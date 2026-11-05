package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
)

func intPtr(v int) *int { return &v }

// TestProjectConfigCannotOverrideRemote pins [remote] as a user-global
// security control: a cloned repository's reasonix.toml must not be able to
// inject SSH hosts, jump chains, or port forwards.
func TestProjectConfigCannotOverrideRemote(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("REASONIX_HOME", "")
	globalDir := filepath.Dir(UserConfigPath())
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalTOML := "[remote]\n[[remote.hosts]]\nname = \"trusted\"\nhost = \"trusted.example\"\n"
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(globalTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	project := testenv.TempDir(t)
	projectTOML := "[remote]\n[[remote.hosts]]\nname = \"evil\"\nhost = \"attacker.example\"\nproxy_jump = \"attacker-jump\"\n"
	if err := os.WriteFile(filepath.Join(project, "reasonix.toml"), []byte(projectTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot() error = %v", err)
	}
	if len(cfg.Remote.Hosts) != 1 || cfg.Remote.Hosts[0].Name != "trusted" {
		t.Fatalf("remote hosts = %+v, want only the user-global \"trusted\" host", cfg.Remote.Hosts)
	}
	if _, ok := cfg.RemoteHost("evil"); ok {
		t.Error("project reasonix.toml injected a remote host; [remote] must stay user-global")
	}
}

func TestRemoteConfigDecodeAndDefaults(t *testing.T) {
	isolateUserConfigHome(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	toml := `
[remote]
import_ssh_config = true

[[remote.hosts]]
name = "gpu-box"
host = "203.0.113.7"
port = 2222
user = "dev"
identity_file = "~/.ssh/id_ed25519"
passphrase_env = "REASONIX_REMOTE_GPUBOX_PASSPHRASE"
proxy_jump = "bastion.corp"
workspace = "~/projects/app"
serve_install = "npm"
use_ssh_config = true

[[remote.hosts.forwards]]
type = "local"
bind = "127.0.0.1:5432"
target = "127.0.0.1:5432"

[[remote.hosts]]
name = "minimal"
host = "10.0.0.1"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Remote.ImportSSHConfig {
		t.Error("import_ssh_config not decoded")
	}
	h, ok := cfg.RemoteHost("gpu-box")
	if !ok {
		t.Fatal("gpu-box host missing")
	}
	if h.Port != 2222 || h.User != "dev" || h.ProxyJump != "bastion.corp" || !h.UseSSHConfig {
		t.Fatalf("gpu-box decoded wrong: %+v", h)
	}
	if h.ServeInstallMode() != "npm" {
		t.Fatalf("ServeInstallMode = %q", h.ServeInstallMode())
	}
	if len(h.Forwards) != 1 || h.Forwards[0].Type != "local" || h.Forwards[0].Bind != "127.0.0.1:5432" {
		t.Fatalf("forwards decoded wrong: %+v", h.Forwards)
	}
	m, ok := cfg.RemoteHost("minimal")
	if !ok {
		t.Fatal("minimal host missing")
	}
	if m.PortOrDefault() != 22 {
		t.Fatalf("PortOrDefault = %d, want 22", m.PortOrDefault())
	}
	if m.ServeInstallMode() != "auto" {
		t.Fatalf("default ServeInstallMode = %q, want auto", m.ServeInstallMode())
	}
}

// A WSL distribution runs on another platform, so the copy this side would
// upload can never run there. A book entry that still names that strategy —
// hand-edited, or left from before the form stopped offering it — reads as the
// strategy that can work instead of failing on every connect.
func TestAWSLHostNeverTakesTheUploadStrategy(t *testing.T) {
	entry := RemoteHostEntry{Name: "wsl", Kind: RemoteKindWSL, Distro: "Ubuntu", ServeInstall: "upload"}
	if got := entry.ServeInstallMode(); got != "auto" {
		t.Fatalf("ServeInstallMode = %q, want auto", got)
	}
	ssh := RemoteHostEntry{Name: "box", Host: "10.0.0.4", ServeInstall: "upload"}
	if got := ssh.ServeInstallMode(); got != "upload" {
		t.Fatalf("an ssh host lost its upload strategy: %q", got)
	}
}

// TestUpsertRemoteHostRoundTripsThroughSave pins that hosts written via the
// CRUD helpers survive a full user-scope re-render (SaveTo renders the whole
// file from the struct — a missing [remote] renderer would silently drop
// every saved host on the next unrelated settings save).
func TestUpsertRemoteHostRoundTripsThroughSave(t *testing.T) {
	isolateUserConfigHome(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("default_model = \"deepseek\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadForEdit(path)
	if cfg == nil {
		t.Fatal("LoadForEdit returned nil")
	}
	host := RemoteHostEntry{
		Name:          "box",
		Host:          "198.51.100.4",
		Port:          22,
		User:          "dev",
		PassphraseEnv: "REASONIX_REMOTE_BOX_PASSPHRASE",
		Forwards:      []RemoteForwardEntry{{Type: "local", Bind: "127.0.0.1:8080", Target: "127.0.0.1:80"}},
	}
	if err := cfg.UpsertRemoteHost(host); err != nil {
		t.Fatalf("UpsertRemoteHost: %v", err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[[remote.hosts]]", `name = "box"`, `host = "198.51.100.4"`, "[[remote.hosts.forwards]]", `bind = "127.0.0.1:8080"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("saved config missing %q:\n%s", want, raw)
		}
	}

	reloaded := LoadForEdit(path)
	got, ok := reloaded.RemoteHost("box")
	if !ok {
		t.Fatal("host lost after save/reload")
	}
	if got.PassphraseEnv != host.PassphraseEnv || len(got.Forwards) != 1 {
		t.Fatalf("host mutated across round-trip: %+v", got)
	}

	// Replace + remove.
	host.User = "ops"
	if err := reloaded.UpsertRemoteHost(host); err != nil {
		t.Fatal(err)
	}
	if h, _ := reloaded.RemoteHost("box"); h.User != "ops" || len(reloaded.Remote.Hosts) != 1 {
		t.Fatalf("upsert did not replace in place: %+v", reloaded.Remote.Hosts)
	}
	if !reloaded.RemoveRemoteHost("box") {
		t.Fatal("RemoveRemoteHost reported missing")
	}
	if reloaded.RemoveRemoteHost("box") {
		t.Fatal("second remove reported present")
	}
}

func TestUpsertRemoteHostValidates(t *testing.T) {
	cfg := Default()
	bad := []RemoteHostEntry{
		{Name: "", Host: "h"},
		{Name: "a b", Host: "h"},
		{Name: "user@host", Host: "h"},
		{Name: "ok", Host: ""},
		{Name: "ok", Host: "h", Port: 70000},
		{Name: "ok", Host: "h", ServeInstall: "curlpipe"},
		{Name: "ok", Host: "h", Forwards: []RemoteForwardEntry{{Type: "dynamic", Bind: "1", Target: "2"}}},
		{Name: "ok", Host: "h", Forwards: []RemoteForwardEntry{{Type: "local", Bind: "", Target: "2"}}},
		{Name: "ok", Host: "h", Forwards: []RemoteForwardEntry{{Type: "local", Bind: "abc", Target: "svc:80"}}},
		{Name: "ok", Host: "h", Forwards: []RemoteForwardEntry{{Type: "local", Bind: "8080", Target: "svc:0"}}},
		{Name: "ok", Host: "h", Forwards: []RemoteForwardEntry{{Type: "local", Bind: "8080", Target: "svc:80"}, {Type: "local", Bind: "127.0.0.1:8080", Target: "other:80"}}},
	}
	for i, e := range bad {
		if err := cfg.UpsertRemoteHost(e); err == nil {
			t.Errorf("case %d (%+v): invalid host accepted", i, e)
		}
	}
	if len(cfg.Remote.Hosts) != 0 {
		t.Fatalf("invalid hosts persisted: %+v", cfg.Remote.Hosts)
	}
}

// TestRemoteCredentialEnvNamesCollected pins that remote passphrase/password
// env names flow into CredentialEnvNames -> secrets.RegisterCredentialEnvKeys
// so they are filtered from tool subprocess environments.
func TestRemoteCredentialEnvNamesCollected(t *testing.T) {
	cfg := Default()
	cfg.Remote.Hosts = []RemoteHostEntry{
		{Name: "a", Host: "h1", PassphraseEnv: "REMOTE_A_PASSPHRASE"},
		{Name: "b", Host: "h2", PasswordEnv: "REMOTE_B_PASSWORD"},
		{Name: "c", Host: "h3", PassphraseEnv: "REMOTE_A_PASSPHRASE"}, // dup collapses
	}
	names := credentialEnvNamesFromConfig(cfg)
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["REMOTE_A_PASSPHRASE"] || !got["REMOTE_B_PASSWORD"] {
		t.Fatalf("remote credential envs missing from %v", names)
	}
}

func TestRemotePathHelpers(t *testing.T) {
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	if got := RemoteStateDir(); got != filepath.Join(home, "remote") {
		t.Fatalf("RemoteStateDir = %q", got)
	}
	if got := RemoteKnownHostsPath(); got != filepath.Join(home, "remote", "known_hosts") {
		t.Fatalf("RemoteKnownHostsPath = %q", got)
	}
}

// One machine holds several projects, and which one a bare connect lands in is
// still a single answer. The pair is stored so an older config that only ever
// wrote `workspace` is a list of one, and so nothing can leave the two of them
// disagreeing about which folder is the default.
func TestRemoteHostWorkspacesAreOneDefaultFirstList(t *testing.T) {
	cases := []struct {
		name  string
		entry RemoteHostEntry
		want  []string
	}{
		{"nothing set", RemoteHostEntry{}, nil},
		{"only the old field", RemoteHostEntry{Workspace: "/srv/a"}, []string{"/srv/a"}},
		{"default first, then the rest", RemoteHostEntry{
			Workspace: "/srv/a", Workspaces: []string{"/srv/b", "/srv/c"},
		}, []string{"/srv/a", "/srv/b", "/srv/c"}},
		{"the default repeated in the list is one folder", RemoteHostEntry{
			Workspace: "/srv/a", Workspaces: []string{"/srv/a", "/srv/b"},
		}, []string{"/srv/a", "/srv/b"}},
		{"blanks and padding are not folders", RemoteHostEntry{
			Workspace: "  ", Workspaces: []string{" /srv/b ", "", "/srv/b"},
		}, []string{"/srv/b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.entry.WorkspaceList()
			if len(got) != len(tc.want) {
				t.Fatalf("WorkspaceList() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("WorkspaceList() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A save is where the two fields are made to agree: whatever list came in, the
// head of it is the default a bare `reasonix remote connect` uses.
func TestUpsertRemoteHostMakesTheHeadOfTheListTheDefault(t *testing.T) {
	cfg := Default()
	err := cfg.UpsertRemoteHost(RemoteHostEntry{
		Name: "box", Host: "198.51.100.4",
		Workspaces: []string{"/srv/eval", "/srv/train", "/srv/eval"},
	})
	if err != nil {
		t.Fatalf("UpsertRemoteHost: %v", err)
	}
	got, _ := cfg.RemoteHost("box")
	if got.Workspace != "/srv/eval" {
		t.Fatalf("default workspace = %q, want the head of the list", got.Workspace)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0] != "/srv/train" {
		t.Fatalf("workspaces = %v, want the default not repeated in the rest", got.Workspaces)
	}
	if list := got.WorkspaceList(); len(list) != 2 {
		t.Fatalf("WorkspaceList() = %v, want both folders once each", list)
	}
}

// The list has to survive the file the same way the forwards beside it do.
func TestRemoteWorkspacesSurviveSaveAndReload(t *testing.T) {
	isolateUserConfigHome(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("default_model = \"deepseek\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadForEdit(path)
	if err := cfg.UpsertRemoteHost(RemoteHostEntry{
		Name: "box", Host: "198.51.100.4",
		Workspaces: []string{"/srv/train", "/srv/eval"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "workspaces = ") {
		t.Fatalf("saved config never wrote the extra projects:\n%s", raw)
	}
	got, ok := LoadForEdit(path).RemoteHost("box")
	if !ok {
		t.Fatal("host lost after save/reload")
	}
	if list := got.WorkspaceList(); len(list) != 2 || list[0] != "/srv/train" || list[1] != "/srv/eval" {
		t.Fatalf("WorkspaceList() after reload = %v", list)
	}
}

// Model credentials default to this machine's, resolved over the tunnel: a
// host needing them configured a second time is what made a first connect a
// setup task rather than a connection.
func TestRemoteProviderModeDefaultsToLocal(t *testing.T) {
	cases := []struct {
		name  string
		entry RemoteHostEntry
		want  string
	}{
		{"unset", RemoteHostEntry{}, RemoteProviderLocal},
		{"blank", RemoteHostEntry{Provider: "   "}, RemoteProviderLocal},
		{"local", RemoteHostEntry{Provider: "local"}, RemoteProviderLocal},
		{"remote", RemoteHostEntry{Provider: "remote"}, RemoteProviderRemote},
		{"cased", RemoteHostEntry{Provider: "ReMoTe"}, RemoteProviderRemote},
		{"padded", RemoteHostEntry{Provider: " remote "}, RemoteProviderRemote},
		// An entry written by a newer Reasonix reads as the default here rather
		// than being reinterpreted as the other mode.
		{"unknown", RemoteHostEntry{Provider: "broker-v2"}, RemoteProviderLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.ProviderMode(); got != tc.want {
				t.Fatalf("ProviderMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaceRemoteHostOrdersTheBook(t *testing.T) {
	var c Config
	for _, name := range []string{"a", "b", "c"} {
		if err := c.UpsertRemoteHost(RemoteHostEntry{Name: name, Host: "10.0.0.1"}); err != nil {
			t.Fatal(err)
		}
	}
	order := func() string {
		var out strings.Builder
		for _, h := range c.Remote.Hosts {
			out.WriteString(h.Name)
		}
		return out.String()
	}
	for _, step := range []struct {
		name, before string
		changed      bool
		want         string
	}{
		{"c", "a", true, "cab"},
		{"c", "a", false, "cab"},
		{"c", "", true, "abc"},
		{"a", "a", false, "abc"},
		{"a", "missing", false, "abc"},
		{"missing", "a", false, "abc"},
	} {
		if got := c.PlaceRemoteHost(step.name, step.before); got != step.changed || order() != step.want {
			t.Fatalf("place %s before %q = %v, order %s; want %v, %s", step.name, step.before, got, order(), step.changed, step.want)
		}
	}
}

// A kernel kept alive for reconnect needs an owner: the idle timeout says how
// long it may sit with no client. It is a global default with a per-host
// override, and the zero value is meaningful, so it is stored as a pointer in
// both places.
func TestRemoteIdleTimeoutDecodesGloballyAndPerHost(t *testing.T) {
	isolateUserConfigHome(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	toml := `
[remote]
idle_timeout_minutes = 30

[[remote.hosts]]
name = "override"
host = "10.0.0.1"
idle_timeout_minutes = 0

[[remote.hosts]]
name = "inherit"
host = "10.0.0.2"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Remote.IdleTimeoutMinutes == nil || *cfg.Remote.IdleTimeoutMinutes != 30 {
		t.Fatalf("global idle timeout = %v, want 30", cfg.Remote.IdleTimeoutMinutes)
	}
	override, _ := cfg.RemoteHost("override")
	if override.IdleTimeoutMinutes == nil || *override.IdleTimeoutMinutes != 0 {
		t.Fatalf("per-host idle timeout = %v, want 0", override.IdleTimeoutMinutes)
	}
	inherit, _ := cfg.RemoteHost("inherit")
	if inherit.IdleTimeoutMinutes != nil {
		t.Fatalf("unset per-host idle timeout = %v, want nil", inherit.IdleTimeoutMinutes)
	}
}

func TestRemoteIdleTimeoutAccessorDefaultsAndOverrides(t *testing.T) {
	wsl := RemoteHostEntry{Name: "wsl", Kind: RemoteKindWSL, Distro: "Ubuntu"}
	cases := []struct {
		name   string
		global *int
		entry  RemoteHostEntry
		want   time.Duration
	}{
		// An SSH kernel keeps its long-standing stay-resident behaviour: nobody
		// asked for an idle exit, so it is never reported.
		{"ssh default never exits", nil, RemoteHostEntry{}, -time.Minute},
		// A WSL distribution is kept alive for reconnects, and a kernel nobody
		// stops has to leave on its own.
		{"wsl default keeps it ten minutes", nil, wsl, 10 * time.Minute},
		{"global default", intPtr(30), RemoteHostEntry{}, 30 * time.Minute},
		{"global default reaches wsl", intPtr(30), wsl, 30 * time.Minute},
		{"host override", intPtr(30), RemoteHostEntry{IdleTimeoutMinutes: intPtr(5)}, 5 * time.Minute},
		{"host override reaches wsl", nil, RemoteHostEntry{Kind: RemoteKindWSL, Distro: "Ubuntu", IdleTimeoutMinutes: intPtr(5)}, 5 * time.Minute},
		{"zero exits at once", intPtr(30), RemoteHostEntry{IdleTimeoutMinutes: intPtr(0)}, 0},
		{"zero exits at once on wsl", nil, RemoteHostEntry{Kind: RemoteKindWSL, Distro: "Ubuntu", IdleTimeoutMinutes: intPtr(0)}, 0},
		{"minus one never", nil, RemoteHostEntry{IdleTimeoutMinutes: intPtr(-1)}, -time.Minute},
		{"minus one never on wsl", nil, RemoteHostEntry{Kind: RemoteKindWSL, Distro: "Ubuntu", IdleTimeoutMinutes: intPtr(-1)}, -time.Minute},
		{"today's maximum", nil, RemoteHostEntry{IdleTimeoutMinutes: intPtr(1440)}, 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.IdleTimeout(tc.global); got != tc.want {
				t.Fatalf("IdleTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// The global [remote] idle timeout is read from the user file, so a value
// outside what a kernel can honour has to be refused where it is loaded rather
// than turned into a duration on a later connect.
func TestGlobalRemoteIdleTimeoutOutOfRangeIsRefusedAtLoad(t *testing.T) {
	for _, bad := range []int{-5, 99999} {
		t.Run(strconv.Itoa(bad), func(t *testing.T) {
			home := testenv.TempDir(t)
			t.Setenv("REASONIX_HOME", home)
			body := fmt.Sprintf("[remote]\nidle_timeout_minutes = %d\n", bad)
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted idle_timeout_minutes = %d", bad)
			} else if !strings.Contains(err.Error(), "idle_timeout_minutes") || !strings.Contains(err.Error(), "-1..1440") {
				t.Fatalf("Load error = %v, want the off-range value named", err)
			}
		})
	}
}

// A WSL host can never take the upload strategy, so a save must write the value
// that will actually be used instead of preserving one that cannot.
func TestWSLUploadStrategyRendersAsTheEffectiveAuto(t *testing.T) {
	c := Default()
	c.Remote.Hosts = []RemoteHostEntry{{Name: "wsl", Kind: RemoteKindWSL, Distro: "Ubuntu", ServeInstall: "upload"}}
	out := RenderTOMLForScope(c, RenderScopeUser)
	if strings.Contains(out, `serve_install = "upload"`) {
		t.Fatalf("render preserved a strategy a WSL host can never use:\n%s", out)
	}
	if !strings.Contains(out, `serve_install = "auto"`) {
		t.Fatalf("render did not write the effective auto strategy:\n%s", out)
	}
}

func TestRemoteIdleTimeoutIsValidatedAndSurvivesASave(t *testing.T) {
	isolateUserConfigHome(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("default_model = \"deepseek\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadForEdit(path)
	if cfg == nil {
		t.Fatal("LoadForEdit returned nil")
	}
	for _, bad := range []int{-2, 1441} {
		if err := cfg.UpsertRemoteHost(RemoteHostEntry{Name: "box", Host: "h", IdleTimeoutMinutes: intPtr(bad)}); err == nil {
			t.Fatalf("idle_timeout_minutes %d was accepted", bad)
		}
	}
	for _, good := range []int{-1, 0, 1440} {
		if err := cfg.UpsertRemoteHost(RemoteHostEntry{Name: "box", Host: "h", IdleTimeoutMinutes: intPtr(good)}); err != nil {
			t.Fatalf("idle_timeout_minutes %d was refused: %v", good, err)
		}
	}

	// Leave a value on the host and one on the section; a renderer that forgets
	// either line drops it on the next unrelated save.
	if err := cfg.UpsertRemoteHost(RemoteHostEntry{Name: "box", Host: "h", IdleTimeoutMinutes: intPtr(0)}); err != nil {
		t.Fatal(err)
	}
	cfg.Remote.IdleTimeoutMinutes = intPtr(15)
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"idle_timeout_minutes = 15", "idle_timeout_minutes = 0"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("saved config missing %q:\n%s", want, raw)
		}
	}

	reloaded := LoadForEdit(path)
	if reloaded.Remote.IdleTimeoutMinutes == nil || *reloaded.Remote.IdleTimeoutMinutes != 15 {
		t.Fatalf("global idle timeout after save/reload = %v, want 15", reloaded.Remote.IdleTimeoutMinutes)
	}
	got, ok := reloaded.RemoteHost("box")
	if !ok || got.IdleTimeoutMinutes == nil || *got.IdleTimeoutMinutes != 0 {
		t.Fatalf("host idle timeout after save/reload = %v", got.IdleTimeoutMinutes)
	}
}

// A per-host idle timeout is read from the same user file as the [remote]
// default, and a hand-written value outside -1..1440 has to be refused there
// with the host it belongs to named, not turned into a duration on a connect.
func TestPerHostRemoteIdleTimeoutOutOfRangeIsRefusedAtLoad(t *testing.T) {
	for _, bad := range []int{-5, 99999} {
		t.Run(strconv.Itoa(bad), func(t *testing.T) {
			home := testenv.TempDir(t)
			t.Setenv("REASONIX_HOME", home)
			body := fmt.Sprintf("[remote]\n[[remote.hosts]]\nname = \"box\"\nhost = \"10.0.0.1\"\nidle_timeout_minutes = %d\n", bad)
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted per-host idle_timeout_minutes = %d", bad)
			} else if !strings.Contains(err.Error(), `remote host "box"`) || !strings.Contains(err.Error(), "-1..1440") {
				t.Fatalf("Load error = %v, want the host name and range named", err)
			}
		})
	}
}

// The edit loaders decode the same book, so a settings surface cannot load an
// out-of-range override and offer to save it back.
func TestPerHostRemoteIdleTimeoutOutOfRangeIsRefusedForEdit(t *testing.T) {
	for _, bad := range []int{-5, 99999} {
		t.Run(strconv.Itoa(bad), func(t *testing.T) {
			home := testenv.TempDir(t)
			t.Setenv("REASONIX_HOME", home)
			path := filepath.Join(home, "config.toml")
			body := fmt.Sprintf("[remote]\n[[remote.hosts]]\nname = \"box\"\nhost = \"10.0.0.1\"\nidle_timeout_minutes = %d\n", bad)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadForEditReadOnlyStrict(path); err == nil {
				t.Fatalf("LoadForEditReadOnlyStrict accepted per-host idle_timeout_minutes = %d", bad)
			} else if !strings.Contains(err.Error(), `remote host "box"`) || !strings.Contains(err.Error(), "-1..1440") {
				t.Fatalf("LoadForEditReadOnlyStrict error = %v, want the host name and range named", err)
			}
			// LoadForEdit recovers, but the recovered config refuses to save over
			// the file it could not read.
			if err := LoadForEdit(path).SaveTo(path); err == nil {
				t.Fatal("LoadForEdit's recovered config saved over an out-of-range override")
			}
		})
	}
}

// The accepted range itself still loads and reaches the host unchanged.
func TestPerHostRemoteIdleTimeoutInRangeLoads(t *testing.T) {
	for _, good := range []int{-1, 0, 1440} {
		t.Run(strconv.Itoa(good), func(t *testing.T) {
			home := testenv.TempDir(t)
			t.Setenv("REASONIX_HOME", home)
			path := filepath.Join(home, "config.toml")
			body := fmt.Sprintf("[remote]\n[[remote.hosts]]\nname = \"box\"\nhost = \"10.0.0.1\"\nidle_timeout_minutes = %d\n", good)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load refused in-range idle_timeout_minutes = %d: %v", good, err)
			}
			host, ok := cfg.RemoteHost("box")
			if !ok || host.IdleTimeoutMinutes == nil || *host.IdleTimeoutMinutes != good {
				t.Fatalf("host idle timeout = %v, want %d", host.IdleTimeoutMinutes, good)
			}
			if _, err := LoadForEditReadOnlyStrict(path); err != nil {
				t.Fatalf("LoadForEditReadOnlyStrict refused in-range idle_timeout_minutes = %d: %v", good, err)
			}
		})
	}
}

// A config mutated in memory is guarded too: SaveTo is the last step before
// the value is on disk, so it must refuse what Load would refuse on the next
// start, whichever save entry point a caller uses.
func TestSaveToRefusesOutOfRangeRemoteHostIdleTimeout(t *testing.T) {
	for _, bad := range []int{-5, 99999} {
		t.Run(strconv.Itoa(bad), func(t *testing.T) {
			c := Default()
			c.Remote.Hosts = []RemoteHostEntry{{Name: "box", Host: "10.0.0.1", IdleTimeoutMinutes: intPtr(bad)}}
			dir := testenv.TempDir(t)
			for name, save := range map[string]func(string) error{
				"SaveTo":      func(path string) error { return c.SaveTo(path) },
				"SaveToScope": func(path string) error { return c.SaveToScope(path, RenderScopeUser) },
			} {
				path := filepath.Join(dir, name+".toml")
				err := save(path)
				if err == nil {
					t.Fatalf("%s accepted per-host idle_timeout_minutes = %d", name, bad)
				}
				if !strings.Contains(err.Error(), `remote host "box"`) || !strings.Contains(err.Error(), "-1..1440") {
					t.Fatalf("%s error = %v, want the host name and range named", name, err)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("%s wrote %s despite the refusal", name, path)
				}
			}
		})
	}
}

// The run_code kernel clamps both of these values, so a render that keeps the
// written number would show a limit the next run silently lowers.
func TestPTCRenderConvergesOnTheKernelCaps(t *testing.T) {
	c := Default()
	timeout := 99999
	c.Tools.PTC.TimeoutSeconds = &timeout
	c.Tools.PTC.MaxParallel = 99
	out := RenderTOMLForScope(c, RenderScopeUser)
	for _, want := range []string{
		"timeout_seconds = 600",
		"max_parallel = 16",
		"the kernel caps it at 600",
		"the kernel caps it at 16",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render is missing %q:\n%s", want, out)
		}
	}

	// Values under the caps are written as configured.
	timeout = 45
	c.Tools.PTC.MaxParallel = 3
	out = RenderTOMLForScope(c, RenderScopeUser)
	if !strings.Contains(out, "timeout_seconds = 45") || !strings.Contains(out, "max_parallel = 3") {
		t.Fatalf("render changed in-range values:\n%s", out)
	}
}
