package web

import (
	"context"
	"sync"

	"github.com/albertoruiz/space-elevator/internal/store"
)

// deploymentRunner is a small in-process job runner. It gives HTTP handlers a
// clean submit boundary while keeping the single-binary deployment model. The
// durable operation row remains the source of truth; this queue is only the
// execution lane and can later be hosted by a `space-elevator deploy-worker`
// command without changing the deployer.
type deploymentRunner struct {
	jobs chan func()
	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

func newDeploymentRunner(workers int) *deploymentRunner {
	if workers < 1 {
		workers = 1
	}
	r := &deploymentRunner{jobs: make(chan func(), 32), stop: make(chan struct{})}
	for i := 0; i < workers; i++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			for {
				select {
				case <-r.stop:
					return
				case job := <-r.jobs:
					select {
					case <-r.stop:
						return
					default:
					}
					if job != nil {
						job()
					}
				}
			}
		}()
	}
	return r
}

func (r *deploymentRunner) submit(job func()) {
	if job == nil {
		return
	}
	select {
	case <-r.stop:
		return
	case r.jobs <- job:
	}
}

// stop rejects new work and waits for workers currently executing a job.
// Queued-but-not-started jobs are intentionally abandoned; their durable
// operation rows remain retryable after the next service start.
func (r *deploymentRunner) stopRunner() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
	r.wg.Wait()
}

func (s *Server) finishDeploymentOperation(op *store.AppOperation, status string, opErr error) {
	if op == nil {
		return
	}
	message := ""
	if opErr != nil {
		message = opErr.Error()
	}
	if err := s.Store.FinishOperation(context.Background(), op.ID, status, message); err != nil {
		log.Printf("deployment %s: finalize operation: %v", op.ID, err)
	}
}

func (s *Server) submitDeployment(job func()) {
	if s.DeployRunner != nil {
		s.DeployRunner.submit(job)
		return
	}
	// Lightweight test servers and CLI-adjacent helpers do not construct a
	// full Server; preserve their synchronous setup with a safe fallback.
	go job()
}
