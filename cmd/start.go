package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/peterbourgon/ff/v3/ffcli"
	"go.uber.org/zap"

	"github.com/gnolang/tx-indexer/client"
	"github.com/gnolang/tx-indexer/events"
	"github.com/gnolang/tx-indexer/fetch"
	"github.com/gnolang/tx-indexer/serve"
	"github.com/gnolang/tx-indexer/storage"
)

const (
	defaultRemote = "http://127.0.0.1:26657"
	defaultDBPath = "indexer-db"
)

type startCfg struct {
	listenAddress string
	remote        string
	dbPath        string
	logLevel      string

	maxSlots     int
	maxChunkSize int64
}

// newStartCmd creates the indexer start command
func newStartCmd() *ffcli.Command {
	cfg := &startCfg{}

	fs := flag.NewFlagSet("start", flag.ExitOnError)
	cfg.registerFlags(fs)

	return &ffcli.Command{
		Name:       "start",
		ShortUsage: "start [flags]",
		ShortHelp:  "Starts the indexer service",
		LongHelp:   "Starts the indexer service, which includes the fetcher and JSON-RPC server",
		FlagSet:    fs,
		Exec: func(ctx context.Context, _ []string) error {
			return cfg.exec(ctx)
		},
	}
}

// registerFlags registers the indexer start command flags
func (c *startCfg) registerFlags(fs *flag.FlagSet) {
	fs.StringVar(
		&c.listenAddress,
		"listen-address",
		serve.DefaultListenAddress,
		"the IP:PORT URL for the indexer JSON-RPC server",
	)

	fs.StringVar(
		&c.remote,
		"remote",
		defaultRemote,
		"the JSON-RPC URL of the Gno chain",
	)

	fs.StringVar(
		&c.dbPath,
		"db-path",
		defaultDBPath,
		"the absolute path for the indexer DB (embedded)",
	)

	fs.StringVar(
		&c.logLevel,
		"log-level",
		zap.InfoLevel.String(),
		"the log level for the CLI output",
	)

	fs.IntVar(
		&c.maxSlots,
		"max-slots",
		fetch.DefaultMaxSlots,
		"the amount of slots (workers) the fetcher employs",
	)

	fs.Int64Var(
		&c.maxChunkSize,
		"max-chunk-size",
		fetch.DefaultMaxChunkSize,
		"the range for fetching blockchain data by a single worker",
	)
}

// exec executes the indexer start command
func (c *startCfg) exec(ctx context.Context) error {
	// Parse the log level
	logLevel, err := zap.ParseAtomicLevel(c.logLevel)
	if err != nil {
		return fmt.Errorf("unable to parse log level, %w", err)
	}

	cfg := zap.NewDevelopmentConfig()
	cfg.Level = logLevel

	// Create a new logger
	logger, err := cfg.Build()
	if err != nil {
		return fmt.Errorf("unable to create logger, %w", err)
	}

	// Create a DB instance
	db, err := storage.NewPebble(c.dbPath)
	if err != nil {
		return fmt.Errorf("unable to open storage DB, %w", err)
	}

	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("unable to gracefully close DB", zap.Error(err))
		}
	}()

	// Create an Event Manager instance
	em := events.NewManager()

	// Create the fetcher service
	f := fetch.New(
		db,
		client.NewClient(c.remote),
		em,
		fetch.WithLogger(
			logger.Named("fetcher"),
		),
		fetch.WithMaxSlots(c.maxSlots),
		fetch.WithMaxChunkSize(c.maxChunkSize),
	)

	// Create the JSON-RPC service
	j := setupJSONRPC(
		c.listenAddress,
		db,
		em,
		logger,
	)

	// Create a new waiter
	w := newWaiter(ctx)

	// Add the fetcher service
	w.add(f.FetchChainData)

	// Add the JSON-RPC service
	w.add(j.Serve)

	// Wait for the services to stop
	return errors.Join(
		w.wait(),
		logger.Sync(),
	)
}

// setupJSONRPC sets up the JSONRPC instance
func setupJSONRPC(
	listenAddress string,
	db *storage.Pebble,
	em *events.Manager,
	logger *zap.Logger,
) *serve.JSONRPC {
	j := serve.NewJSONRPC(
		em,
		serve.WithLogger(
			logger.Named("json-rpc"),
		),
		serve.WithListenAddress(
			listenAddress,
		),
	)

	// Transaction handlers
	j.RegisterTxEndpoints(db)

	// Block handlers
	j.RegisterBlockEndpoints(db)

	// Sub handlers
	j.RegisterSubEndpoints(db)

	return j
}
