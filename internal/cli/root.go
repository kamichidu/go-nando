package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/config"
	"github.com/kamichidu/go-nando/internal/content"
	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/objectstore/gcs"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

// ErrInvalidArguments is returned when CLI arguments are missing or invalid.
var ErrInvalidArguments = errors.New("invalid arguments")

// GlobalFlags represents common options available across all subcommands.
type GlobalFlags struct {
	Repository       string
	ConfigFile       string
	Verbose          bool
	VerboseSet       bool
	DaemonEndpoint   string
	DaemonTokenFile  string
	DaemonCACertFile string
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
	fs.Var(trackedBoolFlag{value: &c.flags.Verbose, wasSet: &c.flags.VerboseSet}, "verbose", "enable or disable verbose output")
	fs.Alias("verbose", "v")
	fs.StringVar(&c.flags.DaemonEndpoint, "daemon-endpoint", "", "daemon control endpoint (unix:///path or tcp://host:port)")
	fs.StringVar(&c.flags.DaemonTokenFile, "daemon-token-file", "", "private bearer-token file for TCP daemon access")
	fs.StringVar(&c.flags.DaemonCACertFile, "daemon-ca-cert", "", "CA certificate PEM for TCP daemon TLS")

	return flagshim.ContextWithFlag(ctx, &c.flags)
}

// NewDaemonClientFromContext resolves the configured or repository-default
// control endpoint without constructing a local Repository.
func NewDaemonClientFromContext(ctx context.Context) (*controlrpc.Client, error) {
	flags := flagshim.MustFlagFromContext[GlobalFlags](ctx)
	repoDir, _, err := ResolveRepositoryAndVerbose(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := ResolveConfigFromContext(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := flags.DaemonEndpoint
	if endpoint == "" {
		endpoint = cfg.DaemonEndpoint
	}
	if endpoint == "" {
		endpoint, err = controlrpc.DefaultEndpoint(repoDir)
		if err != nil {
			return nil, err
		}
	}
	var token string
	tokenFile := flags.DaemonTokenFile
	if tokenFile == "" {
		tokenFile = cfg.DaemonTokenFile
	}
	if tokenFile != "" {
		token, err = controlrpc.ReadTokenFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read daemon bearer token: %w", err)
		}
	}
	var caPEM []byte
	caCertFile := flags.DaemonCACertFile
	if caCertFile == "" {
		caCertFile = cfg.DaemonCACert
	}
	if caCertFile != "" {
		caPEM, err = os.ReadFile(caCertFile)
		if err != nil {
			return nil, fmt.Errorf("read daemon CA certificate: %w", err)
		}
	}
	return controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint, Token: token, CACertPEM: caPEM})
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

type trackedBoolFlag struct {
	value  *bool
	wasSet *bool
}

func (f trackedBoolFlag) String() string { return strconv.FormatBool(*f.value) }
func (f trackedBoolFlag) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*f.value = parsed
	*f.wasSet = true
	return nil
}
func (trackedBoolFlag) IsBoolFlag() bool { return true }

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
	var remoteErr *controlrpc.RemoteError
	if errors.As(err, &remoteErr) {
		switch remoteErr.Code {
		case controlrpc.CodeEntryNotFound:
			return 3
		case controlrpc.CodeEntryConflict:
			return 4
		case controlrpc.CodeInvalidParams:
			return 2
		}
	}

	if errors.Is(err, objectstore.ErrHashMismatch) ||
		strings.Contains(err.Error(), "corruption") ||
		strings.Contains(err.Error(), "verification") {
		return 5 // Repository corruption or verification failure
	}

	return 1 // General error
}

// ResolveConfigFromContext loads explicit or default TOML user configuration.
func ResolveConfigFromContext(ctx context.Context) (*config.Config, error) {
	flags := flagshim.MustFlagFromContext[GlobalFlags](ctx)
	cfg, _, err := config.LoadOptionalConfig(flags.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration file: %w", err)
	}
	return cfg, nil
}

