package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/anisharaz/incus-k8s-manager/be/internal/auth"
	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/middleware"
	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const minPasswordLength = 8

// errAdminAlreadyExists distinguishes "already bootstrapped" from a real
// DB error inside RegisterAdmin's transaction.
var errAdminAlreadyExists = errors.New("admin account already exists")

// AuthHandlers handles bootstrap, login, logout, and "who am I" endpoints.
type AuthHandlers struct {
	db           *gorm.DB
	jwtSecret    []byte
	cookieSecure bool
	incus        *incus.Client
}

// NewAuthHandlers creates a new auth handler. cookieSecure should be true
// whenever the app is served over HTTPS (sets the cookie's Secure attribute).
func NewAuthHandlers(db *gorm.DB, jwtSecret []byte, cookieSecure bool, incusClient *incus.Client) *AuthHandlers {
	return &AuthHandlers{db: db, jwtSecret: jwtSecret, cookieSecure: cookieSecure, incus: incusClient}
}

// BootstrapStatus reports whether the admin account has been created yet,
// so the frontend knows whether to show a "register admin" or login screen.
func (h *AuthHandlers) BootstrapStatus(c fiber.Ctx) error {
	var state models.AppState
	if err := h.db.First(&state, 1).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.BootstrapStatusResponse{AdminCreated: state.AdminCreated})
}

// RegisterAdmin creates the app's one admin account and logs it straight
// in. Only works once — a row lock on the bootstrap state row means two
// concurrent first-boot requests can't both succeed.
func (h *AuthHandlers) RegisterAdmin(c fiber.Ctx) error {
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
		Role:         string(models.UserRoleAdmin),
	}

	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		var state models.AppState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, 1).Error; err != nil {
			return err
		}

		if state.AdminCreated {
			return errAdminAlreadyExists
		}

		if err := tx.Create(&user).Error; err != nil {
			return err
		}

		return tx.Model(&models.AppState{}).Where("id = 1").Update("admin_created", true).Error
	})

	if txErr != nil {
		if errors.Is(txErr, errAdminAlreadyExists) {
			return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
				Error:   "already bootstrapped",
				Message: "an admin account already exists",
				Code:    fiber.StatusConflict,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: txErr.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	// Best-effort, same as UserHandlers.CreateUser: a missing default
	// network is inconvenient but not worth failing bootstrap over.
	if _, err := createDefaultNetwork(c.Context(), h.db, h.incus, user.ID); err != nil {
		log.Printf("failed to create default network for user %s: %v", user.ID, err)
	}

	token, err := auth.GenerateToken(h.jwtSecret, user)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "internal error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}
	h.setAuthCookie(c, token)

	return c.Status(fiber.StatusCreated).JSON(models.UserResponse{User: user})
}

// Login authenticates a user by username/password and sets the session cookie.
func (h *AuthHandlers) Login(c fiber.Ctx) error {
	var req models.LoginRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "invalid request body",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	req.Username = strings.TrimSpace(req.Username)

	var user models.User
	if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
				Error:   "unauthorized",
				Message: "invalid username or password",
				Code:    fiber.StatusUnauthorized,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	// Deliberately identical error/status for "no such user" and "wrong
	// password" — don't leak which one was wrong.
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error:   "unauthorized",
			Message: "invalid username or password",
			Code:    fiber.StatusUnauthorized,
		})
	}

	token, err := auth.GenerateToken(h.jwtSecret, user)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "internal error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}
	h.setAuthCookie(c, token)

	return c.JSON(models.UserResponse{User: user})
}

// Logout clears the session cookie. Deliberately not using fiber's
// ClearCookie: per fasthttp's own docs, it doesn't work for a cookie set
// with an explicit Path (ours is "/"), since the expiry it sends targets a
// different Path and so never overrides the original — the browser keeps
// the real cookie. Expiring one with matching attributes instead.
func (h *AuthHandlers) Logout(c fiber.Ctx) error {
	c.Cookie(&fiber.Cookie{
		Name:     middleware.AuthCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HTTPOnly: true,
		Secure:   h.cookieSecure,
		SameSite: "Lax",
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// Me returns the currently authenticated user. Since the session cookie is
// HttpOnly, this is how the frontend finds out who (if anyone) is logged
// in after a page load. Must run behind middleware.RequireAuth.
func (h *AuthHandlers) Me(c fiber.Ctx) error {
	claims := middleware.ClaimsFromContext(c)

	var user models.User
	if err := h.db.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
				Error:   "unauthorized",
				Message: "user no longer exists",
				Code:    fiber.StatusUnauthorized,
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

func (h *AuthHandlers) setAuthCookie(c fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name:     middleware.AuthCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.TTL.Seconds()),
		HTTPOnly: true,
		Secure:   h.cookieSecure,
		SameSite: "Lax",
	})
}
