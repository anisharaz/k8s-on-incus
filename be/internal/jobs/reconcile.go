package jobs

import (
	"log"
	"time"

	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
)

// Reconcile marks any jobs left "queued" or "running" from a previous
// process lifetime (crash, deploy, restart) as failed, and fails the
// resources they were operating on too. The goroutine that owned each job
// died with the old process, so without this a job frozen mid-flight would
// never update its row again — and since node/cluster deletion refuses to
// touch a resource already "creating"/"deleting" (see
// handlers.NodeHandlers.DeleteNode, handlers.ClusterHandlers.DeleteCluster),
// that resource would be permanently stuck and undeletable through the API.
// Called once at startup, before the server starts accepting requests.
func (m *Manager) Reconcile() {
	var interrupted []models.Job
	if err := m.db.
		Where("status IN ?", []models.JobStatus{models.JobStatusQueued, models.JobStatusRunning}).
		Find(&interrupted).Error; err != nil {
		log.Printf("job reconciliation: failed to query interrupted jobs: %v", err)
		return
	}

	for _, job := range interrupted {
		m.reconcileJob(job)
	}
}

func (m *Manager) reconcileJob(job models.Job) {
	const reason = "job was interrupted by a server restart"
	completedAt := time.Now().UTC()

	m.updateJob(job.ID, func(j *models.Job) {
		j.Status = models.JobStatusFailed
		j.Stage = "failed"
		j.Message = "Job interrupted"
		j.Error = reason
		j.CompletedAt = &completedAt
	})

	// job.Metadata is nil-safe to index: a missing key just reads as "".
	if nodeID := job.Metadata["nodeId"]; nodeID != "" {
		m.updateNode(nodeID, map[string]any{
			"status":  string(models.NodeStatusFailed),
			"message": "Node operation " + reason,
		})
		m.setClusterStatusIfMaster(nodeID, models.ClusterStatusFailed, "Master node operation "+reason)
	}

	if clusterID := job.Metadata["clusterId"]; clusterID != "" {
		m.db.Model(&models.Cluster{}).Where("id = ?", clusterID).Updates(map[string]any{
			"status":  string(models.ClusterStatusFailed),
			"message": "Cluster operation " + reason,
		})
		// Cluster deletion also marks every one of its nodes "deleting"
		// synchronously before the job starts (see
		// handlers.ClusterHandlers.DeleteCluster) — none of that is
		// reflected in the job's own metadata, so sweep those up too, or
		// they'd be stuck at "deleting" forever.
		m.db.Model(&models.Node{}).
			Where("cluster_id = ? AND status = ?", clusterID, string(models.NodeStatusDeleting)).
			Updates(map[string]any{
				"status":  string(models.NodeStatusFailed),
				"message": "Node operation " + reason,
			})
	}

	log.Printf("job reconciliation: marked interrupted job %s (%s, %q) as failed", job.ID, job.Type, job.Name)
}
