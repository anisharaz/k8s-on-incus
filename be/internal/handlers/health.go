package handlers

import (
	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/gofiber/fiber/v3"
)

// HealthHandler handles the health check endpoint
func HealthHandler(c fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":  "ok",
		"message": "Server is running",
	})
}

// StatusHandlers handles the status endpoint.
type StatusHandlers struct {
	incus *incus.Client
}

// NewStatusHandlers creates a new status handler.
func NewStatusHandlers(incusClient *incus.Client) *StatusHandlers {
	return &StatusHandlers{incus: incusClient}
}

// Status reports whether the Incus daemon is reachable over the shared
// socket, via a live SDK call — not the incus CLI binary, which isn't
// installed in the app's container image.
func (h *StatusHandlers) Status(c fiber.Ctx) error {
	incusStatus := "running"
	if _, err := h.incus.List(c.Context()); err != nil {
		incusStatus = "unreachable"
	}

	return c.JSON(models.StatusResponse{
		Status: map[string]string{
			"incus": incusStatus,
		},
	})
}
