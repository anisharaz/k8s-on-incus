package handlers

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/middleware"
	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	incusapi "github.com/lxc/incus/v7/shared/api"
	"gorm.io/gorm"
)

// NetworkHandlers handles cluster network endpoints.
type NetworkHandlers struct {
	db    *gorm.DB
	incus *incus.Client
}

// NewNetworkHandlers creates a new cluster network handler.
func NewNetworkHandlers(db *gorm.DB, incusClient *incus.Client) *NetworkHandlers {
	return &NetworkHandlers{db: db, incus: incusClient}
}

// CreateNetwork validates the requested name/CIDR, checks it against every
// network Incus currently knows about (not just ones this app created),
// creates the Incus bridge network, and persists the result. The owner is
// the authenticated session's user.
func (h *NetworkHandlers) CreateNetwork(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var req models.CreateClusterNetworkRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "invalid request body",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 63 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: "name must be between 1 and 63 characters",
			Code:    fiber.StatusBadRequest,
		})
	}

	// CIDR is optional: if the caller doesn't supply one, Incus picks an
	// unused private subnet itself ("ipv4.address": "auto") — we only do
	// our own parsing/conflict-checking when a specific CIDR is requested.
	req.CIDR = strings.TrimSpace(req.CIDR)
	autoCIDR := req.CIDR == ""

	var network *net.IPNet
	var gateway net.IP

	if !autoCIDR {
		var err error
		network, err = parseClusterCIDR(req.CIDR)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
				Error:   "validation error",
				Message: err.Error(),
				Code:    fiber.StatusBadRequest,
			})
		}
		gateway = gatewayForNetwork(network)

		existing, err := h.incus.ListNetworks(c.Context())
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
				Error:   "incus error",
				Message: err.Error(),
				Code:    fiber.StatusInternalServerError,
			})
		}

		if conflictName, conflictCIDR, found := findConflictingNetwork(existing, network); found {
			return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
				Error:   "cidr conflict",
				Message: fmt.Sprintf("requested CIDR %s overlaps with existing Incus network %q (%s)", req.CIDR, conflictName, conflictCIDR),
				Code:    fiber.StatusConflict,
			})
		}
	}

	// Fast pre-check for a friendlier duplicate-name error than the DB's own.
	// Name only needs to be unique within the owner's own networks.
	var count int64
	h.db.Model(&models.ClusterNetwork{}).Where("owner_id = ? AND name = ?", ownerID, req.Name).Count(&count)
	if count > 0 {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "network already exists",
			Message: "you already have a cluster network with this name",
			Code:    fiber.StatusConflict,
		})
	}

	incusConfig := map[string]string{
		"ipv4.nat":     "true",
		"ipv6.address": "none",
	}
	if autoCIDR {
		incusConfig["ipv4.address"] = "auto"
	} else {
		ones, _ := network.Mask.Size()
		incusConfig["ipv4.address"] = fmt.Sprintf("%s/%d", gateway, ones)
	}

	id := uuid.New().String()
	incusName := generateIncusNetworkName(id)

	if err := h.incus.CreateNetwork(c.Context(), incusName, incusConfig); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "incus error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if autoCIDR {
		created, err := h.incus.GetNetwork(c.Context(), incusName)
		if err != nil {
			_ = h.incus.DeleteNetwork(c.Context(), incusName)
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
				Error:   "incus error",
				Message: "failed to read auto-assigned network config: " + err.Error(),
				Code:    fiber.StatusInternalServerError,
			})
		}

		ip, ipnet, err := net.ParseCIDR(created.Config["ipv4.address"])
		if err != nil {
			_ = h.incus.DeleteNetwork(c.Context(), incusName)
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
				Error:   "incus error",
				Message: "incus returned an unparseable auto-assigned address: " + err.Error(),
				Code:    fiber.StatusInternalServerError,
			})
		}
		network = ipnet
		gateway = ip
	}

	clusterNetwork := models.ClusterNetwork{
		ID:        id,
		OwnerID:   ownerID,
		Name:      req.Name,
		IncusName: incusName,
		CIDR:      network.String(),
		Gateway:   gateway.String(),
		Status:    string(models.ClusterNetworkStatusReady),
		Message:   "Network created",
	}

	if err := h.db.Create(&clusterNetwork).Error; err != nil {
		// Roll back the Incus-side network so the name isn't stuck
		// existing-in-Incus-but-unknown-to-us after a DB failure.
		_ = h.incus.DeleteNetwork(c.Context(), incusName)
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.Status(fiber.StatusCreated).JSON(models.ClusterNetworkResponse{Network: clusterNetwork})
}

