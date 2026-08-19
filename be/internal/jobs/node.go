package jobs

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/google/uuid"
)

// nodeProvisionTimeout bounds how long a single node's VM launch and (for a
// master) kubeadm init are allowed to take. Generous because kubeadm may
// need to pull control-plane images on first run, and a master additionally
// downloads the Cilium CLI and pulls Cilium's own images during CNI install.
const nodeProvisionTimeout = 20 * time.Minute

// nodeDeleteVMTimeout bounds the best-effort VM cleanup failNodeJob performs
// after a provisioning failure — just a VM stop+delete, not a full
// drain/reset/teardown sequence, so it needs far less time than
// nodeDeleteTimeout (delete.go).
const nodeDeleteVMTimeout = 2 * time.Minute

// nodeImageAlias is the only VM image currently available: a prebaked
// Ubuntu + Kubernetes image (see meta/incusDocker).
const nodeImageAlias = "k8s"

// rootKubeDir and rootKubeconfigPath are where the master's admin
// kubeconfig is copied so `kubectl` works for the root user.
const (
	rootKubeDir        = "/root/.kube"
	rootKubeconfigPath = rootKubeDir + "/config"
)

// NodeSize specifies a node's VM allocation, in Incus's config value format
// (e.g. CPU: "2", Memory: "2GiB", Disk: "20GiB"). Callers are expected to
// have already validated these against a sane minimum (see
// handlers.validateNodeSize) — this package just passes them through to
// Incus as-is.
type NodeSize struct {
	CPU    string
	Memory string
	Disk   string
}

