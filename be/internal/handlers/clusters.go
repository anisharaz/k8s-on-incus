package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/jobs"
	"github.com/anisharaz/incus-k8s-manager/be/internal/middleware"
	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/lxc/incus/v7/shared/units"
	"gorm.io/gorm"
)

// rootKubeconfigPath mirrors the same constant in internal/jobs/node.go —
// where the master's admin kubeconfig is copied so `kubectl` (and this
// handler's download) can read it as the root user.
const rootKubeconfigPath = "/root/.kube/config"

// Minimum node VM allocation. CPU/Memory match kubeadm's own hard preflight
// requirements (confirmed live: kubeadm rejects <2 CPUs or <1700MB RAM).
// Disk isn't kubeadm-enforced, but 20GiB is the commonly recommended floor
// for a control-plane node (etcd + images) and comfortably exceeds the base
// VM image's own 4GiB virtual size.
const (
	minNodeCPU    = 2
	minNodeMemory = "1700MB"
	minNodeDisk   = "20GiB"
)

// defaultNodeMemory is used when the request omits memory. It's above
// minNodeMemory, not equal to it: virtualization overhead means the guest
// can see slightly less RAM than the configured limit, and kubeadm's check
// is a hard cutoff, so sitting exactly on the minimum risks failing it.
const defaultNodeMemory = "2GiB"

// ClusterHandlers handles cluster endpoints.
type ClusterHandlers struct {
	db      *gorm.DB
	manager *jobs.Manager
	incus   *incus.Client
}

// NewClusterHandlers creates a new cluster handler.
func NewClusterHandlers(db *gorm.DB, manager *jobs.Manager, incusClient *incus.Client) *ClusterHandlers {
	return &ClusterHandlers{db: db, manager: manager, incus: incusClient}
}

// CreateCluster creates a cluster and its master node, then starts a
// background job to launch the master's VM on the chosen network. Only the
// VM is launched here — bootstrapping Kubernetes on it is a later step.
// The owner is the authenticated session's user, who must also own the
// referenced network.
func (h *ClusterHandlers) CreateCluster(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var req models.CreateClusterRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "invalid request body",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	req.NetworkID = strings.TrimSpace(req.NetworkID)
	if req.NetworkID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: "networkId is required",
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

	cni, err := validateCNI(req.CNI)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	size, err := validateNodeSize(req.CPU, req.Memory, req.Disk)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	// Scoped by owner: a network owned by someone else looks like it
	// doesn't exist, and you can't build a cluster on it either way.
	var network models.ClusterNetwork
	if err := h.db.Where("id = ? AND owner_id = ?", req.NetworkID, ownerID).First(&network).Error; err != nil {
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

	// Fast pre-check for a friendlier duplicate-name error than the DB's own.
	var count int64
	h.db.Model(&models.Cluster{}).Where("owner_id = ? AND name = ?", ownerID, req.Name).Count(&count)
	if count > 0 {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "cluster already exists",
			Message: "you already have a cluster with this name",
			Code:    fiber.StatusConflict,
		})
	}

	cluster := models.Cluster{
		ID:        uuid.New().String(),
		OwnerID:   ownerID,
		NetworkID: req.NetworkID,
		Name:      req.Name,
		CNI:       cni,
		Status:    string(models.ClusterStatusCreating),
		Message:   "Cluster creation started",
	}

	nodeID := uuid.New().String()
	node := models.Node{
		ID:        nodeID,
		ClusterID: cluster.ID,
		Name:      "master",
		IncusName: generateIncusNodeName(string(models.NodeRoleMaster), nodeID),
		Role:      string(models.NodeRoleMaster),
		Status:    string(models.NodeStatusCreating),
		Message:   "Node creation started",
	}

	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&cluster).Error; err != nil {
			return err
		}
		return tx.Create(&node).Error
	}); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	// masterIncusName is unused for the master's own provisioning job.
	job, err := h.manager.CreateNodeJob(ownerID, node.ID, node.IncusName, network.IncusName, node.Role, "", cni, size)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "job creation error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}
	h.db.Model(&node).Update("job_id", job.ID)

	return c.Status(fiber.StatusAccepted).JSON(models.ClusterResponse{Cluster: cluster})
}

// ListClusters returns the authenticated user's clusters.
func (h *ClusterHandlers) ListClusters(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var clusters []models.Cluster
	if err := h.db.Where("owner_id = ?", ownerID).Order("created_at DESC").Find(&clusters).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.ClusterListResponse{Clusters: clusters})
}

// GetCluster returns a single cluster by ID, scoped to the authenticated user.
func (h *ClusterHandlers) GetCluster(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID

	var cluster models.Cluster
	if err := h.db.Where("id = ? AND owner_id = ?", c.Params("id"), ownerID).First(&cluster).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "cluster not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.ClusterResponse{Cluster: cluster})
}

