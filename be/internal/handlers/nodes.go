package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"runtime/debug"

	"github.com/anisharaz/incus-k8s-manager/be/internal/incus"
	"github.com/anisharaz/incus-k8s-manager/be/internal/jobs"
	"github.com/anisharaz/incus-k8s-manager/be/internal/middleware"
	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	contribws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NodeHandlers handles node endpoints.
type NodeHandlers struct {
	db      *gorm.DB
	manager *jobs.Manager
	incus   *incus.Client
}

// NewNodeHandlers creates a new node handler.
func NewNodeHandlers(db *gorm.DB, manager *jobs.Manager, incusClient *incus.Client) *NodeHandlers {
	return &NodeHandlers{db: db, manager: manager, incus: incusClient}
}

// ListNodesForCluster returns all nodes belonging to a cluster (master
// first, then workers in creation order), so callers can poll VM status
// (and, via jobId, the underlying job's progress) while a node is created.
// The cluster must belong to the authenticated user.
func (h *NodeHandlers) ListNodesForCluster(c fiber.Ctx) error {
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

	var nodes []models.Node
	if err := h.db.Where("cluster_id = ?", clusterID).Order("created_at ASC").Find(&nodes).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	return c.JSON(models.NodeListResponse{Nodes: nodes})
}

// CreateNode adds a worker node to a cluster: it launches a VM on the
// cluster's network, fetches a fresh join token from the cluster's master
// (kubeadm token create --print-join-command — not the one-time token
// kubeadm init printed, which may be long expired), and runs kubeadm join
// on the new VM. The request body is optional; cpu/memory/disk each
// default if omitted, and are validated the same way as cluster creation.
// The cluster must belong to the authenticated user.
func (h *NodeHandlers) CreateNode(c fiber.Ctx) error {
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

	if cluster.Status != string(models.ClusterStatusReady) {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "cluster not ready",
			Message: "cluster must be ready before adding workers",
			Code:    fiber.StatusConflict,
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
			Message: "master node must be running before adding workers",
			Code:    fiber.StatusConflict,
		})
	}

	var network models.ClusterNetwork
	if err := h.db.Where("id = ?", cluster.NetworkID).First(&network).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	var req models.CreateNodeRequest
	if body := c.Body(); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
				Error:   "invalid request body",
				Message: err.Error(),
				Code:    fiber.StatusBadRequest,
			})
		}
	}

	size, err := validateNodeSize(req.CPU, req.Memory, req.Disk)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "validation error",
			Message: err.Error(),
			Code:    fiber.StatusBadRequest,
		})
	}

	var workerCount int64
	h.db.Model(&models.Node{}).Where("cluster_id = ? AND role = ?", clusterID, string(models.NodeRoleWorker)).Count(&workerCount)

	nodeID := uuid.New().String()
	node := models.Node{
		ID:        nodeID,
		ClusterID: clusterID,
		Name:      fmt.Sprintf("worker-%d", workerCount+1),
		IncusName: generateIncusNodeName(string(models.NodeRoleWorker), nodeID),
		Role:      string(models.NodeRoleWorker),
		Status:    string(models.NodeStatusCreating),
		Message:   "Node creation started",
	}

	if err := h.db.Create(&node).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	job, err := h.manager.CreateNodeJob(ownerID, node.ID, node.IncusName, network.IncusName, node.Role, master.IncusName, "", size)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "job creation error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}
	h.db.Model(&node).Update("job_id", job.ID)

	return c.Status(fiber.StatusAccepted).JSON(models.NodeResponse{Node: node})
}

