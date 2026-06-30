package deployqueue

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Queue struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Queue {
	return &Queue{db: db}
}

type EnqueueInput struct {
	ServiceID      string
	TeamID         string
	TriggerType    string // webhook|manual|api
	CommitSHA      string
	CommitMessage  string
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