// ListNetworks returns the authenticated user's cluster networks.
func (h *NetworkHandlers) ListNetworks(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var networks []models.ClusterNetwork
	if err := h.db.Where("owner_id = ?", ownerID).Order("created_at DESC").Find(&networks).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.ClusterNetworkListResponse{Networks: networks})
}

// GetNetwork returns a single cluster network by ID, scoped to the
// authenticated user — one owned by someone else looks like it doesn't exist.
func (h *NetworkHandlers) GetNetwork(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var network models.ClusterNetwork
	if err := h.db.Where("id = ? AND owner_id = ?", c.Params("id"), ownerID).First(&network).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "cluster network not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.ClusterNetworkResponse{Network: network})
}

// DeleteNetwork deletes a cluster network from both Incus and the
// database, scoped to the authenticated user.
func (h *NetworkHandlers) DeleteNetwork(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var network models.ClusterNetwork
	if err := h.db.Where("id = ? AND owner_id = ?", c.Params("id"), ownerID).First(&network).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "cluster network not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	// Incus refuses to delete a network still in use by an instance, so any
	// error here (e.g. "network in use") is returned as-is.
	if err := h.incus.DeleteNetwork(c.Context(), network.IncusName); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "incus error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if err := h.db.Delete(&network).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// parseClusterCIDR validates that cidr is an IPv4 network address (no host
// bits set) with a prefix length that leaves room for a gateway plus hosts.
func parseClusterCIDR(cidr string) (*net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("cidr: invalid CIDR notation: %w", err)
	}

	if ip.To4() == nil {
		return nil, errors.New("cidr: only IPv4 CIDR ranges are supported")
	}

	if !ip.Equal(ipnet.IP) {
		return nil, fmt.Errorf("cidr: %s has host bits set, did you mean %s", cidr, ipnet.String())
	}

	ones, _ := ipnet.Mask.Size()
	if ones < 8 || ones > 29 {
		return nil, fmt.Errorf("cidr: prefix length /%d is out of range, must be between /8 and /29", ones)
	}

	return ipnet, nil
}

// generateIncusNetworkName derives a globally-unique, Incus-safe bridge
// interface name (<=15 chars, satisfying validate.IsInterfaceName) from a
// resource ID, so the user-facing display name is free of Incus's
// restrictive interface naming rules and per-owner uniqueness scope.
func generateIncusNetworkName(id string) string {
	const prefix = "cn"
	compact := strings.ReplaceAll(id, "-", "")
	maxSuffixLen := 15 - len(prefix)
	if len(compact) > maxSuffixLen {
		compact = compact[:maxSuffixLen]
	}
	return prefix + compact
}

// gatewayForNetwork returns the first usable address in the network (the
// network address + 1), which is used as the bridge's own gateway IP.
func gatewayForNetwork(n *net.IPNet) net.IP {
	val := binary.BigEndian.Uint32(n.IP.To4())
	val++

	gw := make(net.IP, 4)
	binary.BigEndian.PutUint32(gw, val)
	return gw
}

// findConflictingNetwork checks requested against every managed bridge
// network Incus currently knows about (regardless of who created it).
func findConflictingNetwork(networks []incusapi.Network, requested *net.IPNet) (name, cidr string, found bool) {
	for _, n := range networks {
		if n.Type != "bridge" {
			continue
		}

		addr := n.Config["ipv4.address"]
		if addr == "" || addr == "none" {
			continue
		}

		_, existing, err := net.ParseCIDR(addr)
		if err != nil {
			continue
		}

		if requested.Contains(existing.IP) || existing.Contains(requested.IP) {
			return n.Name, existing.String(), true
		}
	}

	return "", "", false
}
