package main

//go:generate go run github.com/google/wire/cmd/wire

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
)

//go:embed VERSION
var embeddedVersion string

// Build-time variables (can be set by ldflags)
var (
	Version   = ""
	Commit    = "unknown"
	Date      = "unknown"
	BuildType = "source" // "source" for manual builds, "release" for CI builds (set by ldflags)
)

func init() {
	// 如果 Version 已通过 ldflags 注入（例如 -X main.Version=...），则不要覆盖。
	if strings.TrimSpace(Version) != "" {
		return
	}

	// 默认从 embedded VERSION 文件读取版本号（编译期打包进二进制）。
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

// initLogger configures the default slog handler based on gin.Mode().
// In non-release mode, Debug level logs are enabled.
func main() {
	// Parse command line flags
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	migrateOnly := flag.Bool("migrate-only", false, "Apply database migrations and exit")
	migrationPlanJSON := flag.Bool("migration-plan-json", false, "Print a read-only database migration plan as JSON and exit")
	migrationPlanSnapshotJSON := flag.String("migration-plan-snapshot-json", "", "Print a read-only migration plan from an immutable schema_migrations snapshot and exit")
	migrationApplyPlanJSON := flag.String("migration-apply-plan-json", "", "Apply a verified migration plan from JSON and exit")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		log.Printf("Sub2API %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}
	if *migrationPlanJSON || *migrationPlanSnapshotJSON != "" {
		if *migrationPlanJSON && *migrationPlanSnapshotJSON != "" {
			log.Fatal("Only one migration plan input may be selected")
		}
		if *migrationPlanSnapshotJSON != "" {
			data, err := os.ReadFile(*migrationPlanSnapshotJSON)
			if err != nil {
				log.Fatalf("Failed to read migration snapshot: %v", err)
			}
			var snapshot struct {
				SchemaMigrations []repository.MigrationRecord `json:"schema_migrations"`
			}
			if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.SchemaMigrations == nil {
				log.Fatalf("Failed to decode migration snapshot")
			}
			plan, planErr := repository.PlanEmbeddedMigrationsFromRecords(snapshot.SchemaMigrations)
			encoded, err := repository.MigrationPlanJSON(plan)
			if err != nil {
				log.Fatalf("Failed to encode migration plan: %v", err)
			}
			if _, err := fmt.Fprintln(os.Stdout, string(encoded)); err != nil {
				log.Fatalf("Failed to write migration plan: %v", err)
			}
			if planErr != nil {
				log.Fatalf("Migration plan rejected: %v", planErr)
			}
			return
		}
		cfg, err := config.LoadForBootstrap()
		if err != nil {
			log.Fatalf("Failed to load migration config: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		plan, planErr := repository.PlanEmbeddedMigrations(ctx, cfg)
		cancel()
		data, err := repository.MigrationPlanJSON(plan)
		if err != nil {
			log.Fatalf("Failed to encode migration plan: %v", err)
		}
		if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
			log.Fatalf("Failed to write migration plan: %v", err)
		}
		if planErr != nil {
			log.Fatalf("Migration plan rejected: %v", planErr)
		}
		return
	}

	// Machine-readable migration plan commands must keep stdout JSON-only. The
	// bootstrap logger writes informational entries to stdout, so initialize it
	// only after these commands have returned.
	logger.InitBootstrap()
	defer logger.Sync()

	if *migrationApplyPlanJSON != "" {
		data, err := os.ReadFile(*migrationApplyPlanJSON)
		if err != nil {
			log.Fatalf("Failed to read migration execution plan: %v", err)
		}
		var expected repository.MigrationPlan
		if err := json.Unmarshal(data, &expected); err != nil {
			log.Fatalf("Failed to decode migration execution plan: %v", err)
		}
		cfg, err := config.LoadForBootstrap()
		if err != nil {
			log.Fatalf("Failed to load migration config: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := repository.ApplyEmbeddedMigrationPlan(ctx, cfg, expected); err != nil {
			log.Fatalf("Migration plan execution failed: %v", err)
		}
		log.Println("Verified migration plan completed")
		return
	}
	if *migrateOnly {
		cfg, err := config.LoadForBootstrap()
		if err != nil {
			log.Fatalf("Failed to load migration config: %v", err)
		}
		if err := repository.RunMigrations(cfg); err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		log.Println("Database migrations completed")
		return
	}

	// CLI setup mode
	if *setupMode {
		if err := setup.RunCLI(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	// Check if setup is needed
	if setup.NeedsSetup() {
		// Check if auto-setup is enabled (for Docker deployment)
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				log.Fatalf("Auto setup failed: %v", err)
			}
			// Continue to main server after auto-setup
		} else {
			log.Println("First run detected, starting setup wizard...")
			runSetupServer()
			return
		}
	}

	// Normal server mode
	runMainServer()
}

func runSetupServer() {
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS(config.CORSConfig{}))
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil))

	// Register setup routes
	setup.RegisterRoutes(r)

	// Serve embedded frontend if available
	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// Get server address from config.yaml or environment variables (SERVER_HOST, SERVER_PORT)
	// This allows users to run setup on a different address if needed
	addr := config.GetServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure Sub2API")

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         protocols,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start setup server: %v", err)
	}
}

func runMainServer() {
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if err := logger.Init(logger.OptionsFromConfig(cfg.Log)); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	if cfg.RunMode == config.RunModeSimple {
		log.Println("⚠️  WARNING: Running in SIMPLE mode - billing and quota checks are DISABLED")
	}

	buildInfo := handler.BuildInfo{
		Version:   Version,
		BuildType: BuildType,
	}

	app, err := initializeApplication(buildInfo)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	service.CloseReleaseActivationRegistration()
	defer app.Cleanup()
	if app.PromptAudit != nil {
		if err := app.PromptAudit.Start(context.Background()); err != nil {
			// Startup continues so unrelated APIs stay up. Fail-closed (unavailable)
			// applies only when a persisted blocking policy was observed; without
			// blocking intent, Prompt Audit stays ModeOff so the gateway remains
			// usable and administrators can still disable the feature (#4560).
			log.Printf("Prompt Audit started in degraded state: %v", err)
		}
	}

	// 启动服务器
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on %s", app.Server.Addr)

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
