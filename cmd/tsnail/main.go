// Command tsnail is a peer-to-peer terminal Snake game played over your own
// tailnet.
//
// Every instance is a full peer: it runs an embedded Tailscale node, listens
// for other players on a well-known port, hosts or joins lobbies, and gossips
// signed match results with everyone it meets. There is no server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/theolol/tailsnail/internal/discovery"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/store"
	"github.com/theolol/tailsnail/internal/tsnode"
	"github.com/theolol/tailsnail/internal/ui"
	"github.com/theolol/tailsnail/internal/ui/theme"
	"github.com/theolol/tailsnail/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tsnail: "+err.Error())
		os.Exit(1)
	}
}

// options holds the parsed command line.
type options struct {
	hostname string
	stateDir string
	verbose  bool
	ascii    bool
	color    string
	version  bool
}

// run dispatches the command line.
func run(args []string) error {
	// The history subcommand is peeled off before the main flag set so that
	// `tsnail history export --json` reads naturally.
	if len(args) > 0 && args[0] == "history" {
		return runHistory(args[1:])
	}

	var opts options
	fs := flag.NewFlagSet("tsnail", flag.ContinueOnError)
	fs.StringVar(&opts.hostname, "hostname", "", "tailnet hostname for this node (default tsnail-<hostname>)")
	fs.StringVar(&opts.stateDir, "state-dir", "", "directory for node state, identity and match history")
	fs.BoolVar(&opts.verbose, "verbose", false, "also mirror the in-app log to a file in the state directory")
	fs.BoolVar(&opts.ascii, "ascii", false, "draw with plain ASCII instead of Unicode glyphs")
	fs.StringVar(&opts.color, "color", "auto", "colour depth: "+strings.Join(theme.ModeNames(), "|"))
	fs.BoolVar(&opts.version, "version", false, "print the version and exit")
	fs.Usage = func() { usage(fs) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.version {
		fmt.Println("tsnail " + version.String())
		return nil
	}
	return runTUI(opts)
}

// usage prints help text.
func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, `tsnail — peer-to-peer terminal Snake over your tailnet

Usage:
  tsnail [flags]                 launch the game
  tsnail history export --json   print stored match records as JSON

Flags:
`)
	fs.PrintDefaults()
}

// runHistory implements the non-interactive history subcommand.
func runHistory(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return errors.New("usage: tsnail history export --json")
	}
	var (
		stateDir string
		asJSON   bool
	)
	fs := flag.NewFlagSet("tsnail history export", flag.ContinueOnError)
	fs.StringVar(&stateDir, "state-dir", "", "directory holding match history")
	fs.BoolVar(&asJSON, "json", false, "emit JSON (the only supported format)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !asJSON {
		return errors.New("tsnail history export currently supports only --json")
	}

	dir, err := resolveStateDir(stateDir)
	if err != nil {
		return err
	}
	st, problems, err := store.Open(dir)
	if err != nil {
		return err
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "tsnail: "+p.Error())
	}
	raw, err := st.ExportJSON()
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(raw)
	return err
}

// resolveStateDir returns the state directory, creating it if needed.
func resolveStateDir(override string) (string, error) {
	dir := override
	if dir == "" {
		var err error
		if dir, err = store.DefaultStateDir(); err != nil {
			return "", err
		}
	}
	if err := store.EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// runTUI wires every subsystem together and hands control to Bubble Tea.
func runTUI(opts options) error {
	// Validate the flags before anything environmental, so a typo reports the
	// typo rather than whatever else happens to be wrong.
	colorMode, ok := theme.ParseMode(opts.color)
	if !ok {
		return fmt.Errorf("unknown --color value %q; expected one of %s",
			opts.color, strings.Join(theme.ModeNames(), ", "))
	}
	// tailsnail is an interactive program and nothing else. Failing early with
	// an explanation beats emitting escape sequences into a pipe.
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("this is an interactive program; run it in a terminal")
	}

	stateDir, err := resolveStateDir(opts.stateDir)
	if err != nil {
		return err
	}

	// Everything the libraries log goes into the ring from here on, so that
	// nothing can write over the interface.
	log := logring.New(logring.DefaultCapacity)
	defer log.Close()
	if opts.verbose {
		if err := log.MirrorTo(store.LogPath(stateDir)); err != nil {
			return err
		}
		log.Logf("tsnail %s starting; state in %s", version.String(), stateDir)
	}

	settings, err := store.LoadSettings(stateDir)
	if err != nil {
		// Corrupt settings are not fatal: note it and carry on with defaults.
		log.Logf("settings: %v", err)
	}
	ident, err := store.LoadOrCreateIdentity(stateDir, store.SuggestDisplayName())
	if err != nil {
		return err
	}
	if settings.DisplayName != "" && settings.DisplayName != ident.DisplayName {
		ident.DisplayName = settings.DisplayName
	}
	records, problems, err := store.Open(stateDir)
	if err != nil {
		return err
	}
	for _, p := range problems {
		log.Logf("%v", p)
	}

	// A single context governs every goroutine, so quitting tears the whole
	// program down in one move.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hostname := opts.hostname
	if hostname == "" {
		osHost, _ := os.Hostname()
		hostname = tsnode.DefaultHostname(osHost)
	}
	node, err := tsnode.Start(ctx, tsnode.Options{
		StateDir: stateDir,
		Hostname: hostname,
		// TS_AUTHKEY exists for CI and scripted testing. Leaving it unset is
		// the normal path and is what produces the interactive device login.
		AuthKey: os.Getenv("TS_AUTHKEY"),
		Log:     log,
	})
	if err != nil {
		return err
	}
	defer node.Close()

	server := netplay.NewServer(node, records, ident, log)
	go func() {
		if err := server.Serve(ctx); err != nil {
			log.Logf("netplay: %v", err)
		}
	}()

	prober := discovery.New(discovery.Options{
		Dialer:      node,
		Store:       records,
		Log:         log,
		PubKey:      ident.PubKey(),
		DisplayName: ident.DisplayName,
		Hostname:    hostname,
	})
	go prober.Run(ctx)

	app := &ui.App{
		Ctx:       ctx,
		Node:      node,
		Server:    server,
		Prober:    prober,
		Store:     records,
		Ident:     ident,
		Log:       log,
		StateDir:  stateDir,
		Settings:  settings,
		ASCIIFlag: opts.ascii,
		ColorFlag: colorMode,
	}

	program := tea.NewProgram(ui.New(app), tea.WithAltScreen(), tea.WithContext(ctx))

	// A signal from outside must leave the terminal in a usable state, so it
	// goes through the same quit path as pressing q rather than exiting hard.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			program.Quit()
		case <-ctx.Done():
		}
	}()

	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}

	// Shut the network down before returning so no listener or session is left
	// half-open behind the restored terminal, and so any clients are told why
	// the lobby went away rather than just seeing the socket drop.
	server.Shutdown("the host quit")
	cancel()
	return nil
}
