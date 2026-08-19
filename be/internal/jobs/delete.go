package jobs

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/google/uuid"
)

// nodeDeleteTimeout bounds a worker deletion job (drain + remove from
// cluster + kubeadm reset + VM teardown).
const nodeDeleteTimeout = 10 * time.Minute

// clusterDeleteTimeout bounds a whole-cluster deletion job (every node's VM
// torn down, fail-fast).
const clusterDeleteTimeout = 20 * time.Minute

// DeleteNodeJob creates a worker deletion job and runs it in the
// background: it drains the worker and removes its Node API object (both
// via the master's root kubeconfig), runs kubeadm reset on the worker
// itself, then deletes its VM. The Node row is deleted only on success; on
// failure it's left behind with status "failed" so the caller can inspect
// the error and retry. Deleting a master isn't supported here — see
// DeleteClusterJob.
func (m *Manager) DeleteNodeJob(ownerID, nodeID, incusName, masterIncusName string) (*models.Job, error) {
	now := time.Now().UTC()
	job := &models.Job{
		ID:        uuid.NewString(),
		OwnerID:   ownerID,
		Type:      "node_deletion",
		Name:      fmt.Sprintf("Delete node %s", incusName),
		Status:    models.JobStatusQueued,
		Progress:  0,
		Stage:     "queued",
		Message:   "Node deletion job accepted",
		Metadata:  map[string]string{"nodeId": nodeID},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	go func() {
		defer recoverJobPanic(func(err error) { m.failNodeDeleteJob(job.ID, nodeID, err) })
		m.runNodeDeleteJob(job.ID, nodeID, incusName, masterIncusName)
	}()

	return job, nil
}

func (m *Manager) runNodeDeleteJob(jobID, nodeID, incusName, masterIncusName string) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeDeleteTimeout)
	defer cancel()

	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusRunning
		job.Stage = "checking-registration"
		job.Progress = 10
		job.Message = "Checking whether the node ever joined the cluster..."
	})

	// A worker whose provisioning failed before (or during) `kubeadm join`
	// never registered with the API server — draining it or removing its
	// Node object is both meaningless and guaranteed to fail ("node not
	// found"), which used to abort deletion before it ever reached VM
	// teardown below, permanently orphaning the VM (it's undeletable any
	// other way: the only path to delete a worker is this same job). Skip
	// straight to best-effort cleanup for a node that never actually
	// joined; keep the normal graceful drain-then-remove path (fatal on
	// failure, as before) for a node that's genuinely part of the cluster
	// and may be running real workloads.
	registered := m.isNodeRegistered(ctx, masterIncusName, incusName)

	if registered {
		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "draining"
			job.Progress = 15
			job.Message = "Draining node..."
		})
		m.updateNode(nodeID, map[string]any{"message": "Draining node"})

		drainCmd := []string{
			"kubectl", "--kubeconfig=" + rootKubeconfigPath, "drain", incusName,
			"--ignore-daemonsets", "--delete-emptydir-data", "--force", "--timeout=60s",
		}
		if _, err := m.incus.Run(ctx, masterIncusName, drainCmd); err != nil {
			m.failNodeDeleteJob(jobID, nodeID, err)
			return
		}

		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "removing-node-object"
			job.Progress = 45
			job.Message = "Removing node from the cluster..."
		})

		deleteNodeCmd := []string{"kubectl", "--kubeconfig=" + rootKubeconfigPath, "delete", "node", incusName}
		if _, err := m.incus.Run(ctx, masterIncusName, deleteNodeCmd); err != nil {
			m.failNodeDeleteJob(jobID, nodeID, err)
			return
		}
	} else {
		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "skipping-drain"
			job.Progress = 45
			job.Message = "Node never joined the cluster — skipping drain"
		})
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "resetting"
		job.Progress = 70
		job.Message = "Running kubeadm reset..."
	})
	m.updateNode(nodeID, map[string]any{"message": "Running kubeadm reset"})

	if _, err := m.incus.Run(ctx, incusName, []string{"kubeadm", "reset", "--force"}); err != nil {
		if registered {
			m.failNodeDeleteJob(jobID, nodeID, err)
			return
		}
		// Best-effort for a node that never joined: it may have failed
		// before the guest agent/containerd ever came up, in which case no
		// command can run on it at all — that shouldn't block deleting the
		// VM below, which is the actual point of this job.
		log.Printf("node delete job %s: kubeadm reset on unregistered node %q failed, proceeding to VM delete anyway: %v", jobID, incusName, err)
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "deleting-vm"
		job.Progress = 90
		job.Message = "Deleting VM..."
	})
	m.updateNode(nodeID, map[string]any{"message": "Deleting VM"})

	if err := m.incus.Delete(ctx, incusName); err != nil {
		m.failNodeDeleteJob(jobID, nodeID, err)
		return
	}

	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusSucceeded
		job.Progress = 100
		job.Stage = "complete"
		job.Message = "Node deleted"
		job.CompletedAt = &completedAt
	})

	m.db.Delete(&models.Node{}, "id = ?", nodeID)
}

