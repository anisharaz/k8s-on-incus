package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/jobs"
	"github.com/anisharaz/incus-k8s-manager/be/internal/middleware"
	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// UserHandlers handles user endpoints. These require an authenticated
// admin (see routes.go) — regular users are created by the admin, not
// self-registered.
type UserHandlers struct {
	db      *gorm.DB
	manager *jobs.Manager
	incus   *incus.Client
}

// NewUserHandlers creates a new user handler.
func NewUserHandlers(db *gorm.DB, manager *jobs.Manager, incusClient *incus.Client) *UserHandlers {
	return &UserHandlers{db: db, manager: manager, incus: incusClient}
}

// CreateUser creates a new regular user (role is always "user" here — the
// one admin account is created exclusively via the bootstrap flow, see
// AuthHandlers.RegisterAdmin).
func (h *UserHandlers) CreateUser(c fiber.Ctx) error {
	var req models.CreateUserRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "invalid request body",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Username) > 63 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: "username must be between 1 and 63 characters",
			Code:    fiber.StatusBadRequest,
		})
	}

	if len(req.Password) < minPasswordLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: fmt.Sprintf("password must be at least %d characters", minPasswordLength),
			Code:    fiber.StatusBadRequest,
		})
	}

	var count int64
	h.db.Model(&models.User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "user already exists",
			Message: "a user with this username already exists",
			Code:    fiber.StatusConflict,
		})
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "internal error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	user := models.User{
		ID:           uuid.New().String(),
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         string(models.UserRoleUser),
	}

	if err := h.db.Create(&user).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	// Best-effort: a missing default network is inconvenient (the user has
	// to create one by hand before their first cluster) but not worth
	// failing account creation over.
	if _, err := createDefaultNetwork(c.Context(), h.db, h.incus, user.ID); err != nil {
		log.Printf("failed to create default network for user %s: %v", user.ID, err)
	}

	return c.Status(fiber.StatusCreated).JSON(models.UserResponse{User: user})
}

// ListUsers returns all users.
func (h *UserHandlers) ListUsers(c fiber.Ctx) error {
	var users []models.User
	if err := h.db.Order("created_at DESC").Find(&users).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.UserListResponse{Users: users})
}

// GetUser returns a single user by ID.
func (h *UserHandlers) GetUser(c fiber.Ctx) error {
	var user models.User
	if err := h.db.Where("id = ?", c.Params("id")).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "user not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.UserResponse{User: user})
}

// DeleteUser starts a background job that tears down every resource the
// target user owns (every cluster's VMs, every network) and then deletes
// the user row itself. Only non-admin users can be deleted this way — the
// caller is already required to be an admin (see routes.go), so this also
// means an admin can't delete themselves or another admin through this
// endpoint.
func (h *UserHandlers) DeleteUser(c fiber.Ctx) error {
	adminID := middleware.ClaimsFromContext(c).UserID

	var target models.User
	if err := h.db.Where("id = ?", c.Params("id")).First(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "user not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if target.Role == string(models.UserRoleAdmin) {
		return c.Status(fiber.StatusForbidden).JSON(models.ErrorResponse{
			Error:   "cannot delete admin",
			Message: "admin users can't be deleted through this endpoint",
			Code:    fiber.StatusForbidden,
		})
	}

	job, err := h.manager.DeleteUserJob(adminID, target.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "job creation error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.Status(fiber.StatusAccepted).JSON(models.JobResponse{Job: *job})
}
