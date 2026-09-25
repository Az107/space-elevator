package web

import (
	"context"
	"sync"

	"github.com/albertoruiz/space-elevator/internal/store"
)

// deployStepSpec is the stable UI vocabulary for a deployment. Runtime
// hooks still emit coarse stages; the progress adapter maps those stages
// into this ordered plan.
type deployStepSpec struct {
	Key   string
	Label string
}

var deploymentPlan = []deployStepSpec{
	{Key: "validate", Label: "Validate request"},
	{Key: "source", Label: "Acquire source"},
	{Key: "compose", Label: "Resolve compose"},
	{Key: "build", Label: "Build images"},
	{Key: "backup", Label: "Back up persistent data"},
	{Key: "stop", Label: "Stop previous release"},
	{Key: "rollback", Label: "Restore previous release"},
	{Key: "activate", Label: "Start candidate release"},
	{Key: "verify", Label: "Verify runtime"},
	{Key: "route", Label: "Publish route"},
	{Key: "commit", Label: "Commit deployment"},
}

// deployProgress is the durable bridge between deployer callbacks and the
// operation UI. The in-memory buildLog remains a compatibility/cache view;
// the operation tables are the source of truth that survives a refresh and
// service restart.
type deployProgress struct {
	store *store.Store
	op    *store.AppOperation
	app   *store.App
	mu    sync.Mutex
	step  string
	index map[string]int
}

func newDeployProgress(st *store.Store, app *store.App, op *store.AppOperation) *deployProgress {
	p := &deployProgress{store: st, op: op, app: app, index: make(map[string]int)}
	for i, step := range deploymentPlan {
		p.index[step.Key] = i
	}
	if op != nil {
		steps := make([]store.DeployStep, 0, len(deploymentPlan))
		for i, step := range deploymentPlan {
			steps = append(steps, store.DeployStep{Position: i + 1, Key: step.Key, Label: step.Label})
		}
		if err := st.CreateDeploySteps(context.Background(), op.ID, steps); err == nil {
			p.step = "validate"
		}
	}
	return p
}

func (p *deployProgress) append(level, message string) {
	if p == nil || p.op == nil || message == "" {
		return
	}
	p.mu.Lock()
	step := p.step
	p.mu.Unlock()
	_, _ = p.store.AppendDeployLog(context.Background(), p.op.ID, step, level, message)
}

// log records a deployer/runtime output line.
func (p *deployProgress) log(message string) { p.append("info", message) }

// stage advances the checklist and mirrors the transition to the operation.
func (p *deployProgress) stage(stage string) {
	if p == nil || p.op == nil {
		return
	}
	key := stage
	switch stage {
	case "preflight":
		key = "validate"
	case "clone":
		key = "source"
	case "deploy":
		key = "activate"
	}
	target, ok := p.index[key]
	if !ok {
		p.append("info", "step: "+stage)
		return
	}

	p.mu.Lock()
	current := p.step
	p.mu.Unlock()
	if current == key {
		return
	}
	now := p.app.CurrentReleaseID != ""
	if current != "" {
		_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, current, "succeeded", "")
	}
	for i, spec := range deploymentPlan {
		if i >= target {
			break
		}
		// A fresh app has no old release to stop or back up. Mark those
		// phases skipped so the checklist is honest instead of pretending
		// work happened.
		if !now && (spec.Key == "backup" || spec.Key == "stop" || spec.Key == "rollback") {
			_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, spec.Key, "skipped", "")
		} else if spec.Key == "rollback" && current != "rollback" {
			_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, spec.Key, "skipped", "")
		} else {
			_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, spec.Key, "succeeded", "")
		}
	}
	p.mu.Lock()
	p.step = key
	p.mu.Unlock()
	_ = p.store.SetOperationStep(context.Background(), p.op.ID, key)
	_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, key, "running", "")
	p.append("info", "step: "+deploymentPlan[target].Label)
}

