package deployqueue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Queue struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Queue {
	return &Queue{db: db}
}

type EnqueueInput struct {
	ServiceID       string
	TeamID          string
	TriggerType     string // webhook|manual|api
	CommitSHA       string
	CommitMessage   string
	TriggeredByUser *string
}

type Deployment struct {
	ID             string
	ServiceID      string
	TeamID         string
	IdempotencyKey string
	Status         string
	CommitSHA      string
	CreatedAt      time.Time
}

type Job struct {
	DeploymentID string
	ServiceID    string
	TeamID       string
	DockerImage  string
	GitRepoURL   string
	GitBranch    string
	IsLocalhost  bool
}

func (q *Queue) Enqueue(ctx context.Context, in EnqueueInput) (*Deployment, error) {
	if in.ServiceID == "" || in.TeamID == "" || in.TriggerType == "" {
		return nil, fmt.Errorf("serviceID, teamID and triggerType are required")
	}

	key := buildIdempotencyKey(in.ServiceID, in.CommitSHA)
	deploymentID := uuid.NewString()
	now := time.Now().UTC()

	_, err := q.db.Exec(ctx, `
		INSERT INTO deployments (
			id, service_id, team_id, idempotency_key, commit_sha, commit_message,
			triggered_by, trigger_type, status, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'queued', $9)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, deploymentID, in.ServiceID, in.TeamID, key, nullable(in.CommitSHA), nullable(in.CommitMessage), in.TriggeredByUser, in.TriggerType, now)
	if err != nil {
		return nil, fmt.Errorf("insert deployment: %w", err)
	}

	var dep Deployment
	err = q.db.QueryRow(ctx, `
		SELECT id, service_id, team_id, idempotency_key, status, COALESCE(commit_sha, ''), created_at
		FROM deployments
		WHERE idempotency_key = $1
	`, key).Scan(&dep.ID, &dep.ServiceID, &dep.TeamID, &dep.IdempotencyKey, &dep.Status, &dep.CommitSHA, &dep.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("fetch deployment by idempotency key: %w", err)
	}
	return &dep, nil
}

func (q *Queue) ClaimNext(ctx context.Context) (*Job, error) {
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var job Job
	err = tx.QueryRow(ctx, `
		WITH next_job AS (
			SELECT d.id, d.service_id, d.team_id,
			       COALESCE(s.docker_image, '') AS docker_image,
			       COALESCE(s.git_repo_url, '') AS git_repo_url,
			       COALESCE(s.git_branch, 'main') AS git_branch,
			       srv.is_localhost
			FROM deployments d
			JOIN services s ON s.id = d.service_id
			JOIN projects p ON p.id = s.project_id
			JOIN servers srv ON srv.id = p.server_id
			WHERE d.status = 'queued' AND s.deleted_at IS NULL AND p.deleted_at IS NULL AND srv.deleted_at IS NULL
			ORDER BY d.created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE deployments d
		SET status = 'building', started_at = now()
		FROM next_job
		WHERE d.id = next_job.id
		RETURNING next_job.id, next_job.service_id, next_job.team_id, next_job.docker_image, next_job.git_repo_url, next_job.git_branch, next_job.is_localhost
	`).Scan(
		&job.DeploymentID,
		&job.ServiceID,
		&job.TeamID,
		&job.DockerImage,
		&job.GitRepoURL,
		&job.GitBranch,
		&job.IsLocalhost,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim next deployment: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim transaction: %w", err)
	}
	return &job, nil
}

func (q *Queue) MarkFailed(ctx context.Context, deploymentID, diagnosis, suggestion string) error {
	_, err := q.db.Exec(ctx, `
		UPDATE deployments
		SET status = 'failed', ai_diagnosis = $2, ai_suggestion = $3, finished_at = now()
		WHERE id = $1
	`, deploymentID, nullable(diagnosis), nullable(suggestion))
	if err != nil {
		return fmt.Errorf("mark deployment failed: %w", err)
	}
	return nil
}

func (q *Queue) MarkRunning(ctx context.Context, deploymentID string) error {
	_, err := q.db.Exec(ctx, `
		UPDATE deployments
		SET status = 'running', finished_at = now()
		WHERE id = $1
	`, deploymentID)
	if err != nil {
		return fmt.Errorf("mark deployment running: %w", err)
	}
	return nil
}

func (q *Queue) AppendLog(ctx context.Context, deploymentID, stream, line string) error {
	if deploymentID == "" || stream == "" {
		return fmt.Errorf("deploymentID and stream are required")
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO deployment_logs (deployment_id, stream, line, ts)
		VALUES ($1, $2, $3, now())
	`, deploymentID, stream, line)
	if err != nil {
		return fmt.Errorf("append deployment log: %w", err)
	}
	return nil
}

func buildIdempotencyKey(serviceID, commitSHA string) string {
	if commitSHA == "" {
		return fmt.Sprintf("%s:manual", serviceID)
	}
	return fmt.Sprintf("%s:%s", serviceID, commitSHA)
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
