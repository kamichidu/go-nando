package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/config"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/internal/repository"
)

// ErrInvalidArguments is returned when CLI arguments are missing or invalid.
var ErrInvalidArguments = errors.New("invalid arguments")

// GlobalFlags represents common options available across all subcommands.
type GlobalFlags struct {
	Repository string
	ConfigFile string
	Verbose    bool
}

// RootCommand represents the base "nando" command.
type RootCommand struct {
	flags    GlobalFlags
	children []flagshim.Command
}

// NewRootCommand creates a new RootCommand instance with subcommands.
func NewRootCommand(children []flagshim.Command) *RootCommand {
	return &RootCommand{
		children: children,
	}
}

// Name returns the command name.
func (c *RootCommand) Name() string {
	return "nando"
}

// RegisterFlags defines the global command-line options.
func (c *RootCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.flags.Repository, "repository", "", "repository directory")
	fs.Alias("repository", "r")
	fs.StringVar(&c.flags.ConfigFile, "config", "", "configuration file path")
	fs.Alias("config", "c")
	fs.BoolVar(&c.flags.Verbose, "verbose", false, "enable verbose output")
	fs.Alias("verbose", "v")

	return flagshim.ContextWithFlag(ctx, &c.flags)
}

// Commands exposes subcommands of the root command.
func (c *RootCommand) Commands() []flagshim.Command {
	return c.children
}

// Run executes the root command if no subcommand matches.
func (c *RootCommand) Run(ctx context.Context, args []string) error {
	// If no subcommand was parsed, show usage.
	return flag.ErrHelp
}

// MapError translates internal errors to specific process exit codes.
func (c *RootCommand) MapError(err error) int {
	if err == nil {
		return 0
	}

	if errors.Is(err, ErrInvalidArguments) {
		return 2
	}

	if errors.Is(err, flag.ErrHelp) {
		return 0
	}

	var parseErr *flagshim.ParseError
	if errors.As(err, &parseErr) {
		if errors.Is(parseErr.Err, flag.ErrHelp) {
			return 0
		}
		return 2 // Invalid arguments
	}

	if errors.Is(err, metadata.ErrEntryNotFound) || errors.Is(err, objectstore.ErrObjectNotFound) {
		return 3 // Not Found
	}

	if errors.Is(err, metadata.ErrEntryConflict) {
		return 4 // Conflict
	}

	if errors.Is(err, objectstore.ErrHashMismatch) ||
		strings.Contains(err.Error(), "corruption") ||
		strings.Contains(err.Error(), "verification") {
		return 5 // Repository corruption or verification failure
	}

	return 1 // General error
}

// ResolveRepositoryAndVerbose resolves configuration based on CLI -> Config File -> Default priority.
func ResolveRepositoryAndVerbose(ctx context.Context) (string, bool, error) {
	gFlags := flagshim.MustFlagFromContext[GlobalFlags](ctx)

	// 1. Set Default
	repoDir, err := paths.DefaultRepositoryDir()
	if err != nil {
		return "", false, fmt.Errorf("failed to determine default user configuration directory: %w", err)
	}
	verbose := false

	// 2. Load Config File if provided
	if gFlags.ConfigFile != "" {
		cfg, err := config.LoadConfig(gFlags.ConfigFile)
		if err != nil {
			return "", false, fmt.Errorf("failed to load configuration file: %w", err)
		}
		if cfg.Repository != "" {
			repoDir = cfg.Repository
		}
		verbose = cfg.Verbose
	}

	// 3. Override with CLI Options
	if gFlags.Repository != "" {
		repoDir = gFlags.Repository
	}
	if gFlags.Verbose {
		verbose = true
	}

	return repoDir, verbose, nil
}

// NewAppFromContext initializes the App layer and dependencies based on resolved context configuration.
func NewAppFromContext(ctx context.Context) (*app.App, string, func(), error) {
	repoDir, _, err := ResolveRepositoryAndVerbose(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	dbPath := filepath.Join(repoDir, "metadata.sqlite")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, "", nil, fmt.Errorf("repository is not initialized (run 'nando init' first)")
	}

	metaStore, err := sqlite.NewStore(dbPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to open metadata store: %w", err)
	}

	objectsDir := filepath.Join(repoDir, "objects")
	tempDir := filepath.Join(repoDir, "temp")
	hasher := content.NewHasher()

	objStore, err := local.NewStore(objectsDir, tempDir, hasher)
	if err != nil {
		_ = metaStore.Close()
		return nil, "", nil, fmt.Errorf("failed to open object store: %w", err)
	}

	repo := repository.NewRepository(metaStore, objStore, hasher)
	application := app.NewApp(repo)

	cleanup := func() {
		_ = metaStore.Close()
	}

	return application, repoDir, cleanup, nil
}

// LogVerbose prints debug/tracing details to stderr if verbose logging is enabled.
func LogVerbose(ctx context.Context, format string, args ...any) {
	_, verbose, _ := ResolveRepositoryAndVerbose(ctx)
	if verbose {
		stderr := flagshim.MustStderrFromContext(ctx)
		_, _ = fmt.Fprintf(stderr, "[verbose] "+format+"\n", args...)
	}
}