func (p *deployProgress) success() {
	if p == nil || p.op == nil {
		return
	}
	p.mu.Lock()
	current := p.step
	p.step = "commit"
	p.mu.Unlock()
	if current != "" {
		_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, current, "succeeded", "")
	}
	_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, "commit", "succeeded", "")
	_ = p.store.SetOperationStep(context.Background(), p.op.ID, "commit")
	p.append("info", "deployment completed")
}

func (p *deployProgress) failure(err error) {
	if p == nil || p.op == nil || err == nil {
		return
	}
	currentOperation, _ := p.store.GetOperation(context.Background(), p.op.ID)
	if currentOperation != nil && currentOperation.Status == store.OperationStatusRolledBack {
		p.mu.Lock()
		current := p.step
		p.mu.Unlock()
		if current != "" {
			_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, current, "rolled_back", "")
		}
		p.append("warn", err.Error())
		return
	}
	p.mu.Lock()
	current := p.step
	p.mu.Unlock()
	if current != "" {
		_ = p.store.UpdateDeployStep(context.Background(), p.op.ID, current, "failed", err.Error())
	}
	p.append("error", err.Error())
}

func (p *deployProgress) rollback() {
	if p == nil || p.op == nil {
		return
	}
	p.append("warn", "restoring the previous release")
}

func deploymentStatusLabel(status string) string {
	switch status {
	case store.OperationStatusPreflighting:
		return "Preparing"
	case store.OperationStatusBuilding:
		return "Building"
	case store.OperationStatusBackingUp:
		return "Backing up"
	case store.OperationStatusCuttingOver:
		return "Starting"
	case store.OperationStatusVerifying:
		return "Verifying"
	case store.OperationStatusRollingBack:
		return "Rolling back"
	case store.OperationStatusCompleted:
		return "Succeeded"
	case store.OperationStatusFailed:
		return "Failed"
	case store.OperationStatusRolledBack:
		return "Rolled back"
	case store.OperationStatusRollbackFailed:
		return "Rollback failed"
	case store.OperationStatusCancelled:
		return "Cancelled"
	case store.OperationStatusInterrupted:
		return "Interrupted"
	default:
		if status == "" {
			return "Queued"
		}
		return status
	}
}

func deploymentStatusDescription(op *store.AppOperation) string {
	if op == nil {
		return ""
	}
	switch op.Status {
	case store.OperationStatusCompleted:
		return "The app is running and the new release is available."
	case store.OperationStatusRolledBack:
		return "The deployment failed, but the previous release was restored and remains available."
	case store.OperationStatusRollbackFailed:
		return "The deployment failed and the previous release could not be restored automatically. The app needs operator attention."
	case store.OperationStatusCancelled:
		return "The deployment was cancelled before completion."
	case store.OperationStatusInterrupted:
		return "The service stopped while this deployment was running. Retry it to start a fresh attempt."
	}
	if op.Error != "" {
		return op.Error
	}
	return "The deployment pipeline is running on the space-elevator host."
}

func operationTypeLabel(op *store.AppOperation) string {
	if op == nil {
		return "Deployment"
	}
	switch op.OperationType {
	case "update", "app.update":
		return "Update"
	case "redeploy", "app.redeploy":
		return "Redeploy"
	case "retry":
		return "Retry"
	case "create", "app.deploy":
		return "Initial deployment"
	default:
		return "Deployment"
	}
}

func operationIsActive(op *store.AppOperation) bool {
	if op == nil {
		return false
	}
	switch op.Status {
	case store.OperationStatusPreflighting, store.OperationStatusBuilding, store.OperationStatusBackingUp,
		store.OperationStatusCuttingOver, store.OperationStatusVerifying, store.OperationStatusRollingBack:
		return true
	default:
		return false
	}
}

func operationCanRetry(op *store.AppOperation) bool {
	if op == nil {
		return false
	}
	switch op.Status {
	case store.OperationStatusFailed, store.OperationStatusRolledBack, store.OperationStatusRollbackFailed, store.OperationStatusInterrupted:
		return true
	default:
		return false
	}
}
