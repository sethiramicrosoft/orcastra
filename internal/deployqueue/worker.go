package deployqueue

import (
	"context"
	"fmt"
	"time"

	"github.com/sethiramicrosoft/orcastra/internal/ai"
	"github.com/sethiramicrosoft/orcastra/internal/hostdriver"
	"github.com/sethiramicrosoft/orcastra/internal/hostdriver/local"
)

type Worker struct {
	queue        *Queue
	localDriver  *local.Driver
	pollInterval time.Duration
}

func NewWorker(queue *Queue, pollInterval time.Duration) *Worker {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	ld, err := local.New()
	if err != nil {
		ld = nil
	}
	return &Worker{
		queue:        queue,
		localDriver:  ld,
		pollInterval: pollInterval,
	}
}

func (w *Worker) Start(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.processOnce(ctx); err != nil {
				// keep worker alive; failure is logged in deployment rows
			}
		}
	}
}

func (w *Worker) processOnce(ctx context.Context) error {
	job, err := w.queue.ClaimNext(ctx)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}

	if !job.IsLocalhost {
		_ = w.queue.AppendLog(ctx, job.DeploymentID, "stderr", "remote deployment path not wired yet")
		return w.markFailedWithAI(ctx, job, "Remote deployment is not yet enabled for this build.", "Use localhost server for now, or wait for SSH executor wiring.")
	}

	if job.DockerImage == "" {
		_ = w.queue.AppendLog(ctx, job.DeploymentID, "stderr", "service has no docker_image")
		return w.markFailedWithAI(ctx, job, "Service is missing docker_image.", "Set docker_image on the service before deploying.")
	}
	if w.localDriver == nil {
		_ = w.queue.AppendLog(ctx, job.DeploymentID, "stderr", "local driver unavailable")
		return w.markFailedWithAI(ctx, job, "Local host driver is unavailable.", "Restart Orcastra and confirm Docker socket access.")
	}

	envVars, envErr := w.queue.BuildServiceEnv(ctx, job.ServiceID, job.TeamID)
	if envErr != nil {
		_ = w.queue.AppendLog(ctx, job.DeploymentID, "stderr", "failed to load service secrets: "+envErr.Error())
		return w.markFailedWithAI(ctx, job, "Failed to load service secrets.", envErr.Error())
	}

	containerName := fmt.Sprintf("orcastra-%s-%s", short(job.ServiceID), short(job.DeploymentID))
	containerID, err := w.localDriver.RunContainer(ctx, hostdriver.ContainerSpec{
		Name:  containerName,
		Image: job.DockerImage,
		Env:   envVars,
		Labels: map[string]string{
			"orcastra.service_id":    job.ServiceID,
			"orcastra.deployment_id": job.DeploymentID,
		},
	})
	if err != nil {
		_ = w.queue.AppendLog(ctx, job.DeploymentID, "stderr", err.Error())
		return w.markFailedWithAI(ctx, job, "Docker run failed for this image.", err.Error())
	}

	_ = w.queue.AppendLog(ctx, job.DeploymentID, "stdout", "container started: "+containerID)
	return w.queue.MarkRunning(ctx, job.DeploymentID)
}

func (w *Worker) markFailedWithAI(ctx context.Context, job *Job, fallbackDiagnosis, failureLine string) error {
	diagnosis := fallbackDiagnosis
	suggestion := failureLine

	cfg, err := w.queue.GetAIProviderConfig(ctx, job.TeamID)
	if err == nil && cfg != nil && cfg.Enabled {
		provider, pErr := ai.NewProvider(ai.Config{
			Type:    ai.ProviderType(cfg.ProviderType),
			BaseURL: cfg.BaseURL,
			APIKey:  cfg.APIKey,
			Model:   cfg.Model,
		})
		if pErr == nil {
			result, aErr := provider.Analyze(ctx, ai.AnalysisRequest{
				LogLines:    []string{failureLine},
				ServiceName: job.ServiceName,
				TriggerType: job.TriggerType,
				CommitSHA:   job.CommitSHA,
			})
			if aErr == nil && result != nil && result.Diagnosis != "" {
				diagnosis = result.Diagnosis
				if result.Suggestion != "" {
					suggestion = result.Suggestion
				}
			}
		}
	}

	return w.queue.MarkFailed(ctx, job.DeploymentID, diagnosis, suggestion)
}

func short(v string) string {
	if len(v) <= 8 {
		return v
	}
	return v[:8]
}
