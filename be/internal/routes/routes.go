package routes

import (
	"io/fs"

	"github.com/anisharaz/incus-k8s-manager/be/internal/config"
	"github.com/anisharaz/incus-k8s-manager/be/internal/handlers"
	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/jobs"
	"github.com/anisharaz/incus-k8s-manager/be/internal/middleware"
	"github.com/anisharaz/incus-k8s-manager/be/internal/webui"
	contribws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"
	"gorm.io/gorm"
)

// SetupRoutes initializes all application routes
func SetupRoutes(app *fiber.App, jobManager *jobs.Manager, db *gorm.DB, incusClient *incus.Client, cfg *config.Config) {
	taskHandlers := handlers.NewTaskHandlers(jobManager)
	statusHandlers := handlers.NewStatusHandlers(incusClient)
	networkHandlers := handlers.NewNetworkHandlers(db, incusClient)
	userHandlers := handlers.NewUserHandlers(db, jobManager, incusClient)
	clusterHandlers := handlers.NewClusterHandlers(db, jobManager, incusClient)
	nodeHandlers := handlers.NewNodeHandlers(db, jobManager, incusClient)
	authHandlers := handlers.NewAuthHandlers(db, cfg.JWTSecret, cfg.CookieSecure, incusClient)
	requireAuth := middleware.RequireAuth(cfg.JWTSecret)

	// Apply global middleware
	app.Use(middleware.LoggerMiddleware())
	app.Use(middleware.CORSMiddleware())

	// Health check route
	app.Get("/health", handlers.HealthHandler)

	// API v1 routes
	v1 := app.Group("/api/v1")

	// Status routes
	v1.Get("/status", statusHandlers.Status)
	v1.Get("/jobs", requireAuth, taskHandlers.ListJobs)
	v1.Get("/jobs/:id", requireAuth, taskHandlers.GetJob)

	// Auth routes. BootstrapStatus/RegisterAdmin/Login are necessarily
	// public (there's no session before you have one); Me/Logout require
	// an existing session.
	v1.Get("/auth/status", authHandlers.BootstrapStatus)
	v1.Post("/auth/register-admin", authHandlers.RegisterAdmin)
	v1.Post("/auth/login", authHandlers.Login)
	v1.Post("/auth/logout", authHandlers.Logout)
	v1.Get("/auth/me", requireAuth, authHandlers.Me)

	// User routes — admin-only (regular users are created by the admin,
	// not self-registered; see AuthHandlers.RegisterAdmin for the one
	// exception, the bootstrap admin itself).
	v1.Post("/users", requireAuth, middleware.RequireAdmin, userHandlers.CreateUser)
	v1.Get("/users", requireAuth, middleware.RequireAdmin, userHandlers.ListUsers)
	v1.Get("/users/:id", requireAuth, middleware.RequireAdmin, userHandlers.GetUser)
	v1.Delete("/users/:id", requireAuth, middleware.RequireAdmin, userHandlers.DeleteUser)

	// Cluster network routes — every one scoped to the authenticated user.
	v1.Post("/networks", requireAuth, networkHandlers.CreateNetwork)
	v1.Get("/networks", requireAuth, networkHandlers.ListNetworks)
	v1.Get("/networks/:id", requireAuth, networkHandlers.GetNetwork)
	v1.Delete("/networks/:id", requireAuth, networkHandlers.DeleteNetwork)

	// Cluster routes — every one scoped to the authenticated user.
	v1.Post("/clusters", requireAuth, clusterHandlers.CreateCluster)
	v1.Get("/clusters", requireAuth, clusterHandlers.ListClusters)
	v1.Get("/clusters/:id", requireAuth, clusterHandlers.GetCluster)
	v1.Get("/clusters/:id/kubeconfig", requireAuth, clusterHandlers.GetKubeconfig)
	v1.Get("/clusters/:id/nodes", requireAuth, nodeHandlers.ListNodesForCluster)
	v1.Post("/clusters/:id/nodes", requireAuth, nodeHandlers.CreateNode)
	v1.Delete("/clusters/:id/nodes/:nodeId", requireAuth, nodeHandlers.DeleteNode)
	v1.Get("/clusters/:id/nodes/:nodeId/terminal", requireAuth, nodeHandlers.CheckTerminalAccess, contribws.New(nodeHandlers.Terminal))
	v1.Delete("/clusters/:id", requireAuth, clusterHandlers.DeleteCluster)

	// Root API endpoint
	v1.Get("/", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"message": "KOI API v1",
			"version": "1.0.0",
		})
	})

	// JSON 404 for anything under /api that didn't match a specific route.
	app.Use("/api", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Route not found",
			"path":  c.Path(),
		})
	})

	// Serve the built frontend for everything else. A path that isn't a
	// real static file (e.g. a client-side route like /clusters/:id) falls
	// back to index.html so the SPA's own router handles it.
	app.Use(static.New("", static.Config{
		FS: webui.FS,
		NotFoundHandler: func(c fiber.Ctx) error {
			index, err := fs.ReadFile(webui.FS, "index.html")
			if err != nil {
				return c.Status(fiber.StatusNotFound).SendString("not found")
			}
			c.Type("html")
			return c.Status(fiber.StatusOK).Send(index)
		},
	}))
}
