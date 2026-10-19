// Command onyx-ingestor syncs external content into Onyx via its ingestion
// API. Add new sources by implementing ingest.Source under internal/
// and registering a branch below.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"onyx-ingestor/internal/config"
	"onyx-ingestor/internal/confluence"
	"onyx-ingestor/internal/ingest"
	"onyx-ingestor/internal/onyx"
)

// version may be set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	// Load .env when present; real environment variables take precedence
	// (godotenv.Load never overrides variables that are already set).
	if err := godotenv.Load(); err == nil {
		slog.Debug("loaded configuration from .env")
	}
	if err := run(context.Background(), os.Args[1:]); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command provided")
	}
	switch args[0] {
	case "sync":
		if len(args) < 2 {
			usage()
			return errors.New("sync requires a source, e.g. `sync confluence`")
		}
		return runSync(ctx, args[1], args[2:])
	case "version":
		fmt.Printf("onyx-ingestor %s\n", version)
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `onyx-ingestor syncs external content into Onyx.

Usage:
  onyx-ingestor sync confluence [--cql CQL] [--page-size N] [--max-pages N] [--dry-run]
                                [--max-failures N] [--incremental] [--state-file PATH]
                                [--sync-lookback D]
  onyx-ingestor version

Configuration comes from environment variables; see README.md.
`)
}

// runSync resolves the named source and runs the ingestion pipeline. To add
// a source (e.g. Google Drive), implement ingest.Source in
// internal/<name> and add a case here.
func runSync(ctx context.Context, sourceName string, args []string) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: cfg.HTTPTimeout}
	sink := onyx.NewClient(cfg.OnyxBaseURL, cfg.OnyxAPIKey, cfg.OnyxCCPairID, cfg.OnyxIngestionPath, hc)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var source ingest.Source
	var opts ingest.Options
	var incremental bool
	var stateFile string

	switch sourceName {
	case "confluence":
		fs := flag.NewFlagSet("sync confluence", flag.ContinueOnError)
		cql := fs.String("cql", "", "CQL query selecting pages to sync (overrides CONFLUENCE_CQL)")
		pageSize := fs.Int("page-size", 50, "pages per Confluence API request (max 100)")
		maxPages := fs.Int("max-pages", 0, "stop after N API requests (0 = no limit)")
		dryRun := fs.Bool("dry-run", false, "fetch and map pages without calling Onyx")
		maxFailures := fs.Int("max-failures", 10, "failed documents tolerated before the run aborts (0 = abort on first failure)")
		inc := fs.Bool("incremental", false, "only sync pages modified since the last successful run")
		sf := fs.String("state-file", ".onyx-ingestor-state.json", "checkpoint file for --incremental")
		lookback := fs.Duration("sync-lookback", 2*time.Minute, "overlap subtracted from the last sync time to absorb indexing lag")
		if err := fs.Parse(args); err != nil {
			return err
		}
		incremental = *inc
		stateFile = *sf
		if *cql != "" {
			cfg.ConfluenceCQL = *cql
		}
		if cfg.ConfluenceCQL == "" {
			return fmt.Errorf("CQL is required: set CONFLUENCE_CQL or pass --cql")
		}

		mode, err := confluence.ResolveMode(cfg.ConfluenceAuthType, cfg.ConfluenceUsername, cfg.ConfluenceCloudID)
		if err != nil {
			return err
		}
		client, err := confluence.NewClient(confluence.Options{
			BaseURL: cfg.ConfluenceBaseURL,
			Auth: confluence.Auth{
				Mode:     mode,
				Username: cfg.ConfluenceUsername,
				Token:    cfg.ConfluenceToken,
				CloudID:  cfg.ConfluenceCloudID,
			},
		}, hc)
		if err != nil {
			return err
		}
		slog.Info("confluence auth", "mode", mode.String())
		if total, cerr := client.CountPages(ctx, cfg.ConfluenceCQL); cerr != nil {
			slog.Warn("could not count confluence pages; progress will run without a total", "error", cerr)
		} else {
			slog.Info("confluence pages matching CQL", "cql", cfg.ConfluenceCQL, "total", total)
		}
		since := time.Time{}
		if incremental {
			st, err := ingest.LoadState(stateFile)
			if err != nil {
				return err
			}
			if st.Source == sourceName && !st.LastSync.IsZero() {
				since = st.LastSync.Add(-*lookback)
				slog.Info("incremental sync", "since", since.Format(time.RFC3339), "state_file", stateFile)
			} else {
				slog.Info("incremental sync: no prior checkpoint, running a full sync to bootstrap", "state_file", stateFile)
			}
		}
		if since.IsZero() {
			source = confluence.NewSource(client, cfg.ConfluenceCQL, *pageSize, *maxPages)
		} else {
			source = confluence.NewIncrementalSource(client, cfg.ConfluenceCQL, since, *pageSize, *maxPages)
		}
		opts.DryRun = *dryRun
		opts.MaxFailures = *maxFailures
	default:
		usage()
		return fmt.Errorf("unsupported source %q: only confluence is implemented", sourceName)
	}

	syncStarted := time.Now().UTC()
	stats, err := ingest.Run(ctx, source, sink, opts)
	slog.Info("sync finished",
		"source", sourceName,
		"seen", stats.Seen,
		"ingested", stats.Ingested,
		"skipped", stats.Skipped,
		"failed", stats.Failed,
		"dry_run", opts.DryRun,
	)
	// Advance the checkpoint only after a fully clean run. A failure or a
	// tolerated-but-failed document leaves the old timestamp, so the next
	// run re-scans the whole window (cheap: unchanged pages are skipped by
	// Onyx via doc_updated_at).
	if err == nil && incremental && !opts.DryRun {
		if stats.Failed > 0 {
			slog.Warn("documents failed; checkpoint not advanced",
				"failed", stats.Failed, "state_file", stateFile)
			return nil
		}
		if serr := ingest.SaveState(stateFile, ingest.State{Source: sourceName, LastSync: syncStarted}); serr != nil {
			return fmt.Errorf("save sync checkpoint: %w", serr)
		}
	}
	return err
}