// DeleteNode deletes a single worker node: drains it, removes its Node API
// object, resets kubeadm on it, then destroys its VM. The cluster keeps
// running throughout. Deleting the master isn't supported here — delete
// the whole cluster instead (see ClusterHandlers.DeleteCluster). The
// cluster must belong to the authenticated user.
func (h *NodeHandlers) DeleteNode(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID
	clusterID := c.Params("id")
	nodeID := c.Params("nodeId")

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

	var node models.Node
	if err := h.db.Where("id = ? AND cluster_id = ?", nodeID, clusterID).First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "node not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if node.Role == string(models.NodeRoleMaster) {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error:   "cannot delete master",
			Message: "deleting the master node isn't supported; delete the cluster instead",
			Code:    fiber.StatusBadRequest,
		})
	}

	if node.Status == string(models.NodeStatusCreating) || node.Status == string(models.NodeStatusDeleting) {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "operation in progress",
			Message: "node has an operation already in progress",
			Code:    fiber.StatusConflict,
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
			Message: "master node must be running to drain a worker before deleting it",
			Code:    fiber.StatusConflict,
		})
	}

	h.db.Model(&node).Updates(map[string]any{
		"status":  string(models.NodeStatusDeleting),
		"message": "Node deletion started",
	})

	job, err := h.manager.DeleteNodeJob(ownerID, node.ID, node.IncusName, master.IncusName)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "job creation error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}
	h.db.Model(&node).Update("job_id", job.ID)

	node.Status = string(models.NodeStatusDeleting)
	node.JobID = &job.ID
	return c.Status(fiber.StatusAccepted).JSON(models.NodeResponse{Node: node})
}

// CheckTerminalAccess runs as a normal handler before the websocket
// upgrade in the Terminal route, since auth/ownership/status checks need a
// full fiber.Ctx (and a proper HTTP error response) rather than a
// half-upgraded connection. On success it stashes the node's Incus name in
// c.Locals for Terminal to read via the upgraded connection.
func (h *NodeHandlers) CheckTerminalAccess(c fiber.Ctx) error {
	ownerID := middleware.ClaimsFromContext(c).UserID
	clusterID := c.Params("id")
	nodeID := c.Params("nodeId")

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

	var node models.Node
	if err := h.db.Where("id = ? AND cluster_id = ?", nodeID, clusterID).First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
				Error:   "not found",
				Message: "node not found",
				Code:    fiber.StatusNotFound,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error:   "database error",
			Message: err.Error(),
			Code:    fiber.StatusInternalServerError,
		})
	}

	if node.Status != string(models.NodeStatusRunning) {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error:   "node not running",
			Message: "node must be running to open a terminal",
			Code:    fiber.StatusConflict,
		})
	}

	c.Locals("incusName", node.IncusName)
	return c.Next()
}

// terminalControlMessage is the only text-frame message the browser sends —
// everything else it sends is a binary frame of raw keystrokes.
type terminalControlMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// writerFunc adapts a plain function to io.Writer, the same func-as-
// interface idiom as http.HandlerFunc.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Terminal bridges a browser websocket to an interactive bash session
// inside the node's VM (see incus.Client.ExecInteractive). Binary frames
// carry raw PTY bytes in both directions; text frames from the browser
// carry a {"type":"resize","cols":N,"rows":N} control envelope. Runs until
// the browser closes the socket (dialog close/navigation) — there's no
// server-side idle timeout, same lifetime model as an SSH session.
func (h *NodeHandlers) Terminal(conn *contribws.Conn) {
	incusName, _ := conn.Locals("incusName").(string)
	if incusName == "" {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()

	resize := make(chan [2]int, 1)
	defer close(resize)

	stdout := writerFunc(func(p []byte) (int, error) {
		if err := conn.WriteMessage(contribws.BinaryMessage, p); err != nil {
			return 0, err
		}
		return len(p), nil
	})

	done := make(chan error, 1)
	go func() {
		// A panic here (e.g. deep in the Incus SDK) would otherwise crash
		// the whole process — recover it into a normal error on `done` so
		// only this one terminal session dies.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("terminal exec goroutine panicked: %v\n%s", r, debug.Stack())
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- h.incus.ExecInteractive(ctx, incusName, stdinR, stdout, resize)
	}()

readLoop:
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break readLoop
		}

		switch msgType {
		case contribws.BinaryMessage:
			if _, err := stdinW.Write(data); err != nil {
				break readLoop
			}
		case contribws.TextMessage:
			var msg terminalControlMessage
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				select {
				case resize <- [2]int{msg.Cols, msg.Rows}:
				default:
				}
			}
		}
	}

	cancel()
	_ = stdinW.Close()
	<-done
}
