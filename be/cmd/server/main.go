package main

import (
	"log"

	"github.com/anisharaz/incus-k8s-manager/be/db/migrations"
	"github.com/anisharaz/incus-k8s-manager/be/internal/config"
	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/jobs"
	"github.com/anisharaz/incus-k8s-manager/be/internal/routes"
	"github.com/gofiber/fiber/v3"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	// Load configuration
	cfg := config.NewConfig()

	if err := runMigrations(cfg.GetDatabaseDSN()); err != nil {
		log.Fatalf("Failed to run database migrations: %v\n", err)
	}

	// Initialize database
	db, err := initDatabase(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v\n", err)
	}

	incusClient, err := incus.New(cfg.IncusSocketPath)
	if err != nil {
		log.Fatalf("Failed to connect to incus: %v\n", err)
	}

	// Create Fiber app
	app := fiber.New(fiber.Config{
		AppName: "KOI API",
	})

	jobManager := jobs.NewManager(db, incusClient)

	// Any job still "queued"/"running" belonged to a goroutine that died
	// with the previous process (crash/deploy/restart) and will never
	// update its row again — fail it (and the node/cluster it was
	// operating on) now, before accepting new requests, so nothing is left
	// permanently stuck.
	jobManager.Reconcile()

	// Setup all routes
	routes.SetupRoutes(app, jobManager, db, incusClient, cfg)

	// Start server
	log.Printf("Starting server on :%s\n", cfg.Port)
	if err := app.Listen(":" + cfg.Port); err != nil {
		log.Fatalf("Failed to start server: %v\n", err)
	}
}

// initDatabase opens the PostgreSQL connection GORM uses at runtime. Schema
// migrations are applied separately by runMigrations before this is called.
func initDatabase(cfg *config.Config) (*gorm.DB, error) {
	dsn := cfg.GetDatabaseDSN()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	log.Println("Connected to database successfully")

	return db, nil
}

// runMigrations applies any pending schema migrations (db/migrations,
// embedded into the binary via migrations.FS) on every startup, so
// deploying the app is a single step — no separate `migrate` CLI run
// required. Safe to call repeatedly: migrate.ErrNoChange just means the
// schema was already up to date.
func runMigrations(dsn string) error {
	sourceDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", sourceDriver, dsn)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	log.Println("Database migrations applied")
	return nil
}