// GetKubeconfig returns the cluster's admin kubeconfig as a downloadable
// file, read live from the master's /root/.kube/config — not the usual
// JSON envelope every other endpoint uses. The cluster must belong to the
// authenticated user, and its master must be running.
func (h *ClusterHandlers) GetKubeconfig(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID
	clusterID := c.Params("id")

	var cluster models.Cluster
	if err := h.db.Where("id = ? AND owner_id = ?", clusterID, ownerID).First(&cluster).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "cluster not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	var master models.Node
	if err := h.db.Where("cluster_id = ? AND role = ?", clusterID, string(models.NodeRoleMaster)).First(&master).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: "cluster has no master node: " + err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if master.Status != string(models.NodeStatusRunning) {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "master not running",
			Message: "master node must be running to fetch its kubeconfig",
			Code:    fiber.StatusConflict,
		})
	}

	result, err := h.incus.Run(c.Context(), master.IncusName, []string{"cat", rootKubeconfigPath})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "incus error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	c.Set(fiber.HeaderContentType, "application/yaml")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s-kubeconfig.yaml"`, cluster.Name))
	return c.SendString(result.Stdout)
}

// allowedCNIs are the CNI values CreateCluster accepts. Extend this set (and
// jobs.cniInstallers) to support another CNI later.
//
// OVN-Kubernetes is installed with global.dummyGatewayBridge=true (see
// jobs.installOVNKubernetes) — pod-to-pod networking works, but external/
// NodePort access does not, since that mode deliberately avoids binding the
// VM's real NIC into an OVS bridge (the same NIC Incus's own IP detection
// and guest-agent reachability depend on). It's offered as "experimental"
// in the UI for this reason, not because it's unstable.
var allowedCNIs = map[string]bool{
	string(models.CNITypeCilium):        true,
	string(models.CNITypeCalico):        true,
	string(models.CNITypeFlannel):       true,
	string(models.CNITypeOVNKubernetes): true,
}

// validateCNI defaults an empty cni to CNITypeCilium and rejects anything
// not in allowedCNIs, listing the allowed values in the error.
func validateCNI(cni string) (string, error) {
	cni = strings.TrimSpace(cni)
	if cni == "" {
		return string(models.CNITypeCilium), nil
	}
	if !allowedCNIs[cni] {
		allowed := make([]string, 0, len(allowedCNIs))
		for k := range allowedCNIs {
			allowed = append(allowed, k)
		}
		sort.Strings(allowed)
		return "", fmt.Errorf("cni must be one of [%s], got %q", strings.Join(allowed, ", "), cni)
	}
	return cni, nil
}

// DeleteCluster deletes an entire cluster: every node's VM plus the
// cluster itself. This is the only way to remove a master — there is no
// "delete master, keep cluster" operation. Runs in the background; the
// cluster and every node are marked "deleting" immediately, and the
// Cluster row (and its nodes, via cascade) disappears only once the job
// succeeds. The cluster must belong to the authenticated user.
func (h *ClusterHandlers) DeleteCluster(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID
	clusterID := c.Params("id")

	var cluster models.Cluster
	if err := h.db.Where("id = ? AND owner_id = ?", clusterID, ownerID).First(&cluster).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "cluster not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if cluster.Status == string(models.ClusterStatusDeleting) {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "deletion in progress",
			Message: "cluster deletion is already in progress",
			Code:    fiber.StatusConflict,
		})
	}

	var nodes []models.Node
	if err := h.db.Where("cluster_id = ?", clusterID).Find(&nodes).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	var masterIncusName string
	var workerIncusNames []string
	for _, n := range nodes {
		if n.Role == string(models.NodeRoleMaster) {
			masterIncusName = n.IncusName
		} else {
			workerIncusNames = append(workerIncusNames, n.IncusName)
		}
	}
	if masterIncusName == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: "cluster has no master node",
			Code:    fiber.StatusInternalServerError,
		})
	}

	h.db.Model(&cluster).Updates(map[string]any{
		"status":  string(models.ClusterStatusDeleting),
		"message": "Cluster deletion started",
	})
	h.db.Model(&models.Node{}).Where("cluster_id = ?", clusterID).Update("status", string(models.NodeStatusDeleting))

	job, err := h.manager.DeleteClusterJob(ownerID, clusterID, masterIncusName, workerIncusNames)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "job creation error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}
	h.db.Model(&cluster).Update("job_id", job.ID)

	cluster.Status = string(models.ClusterStatusDeleting)
	cluster.JobID = &job.ID
	return c.Status(fiber.StatusAccepted).JSON(models.ClusterResponse{Cluster: cluster})
}

// validateNodeSize applies the minimum to any unset field (cpu == 0 or
// memory/disk == "") and rejects anything explicitly set below it.
func validateNodeSize(cpu int, memory, disk string) (jobs.NodeSize, error) {
	if cpu == 0 {
		cpu = minNodeCPU
	} else if cpu < minNodeCPU {
		return jobs.NodeSize{}, fmt.Errorf("cpu must be at least %d, got %d", minNodeCPU, cpu)
	}

	memory = strings.TrimSpace(memory)
	if memory == "" {
		memory = defaultNodeMemory
	} else if err := checkMinByteSize("memory", memory, minNodeMemory); err != nil {
		return jobs.NodeSize{}, err
	}

	disk = strings.TrimSpace(disk)
	if disk == "" {
		disk = minNodeDisk
	} else if err := checkMinByteSize("disk", disk, minNodeDisk); err != nil {
		return jobs.NodeSize{}, err
	}

	return jobs.NodeSize{CPU: strconv.Itoa(cpu), Memory: memory, Disk: disk}, nil
}

// checkMinByteSize parses value and min using Incus's size format (e.g.
// "2GiB") and errors if value parses below min or doesn't parse at all.
func checkMinByteSize(field, value, min string) error {
	got, err := units.ParseByteSizeString(value)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}

	minBytes, err := units.ParseByteSizeString(min)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}

	if got < minBytes {
		return fmt.Errorf("%s must be at least %s, got %s", field, min, value)
	}

	return nil
}

// generateIncusNodeName derives a globally-unique, Incus-safe VM instance
// name (<=63 chars, alphanumeric/hyphen, satisfying validate.IsHostname)
// from a role and resource ID, so the user-facing display name is free of
// Incus's naming rules and per-cluster uniqueness scope.
func generateIncusNodeName(role, id string) string {
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}
	return role + "-" + compact
}
