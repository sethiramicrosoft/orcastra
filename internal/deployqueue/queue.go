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
	ServiceName  string
	TriggerType  string
	CommitSHA    string
	DockerImage  string
	GitRepoURL   string
	GitBranch    string
	IsLocalhost  bool
}

type AIProviderConfig struct {
	ProviderType string
	BaseURL      string
	Model        string
	APIKey       string
	Enabled      bool
}

type ServiceSecret struct {
	Key       string
	Value     string
	Version   int
	CreatedAt time.Time
}

func (q *Queue) Enqueue(ctx context.Context, in EnqueueInput) (*Deployment, error) {
	if in.ServiceID == "" || in.TeamID == "" || in.TriggerType == "" {
		return nil, fmt.Errorf("serviceID, teamID and triggerType are required")
	}

	deploymentID := uuid.NewString()
	key := buildIdempotencyKey(in.ServiceID, in.CommitSHA, in.TriggerType, deploymentID)
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
			       COALESCE(s.name, '') AS service_name,
			       d.trigger_type,
			       COALESCE(d.commit_sha, '') AS commit_sha,
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
		RETURNING next_job.id, next_job.service_id, next_job.team_id, next_job.service_name, next_job.trigger_type, next_job.commit_sha, next_job.docker_image, next_job.git_repo_url, next_job.git_branch, next_job.is_localhost
	`).Scan(
		&job.DeploymentID,
		&job.ServiceID,
		&job.TeamID,
		&job.ServiceName,
		&job.TriggerType,
		&job.CommitSHA,
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

func buildIdempotencyKey(serviceID, commitSHA, triggerType, nonce string) string {
	if commitSHA == "" {
		return fmt.Sprintf("%s:%s:%s", serviceID, triggerType, nonce)
	}
	return fmt.Sprintf("%s:%s", serviceID, commitSHA)
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (q *Queue) GetAIProviderConfig(ctx context.Context, teamID string) (*AIProviderConfig, error) {
	var (
		cfg      AIProviderConfig
		baseURL  *string
		apiKeyCT []byte
	)
	err := q.db.QueryRow(ctx, `
		SELECT provider_type, base_url, model, api_key_ct, is_enabled
		FROM ai_provider_configs
		WHERE team_id = $1
		LIMIT 1
	`, teamID).Scan(&cfg.ProviderType, &baseURL, &cfg.Model, &apiKeyCT, &cfg.Enabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load ai provider config: %w", err)
	}
	if baseURL != nil {
		cfg.BaseURL = *baseURL
	}
	if len(apiKeyCT) > 0 {
		cfg.APIKey = string(apiKeyCT)
	}
	return &cfg, nil
}

func (q *Queue) GetLatestServiceSecrets(ctx context.Context, serviceID, teamID string) ([]ServiceSecret, error) {
	rows, err := q.db.Query(ctx, `
		SELECT DISTINCT ON (s.key)
			s.key,
			COALESCE(convert_from(s.value_ct, 'UTF8'), ''),
			s.version,
			s.created_at
		FROM secrets s
		WHERE s.service_id = $1::uuid
		  AND s.team_id = $2::uuid
		ORDER BY s.key, s.version DESC
	`, serviceID, teamID)
	if err != nil {
		return nil, fmt.Errorf("load latest service secrets: %w", err)
	}
	defer rows.Close()

	out := make([]ServiceSecret, 0)
	for rows.Next() {
		var item ServiceSecret
		if scanErr := rows.Scan(&item.Key, &item.Value, &item.Version, &item.CreatedAt); scanErr != nil {
			return nil, fmt.Errorf("scan service secret: %w", scanErr)
		}
		out = append(out, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("iterate service secrets: %w", rowsErr)
	}
	return out, nil
}

func (q *Queue) BuildServiceEnv(ctx context.Context, serviceID, teamID string) (map[string]string, error) {
	items, err := q.GetLatestServiceSecrets(ctx, serviceID, teamID)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(items))
	for _, item := range items {
		env[item.Key] = item.Value
	}
	return env, nil
}