// ResolveRepositoryAndVerbose applies CLI -> TOML config -> XDG data default.
func ResolveRepositoryAndVerbose(ctx context.Context) (string, bool, error) {
	gFlags := flagshim.MustFlagFromContext[GlobalFlags](ctx)
	cfg, err := ResolveConfigFromContext(ctx)
	if err != nil {
		return "", false, err
	}
	resolvedPaths, err := config.DefaultPaths()
	if err != nil {
		return "", false, fmt.Errorf("failed to determine XDG-style data directory: %w", err)
	}
	repoDir := resolvedPaths.DataDir
	if cfg.Repository != "" {
		repoDir = cfg.Repository
	}
	if gFlags.Repository != "" {
		repoDir = gFlags.Repository
	}
	verbose := cfg.Verbose
	if gFlags.VerboseSet {
		verbose = gFlags.Verbose
	}
	if cfg.Repository == "" && gFlags.Repository == "" {
		legacyDir, err := config.DefaultLegacyRepositoryDir()
		if err != nil {
			return "", false, fmt.Errorf("failed to resolve legacy repository location: %w", err)
		}
		legacyDB := filepath.Join(legacyDir, "metadata.sqlite")
		newDB := filepath.Join(repoDir, "metadata.sqlite")
		if filepath.Clean(legacyDir) != filepath.Clean(repoDir) {
			if _, err := os.Stat(newDB); errors.Is(err, os.ErrNotExist) {
				if _, legacyErr := os.Stat(legacyDB); legacyErr == nil {
					return "", false, fmt.Errorf("legacy repository found at %q; set repository in config.toml or pass --repository to select it explicitly", legacyDir)
				} else if !errors.Is(legacyErr, os.ErrNotExist) {
					return "", false, fmt.Errorf("inspect legacy repository location %q: %w", legacyDir, legacyErr)
				}
			} else if err != nil {
				return "", false, err
			}
		}
	}

	return repoDir, verbose, nil
}

// NewAppFromContext initializes the App layer and dependencies based on resolved context configuration.
func NewAppFromContext(ctx context.Context) (*app.App, string, func() error, error) {
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

	if err := metaStore.CheckMigrations(ctx); err != nil {
		return nil, "", nil, errors.Join(err, metaStore.Close())
	}
	repoFormat, found, err := metaStore.RepositoryFormat(ctx)
	if err != nil {
		return nil, "", nil, errors.Join(err, metaStore.Close())
	}
	if !found {
		return nil, "", nil, errors.Join(fmt.Errorf("%w: repository format metadata is missing", repositoryformat.ErrUnsupported), metaStore.Close())
	}
	if err := repoFormat.Validate(); err != nil {
		return nil, "", nil, errors.Join(err, metaStore.Close())
	}
	if err := metaStore.Migrate(ctx); err != nil {
		return nil, "", nil, errors.Join(fmt.Errorf("failed to apply metadata schema migrations: %w", err), metaStore.Close())
	}

	objectsDir := filepath.Join(repoDir, "objects")
	tempDir := filepath.Join(repoDir, "temp")
	repoSettings, err := app.LoadRepositorySettings(repoDir)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to load repository settings: %w", err)
	}
	hasher := content.NewHasher()

	objStore, err := local.NewStore(objectsDir, tempDir, hasher)
	if err != nil {
		return nil, "", nil, errors.Join(fmt.Errorf("failed to open object store: %w", err), metaStore.Close())
	}

	validatingMetaStore := metadata.NewValidatingStore(metaStore)
	var replica objectstore.ReplicaStore
	var gcsClient io.Closer
	if repoSettings.DeliveryMode == repository.DeliveryModeGCS {
		gcsStore, client, err := gcs.NewClientStore(ctx, repoSettings.GCSBucket)
		if err != nil {
			return nil, "", nil, errors.Join(err, metaStore.Close())
		}
		replica, gcsClient = gcsStore, client
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(repoDir, "coordination.db"))
	if err != nil {
		return nil, "", nil, errors.Join(fmt.Errorf("failed to open repository coordinator: %w", err), closeIfNotNil(gcsClient), metaStore.Close())
	}
	repo := repository.NewRepositoryWithReplica(validatingMetaStore, objStore, replica, hasher, coordinator, repoSettings)
	application := app.NewApp(repo)

	cleanup := func() error {
		return errors.Join(coordinator.Close(), closeIfNotNil(gcsClient), metaStore.Close())
	}

	return application, repoDir, cleanup, nil
}

func closeIfNotNil(closer io.Closer) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

// LogVerbose prints debug/tracing details to stderr if verbose logging is enabled.
func LogVerbose(ctx context.Context, format string, args ...any) {
	_, verbose, err := ResolveRepositoryAndVerbose(ctx)
	if err != nil {
		return
	}
	if verbose {
		stderr := flagshim.MustStderrFromContext(ctx)
		_, _ = fmt.Fprintf(stderr, "[verbose] "+format+"\n", args...)
	}
}
