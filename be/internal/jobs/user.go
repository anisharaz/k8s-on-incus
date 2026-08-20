package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
	"github.com/google/uuid"
)

// userDeleteTimeout bounds a whole-user deletion job (every cluster's VMs
// torn down, every network removed, fail-fast).
const userDeleteTimeout = 30 * time.Minute

// DeleteUserJob creates a user deletion job and runs it in the background:
// it deletes every VM in every cluster the user owns, then those clusters'
// records, then every network the user owns, then finally the user record
// itself. ownerID is the admin performing the deletion (so it shows up in
// their own job list, not the deleted user's, which is gone by the end of
// this); targetUserID is whose resources/account are being removed.
func (m *Manager) DeleteUserJob(ownerID, targetUserID string) (*models.Job, error) {
	now := time.Now().UTC()
	job := &models.Job{
		ID:        uuid.NewString(),
		OwnerID:   ownerID,
		Type:      "user_deletion",
		Name:      fmt.Sprintf("Delete user %s", targetUserID),
		Status:    models.JobStatusQueued,
		Progress:  0,
		Stage:     "queued",
		Message:   "User deletion job accepted",
		Metadata:  map[string]string{"userId": targetUserID},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	go func() {
		defer recoverJobPanic(func(err error) { m.failUserDeleteJob(job.ID, err) })
		m.runUserDeleteJob(job.ID, targetUserID)
	}()

	return job, nil
}

func (m *Manager) runUserDeleteJob(jobID, targetUserID string) {
	ctx, cancel := context.WithTimeout(context.Background(), userDeleteTimeout)
	defer cancel()

	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusRunning
		job.Stage = "deleting-clusters"
		job.Progress = 5
		job.Message = "Deleting user's clusters..."
	})

	var clusters []models.Cluster
	if err := m.db.Where("owner_id = ?", targetUserID).Find(&clusters).Error; err != nil {
		m.failUserDeleteJob(jobID, fmt.Errorf("list clusters: %w", err))
		return
	}

	for i, cluster := range clusters {
		progress := 10 + (50*(i+1))/max(len(clusters), 1)
		m.updateJob(jobID, func(job *models.Job) {
			job.Progress = progress
			job.Message = fmt.Sprintf("Deleting cluster %d of %d...", i+1, len(clusters))
		})

		var nodes []models.Node
		if err := m.db.Where("cluster_id = ?", cluster.ID).Find(&nodes).Error; err != nil {
			m.failUserDeleteJob(jobID, fmt.Errorf("list nodes for cluster %q: %w", cluster.Name, err))
			return
		}

		for _, node := range nodes {
			if err := m.incus.Delete(ctx, node.IncusName); err != nil {
				m.failUserDeleteJob(jobID, fmt.Errorf("delete vm %q: %w", node.IncusName, err))
				return
			}
		}

		// Nodes cascade-delete with the cluster row (nodes.cluster_id ON
		// DELETE CASCADE) — their VMs are already gone above, so that's
		// just cleaning up bookkeeping, not a second teardown pass.
		if err := m.db.Delete(&models.Cluster{}, "id = ?", cluster.ID).Error; err != nil {
			m.failUserDeleteJob(jobID, fmt.Errorf("delete cluster record %q: %w", cluster.Name, err))
			return
		}
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "deleting-networks"
		job.Progress = 70
		job.Message = "Deleting user's networks..."
	})

	var networks []models.ClusterNetwork
	if err := m.db.Where("owner_id = ?", targetUserID).Find(&networks).Error; err != nil {
		m.failUserDeleteJob(jobID, fmt.Errorf("list networks: %w", err))
		return
	}

	// Every cluster referencing a network is already gone (deleted above),
	// so clusters.network_id's ON DELETE RESTRICT can no longer block this.
	for _, network := range networks {
		if err := m.incus.DeleteNetwork(ctx, network.IncusName); err != nil {
			m.failUserDeleteJob(jobID, fmt.Errorf("delete network %q: %w", network.Name, err))
			return
		}
		if err := m.db.Delete(&models.ClusterNetwork{}, "id = ?", network.ID).Error; err != nil {
			m.failUserDeleteJob(jobID, fmt.Errorf("delete network record %q: %w", network.Name, err))
			return
		}
	}

	m.updateJob(jobID, func(job *models.Job) {
		job.Stage = "deleting-user"
		job.Progress = 90
		job.Message = "Deleting user account..."
	})

	if err := m.db.Delete(&models.User{}, "id = ?", targetUserID).Error; err != nil {
		m.failUserDeleteJob(jobID, fmt.Errorf("delete user record: %w", err))
		return
	}

	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusSucceeded
		job.Progress = 100
		job.Stage = "complete"
		job.Message = "User and all owned resources deleted"
		job.CompletedAt = &completedAt
	})
}

// failUserDeleteJob marks the job as failed. Unlike node/cluster deletion
// failures, there's no per-resource status field on User to also update —
// whatever was deleted before the failure stays deleted, and retrying the
// same delete-user action will simply pick up wherever it left off (each
// step re-queries "owned by this user" fresh rather than working off a
// fixed snapshot).
func (m *Manager) failUserDeleteJob(jobID string, runErr error) {
	completedAt := time.Now().UTC()
	m.updateJob(jobID, func(job *models.Job) {
		job.Status = models.JobStatusFailed
		job.Progress = 100
		job.Stage = "failed"
		job.Message = "User deletion failed"
		job.Error = runErr.Error()
		job.CompletedAt = &completedAt
	})
}