// CreateNodeJob creates a node provisioning job and runs it in the
// background: it launches the node's VM (sized per `size`) on the given
// network, waits for it to get an IP and come up, and updates the node row
// throughout. For a master, it also runs `kubeadm init`, copies the admin
// kubeconfig, waits for the API server to report healthy, and installs cni
// (see jobs.cniInstallers). For a worker, masterIncusName identifies the
// cluster's master, from which a fresh join command is fetched (`kubeadm
// token create --print-join-command`) and run on the new node;
// masterIncusName is ignored for a master, and cni is ignored for a worker
// (CNI is a cluster-wide, master-only concern, never re-run on join).
// ownerID is the resource owner (the cluster's owner) — stashed on the job
// row so job visibility can be scoped per-user; it plays no role in
// provisioning itself.
func (m *Manager) CreateNodeJob(ownerID, nodeID, incusName, networkIncusName, role, masterIncusName, cni string, size NodeSize) (*models.Job, error) {
	now := time.Now().UTC()
	job := &models.Job{
		ID:        uuid.NewString(),
		OwnerID:   ownerID,
		Type:      "node_provision",
		Name:      fmt.Sprintf("Provision %s node %s", role, incusName),
		Status:    models.JobStatusQueued,
		Progress:  0,
		Stage:     "queued",
		Message:   "Node provisioning job accepted",
		Metadata:  map[string]string{"nodeId": nodeID, "role": role},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	go func() {
		defer recoverJobPanic(func(err error) { m.failNodeJob(job.ID, nodeID, incusName, err) })
		m.runNodeJob(job.ID, nodeID, incusName, networkIncusName, role, masterIncusName, cni, size)
	}()

	return job, nil
}

// runNodeJob launches the node's VM, waits for it to get an IPv4 address,
// for the guest agent to come up, and for containerd to be ready, then
// bootstraps it per its role (kubeadm init for a master, fetch-join-and-run
// for a worker) before marking the node running. If the node is a
// cluster's master, it also updates the cluster's status.
func (m *Manager) runNodeJob(jobID, nodeID, incusName, networkIncusName, role, masterIncusName, cni string, size NodeSize) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeProvisionTimeout)
	defer cancel()

	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusRunning
		job.Stage = "launching"
		job.Progress = 10
		job.Message = "Launching VM..."
	})
	m.updateNode(nodeID, map[string]any{
		"status":  string(models.NodeStatusCreating),
		"message": "Launching VM",
	})

	if err := m.incus.Launch(ctx, incusName, nodeImageAlias, networkIncusName, size.CPU, size.Memory, size.Disk, true); err != nil {
		m.failNodeJob(jobID, nodeID, incusName, err)
		return
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "waiting-for-ip"
		job.Progress = 40
		job.Message = "Waiting for VM IP address..."
	})

	ip, err := m.incus.WaitForIPv4(ctx, incusName)
	if err != nil {
		m.failNodeJob(jobID, nodeID, incusName, err)
		return
	}
	m.updateNode(nodeID, map[string]any{"ip": ip})

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "waiting-for-agent"
		job.Progress = 70
		job.Message = "Waiting for guest agent..."
	})

	if err := m.incus.WaitForAgent(ctx, incusName); err != nil {
		m.failNodeJob(jobID, nodeID, incusName, err)
		return
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "waiting-for-containerd"
		job.Progress = 75
		job.Message = "Waiting for container runtime..."
	})

	// The guest agent can respond before containerd has finished starting
	// (both come up during boot), and kubeadm requires a working CRI
	// socket for both init and join, so wait for it explicitly.
	if err := m.waitForContainerd(ctx, incusName); err != nil {
		m.failNodeJob(jobID, nodeID, incusName, err)
		return
	}

	var finalMessage string

	switch role {
	case string(models.NodeRoleMaster):
		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "bootstrapping"
			job.Progress = 80
			job.Message = "Running kubeadm init..."
		})
		m.updateNode(nodeID, map[string]any{"message": "Running kubeadm init"})

		if _, err := m.incus.Run(ctx, incusName, []string{"kubeadm", "init"}); err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "configuring-kubeconfig"
			job.Progress = 90
			job.Message = "Copying admin.conf for the root user..."
		})

		copyKubeconfig := []string{"bash", "-c", fmt.Sprintf(
			"mkdir -p %s && cp -f /etc/kubernetes/admin.conf %s",
			rootKubeDir, rootKubeconfigPath,
		)}
		if _, err := m.incus.Run(ctx, incusName, copyKubeconfig); err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "verifying"
			job.Progress = 95
			job.Message = "Waiting for cluster API to become healthy..."
		})

		if err := m.waitForClusterHealthy(ctx, incusName); err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "installing-cni"
			job.Progress = 97
			job.Message = fmt.Sprintf("Installing %s CNI...", cni)
		})
		m.updateNode(nodeID, map[string]any{"message": "Installing CNI"})

		if err := installCNI(ctx, m, incusName, cni); err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		finalMessage = "Kubernetes control plane is ready"

	case string(models.NodeRoleWorker):
		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "joining"
			job.Progress = 80
			job.Message = "Fetching join command from master..."
		})

		// Always request a fresh token rather than reusing the one printed
		// by kubeadm init: that one may be long expired (default TTL 24h)
		// by the time a worker is added.
		joinCmd, err := m.incus.Run(ctx, masterIncusName, []string{"kubeadm", "token", "create", "--print-join-command"})
		if err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		m.updateJob(jobID, func(job *models.Job) {
			job.Message = "Running kubeadm join..."
		})
		m.updateNode(nodeID, map[string]any{"message": "Running kubeadm join"})

		if _, err := m.incus.Run(ctx, incusName, []string{"bash", "-c", strings.TrimSpace(joinCmd.Stdout)}); err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "verifying"
			job.Progress = 95
			job.Message = "Waiting for node to register with the cluster..."
		})

		if err := m.waitForNodeRegistered(ctx, masterIncusName, incusName); err != nil {
			m.failNodeJob(jobID, nodeID, incusName, err)
			return
		}

		finalMessage = "Node joined the cluster"
	}

	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusSucceeded
		job.Progress = 100
		job.Stage = "complete"
		job.Message = finalMessage
		job.Result = map[string]any{"ip": ip}
		job.CompletedAt = &completedAt
	})

	m.updateNode(nodeID, map[string]any{
		"status":  string(models.NodeStatusRunning),
		"message": finalMessage,
	})

	m.setClusterStatusIfMaster(nodeID, models.ClusterStatusReady, finalMessage)
}

