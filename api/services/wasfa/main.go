package main

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/ardanlabs/conf/v3"
	"github.com/lmittmann/tint"
)

var build = "develop"

func main() {
	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{
		AddSource:  true,
		Level:      slog.LevelDebug,
		TimeFormat: time.DateTime,
	})).With("service", "sales")

	ctx := context.Background()
	if err := run(ctx, logger); err != nil {
		logger.ErrorContext(ctx, "startup", "msg", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {

	// =========================================================================
	// GOMAXPROCS

	log.InfoContext(ctx, "startup", "GOMAXPROCS", runtime.GOMAXPROCS(0))

	// =========================================================================
	// Configuration

	cfg := struct {
		conf.Version
		Web struct {
			ReadTimeout        time.Duration `conf:"default:5s"`
			WriteTimeOut       time.Duration `conf:"default:10s"`
			IdleTimeout        time.Duration `conf:"default:120s"`
			ShutdownTimeout    time.Duration `conf:"default:20s"`
			APIHost            string        `conf:"default:0.0.0.0:3000"`
			DebugHost          string        `conf:"default:0.0.0.0:3010"`
			CORSAllowedOrigins []string      `conf:"default:*"`
		}
		DB struct {
			MaxIdleConns int  `conf:"default:0"`
			MaxOpenConns int  `conf:"default:0"`
			DisableTLS   bool `conf:"default:true"`
		}
	}{
		Version: conf.Version{
			Build: build,
			Desc:  "pastry",
		},
	}

	if help, err := conf.Parse("", &cfg); err != nil {
		if errors.Is(err, conf.ErrHelpWanted) {
			fmt.Println(help)
			return nil
		}
		return fmt.Errorf("parsing config: %w", err)
	}

	// =========================================================================
	// App Starting

	log.InfoContext(ctx, "starting service", "version", cfg.Build)
	defer log.InfoContext(ctx, "shutdown complete")

	log.InfoContext(ctx, "startup", "conf", cfg)

	expvar.NewString("build").Set(cfg.Build)

	// -------------------------------------------------------------------------
	// Start Debug Service

	go func() {
		log.InfoContext(ctx, "startup", "status", "debug v1 router started", "host", cfg.Web.DebugHost)

	}()

	// =========================================================================
	// Start API Service

	log.InfoContext(ctx, "startup", "status", "initializing API support")

	// Make a channel to listen for an interrupt or terminal signal from the OS.
	// Use a buffered channel because the signal package require it.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	// Construct a server to service the request against the mux.
	api := http.Server{
		Addr:         cfg.Web.APIHost,
		ReadTimeout:  cfg.Web.ReadTimeout,
		WriteTimeout: cfg.Web.WriteTimeOut,
		IdleTimeout:  cfg.Web.IdleTimeout,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	// Make a channel to listen for errors coming from the listener. Use a
	// buffered channel so the goroutine can exit if we don't collect this
	// error.

	serveErrors := make(chan error, 1)

	// Start the service listening for api requests.
	go func() {
		log.InfoContext(ctx, "startup", "status", "api router started", "host", api.Addr)
		serveErrors <- api.ListenAndServe()
	}()

	// =========================================================================
	// Shutdown

	// Blocking main and waiting for shutdown
	select {
	case err := <-serveErrors:
		return fmt.Errorf("server error: %w", err)

	case sig := <-shutdown:
		log.InfoContext(ctx, "shutdown", "status", "shutdown started", "signal", sig)
		defer log.InfoContext(ctx, "shutdown", "status", "shutdown complete", "signal", sig)

		// give outstanding requests a deadline for completion.
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Web.ShutdownTimeout)
		defer cancel()

		// Asking listener to shut down and shed load.
		if err := api.Shutdown(ctx); err != nil {
			api.Close()
			return fmt.Errorf("could not stop server gracefully: %w", err)
		}
	}

	return nil
}