// isNodeRegistered reports whether nodeIncusName currently exists as a
// registered Node object in the cluster, per `kubectl get node` run against
// the master. False on any error too (master unreachable, node genuinely
// absent) — both cases mean there's nothing to drain.
func (m *Manager) isNodeRegistered(ctx context.Context, masterIncusName, nodeIncusName string) bool {
	checkCmd := []string{"kubectl", "--kubeconfig=" + rootKubeconfigPath, "get", "node", nodeIncusName}
	result, err := m.incus.Exec(ctx, masterIncusName, checkCmd, nil)
	return err == nil && result.ExitCode == 0
}

// failNodeDeleteJob marks the job and node as failed, mirroring failNodeJob.
func (m *Manager) failNodeDeleteJob(jobID, nodeID string, runErr error) {
	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusFailed
		job.Progress = 100
		job.Stage = "failed"
		job.Message = "Node deletion failed"
		job.Error = runErr.Error()
		job.CompletedAt = &completedAt
	})

	m.updateNode(nodeID, map[string]any{
		"status":  string(models.NodeStatusFailed),
		"message": "Node deletion failed: " + runErr.Error(),
	})
}

// DeleteClusterJob creates a cluster deletion job and runs it in the
// background: it deletes every worker VM, then the master VM, then the
// Cluster row (nodes.cluster_id's ON DELETE CASCADE removes every Node row
// automatically). No kubectl-graceful steps are run — the whole control
// plane is going away, so draining a node has no lasting effect. This is
// the only way to remove a master.
func (m *Manager) DeleteClusterJob(ownerID, clusterID, masterIncusName string, workerIncusNames []string) (*models.Job, error) {
	now := time.Now().UTC()
	job := &models.Job{
		ID:        uuid.NewString(),
		OwnerID:   ownerID,
		Type:      "cluster_deletion",
		Name:      fmt.Sprintf("Delete cluster %s", clusterID),
		Status:    models.JobStatusQueued,
		Progress:  0,
		Stage:     "queued",
		Message:   "Cluster deletion job accepted",
		Metadata:  map[string]string{"clusterId": clusterID},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	go func() {
		defer recoverJobPanic(func(err error) { m.failClusterDeleteJob(job.ID, clusterID, err) })
		m.runClusterDeleteJob(job.ID, clusterID, masterIncusName, workerIncusNames)
	}()

	return job, nil
}

func (m *Manager) runClusterDeleteJob(jobID, clusterID, masterIncusName string, workerIncusNames []string) {
	ctx, cancel := context.WithTimeout(context.Background(), clusterDeleteTimeout)
	defer cancel()

	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusRunning
	})

	total := len(workerIncusNames)
	for i, workerIncusName := range workerIncusNames {
		progress := 10 + (50*(i+1))/max(total, 1)
		m.updateJob(jobID, func(job *models.Job) {
			job.Stage = "deleting-workers"
			job.Progress = progress
			job.Message = fmt.Sprintf("Deleting worker %d of %d...", i+1, total)
		})

		if err := m.incus.Delete(ctx, workerIncusName); err != nil {
			m.failClusterDeleteJob(jobID, clusterID, err)
			return
		}
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "deleting-master"
		job.Progress = 70
		job.Message = "Deleting master..."
	})

	if err := m.incus.Delete(ctx, masterIncusName); err != nil {
		m.failClusterDeleteJob(jobID, clusterID, err)
		return
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "cleaning-up"
		job.Progress = 90
		job.Message = "Removing cluster records..."
	})

	m.db.Delete(&models.Cluster{}, "id = ?", clusterID)

	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusSucceeded
		job.Progress = 100
		job.Stage = "complete"
		job.Message = "Cluster deleted"
		job.CompletedAt = &completedAt
	})
}

// failClusterDeleteJob marks the job and cluster as failed. Node rows are
// left as-is for inspection — retrying DELETE on the cluster will attempt
// the same VM teardowns again (Client.Delete is idempotent).
func (m *Manager) failClusterDeleteJob(jobID, clusterID string, runErr error) {
	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusFailed
		job.Progress = 100
		job.Stage = "failed"
		job.Message = "Cluster deletion failed"
		job.Error = runErr.Error()
		job.CompletedAt = &completedAt
	})

	m.db.Model(&models.Cluster{}).Where("id = ?", clusterID).Updates(map[string]any{
		"status":  string(models.ClusterStatusFailed),
		"message": "Cluster deletion failed: " + runErr.Error(),
	})
}