// waitForContainerd polls for the containerd CRI socket, which kubeadm init
// requires, since it can still be starting when the guest agent responds.
func (m *Manager) waitForContainerd(ctx context.Context, incusName string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	checkCmd := []string{"test", "-S", "/var/run/containerd/containerd.sock"}

	for {
		result, err := m.incus.Exec(ctx, incusName, checkCmd, nil)
		if err == nil && result.ExitCode == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("containerd did not become ready on instance %q: %w", incusName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitForClusterHealthy polls the API server's /healthz endpoint (via the
// root kubeconfig copied by runNodeJob) until it reports "ok".
func (m *Manager) waitForClusterHealthy(ctx context.Context, incusName string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	healthCmd := []string{"kubectl", "--kubeconfig=" + rootKubeconfigPath, "get", "--raw=/healthz"}

	for {
		result, err := m.incus.Exec(ctx, incusName, healthCmd, nil)
		if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "ok" {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster API on instance %q did not become healthy: %w", incusName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitForNodeRegistered polls the master (via its root kubeconfig) until
// `kubectl get node <nodeIncusName>` succeeds, confirming the worker
// actually registered with the API server after kubeadm join. It doesn't
// wait for Ready: without a CNI installed, nodes never reach Ready.
func (m *Manager) waitForNodeRegistered(ctx context.Context, masterIncusName, nodeIncusName string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		if m.isNodeRegistered(ctx, masterIncusName, nodeIncusName) {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("node %q did not register with the cluster: %w", nodeIncusName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// failNodeJob marks the job and node as failed, and fails the cluster too
// if this was its master node. Also best-effort deletes the VM: Launch
// happens in the very first step, so almost every later failure (IP wait,
// agent wait, kubeadm init/join, CNI install) leaves a VM running that
// nothing else will ever clean up — there's no retry path for a failed
// node, only deletion, and deletion is idempotent (a no-op if Launch itself
// never got far enough to create anything). Delete uses its own context,
// not the job's, since the job's timeout may already be exceeded by the
// time a failure is being handled.
func (m *Manager) failNodeJob(jobID, nodeID, incusName string, runErr error) {
	deleteCtx, cancel := context.WithTimeout(context.Background(), nodeDeleteVMTimeout)
	defer cancel()
	if err := m.incus.Delete(deleteCtx, incusName); err != nil {
		log.Printf("job %s: failed to clean up VM %q after provisioning failure: %v", jobID, incusName, err)
	}

	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusFailed
		job.Progress = 100
		job.Stage = "failed"
		job.Message = "Node provisioning failed"
		job.Error = runErr.Error()
		job.CompletedAt = &completedAt
	})

	m.updateNode(nodeID, map[string]any{
		"status":  string(models.NodeStatusFailed),
		"message": "Node provisioning failed: " + runErr.Error(),
	})

	m.setClusterStatusIfMaster(nodeID, models.ClusterStatusFailed, "Master node provisioning failed")
}

// updateNode applies partial updates to a node row.
func (m *Manager) updateNode(nodeID string, updates map[string]any) {
	m.db.Model(&models.Node{}).Where("id = ?", nodeID).Updates(updates)
}

// setClusterStatusIfMaster updates a cluster's status when the given node
// is its master. Worker node outcomes don't change cluster status.
func (m *Manager) setClusterStatusIfMaster(nodeID string, status models.ClusterStatus, message string) {
	var node models.Node
	if err := m.db.Where("id = ?", nodeID).First(&node).Error; err != nil {
		return
	}

	if node.Role != string(models.NodeRoleMaster) {
		return
	}

	m.db.Model(&models.Cluster{}).Where("id = ?", node.ClusterID).Updates(map[string]any{
		"status":  string(status),
		"message": message,
	})
}
