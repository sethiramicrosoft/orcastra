package healthmonitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sethiramicrosoft/orcastra/internal/hostdriver/local"
)

type Monitor struct {
	db       *pgxpool.Pool
	driver   *local.Driver
	interval time.Duration
}

func New(db *pgxpool.Pool, interval time.Duration) (*Monitor, error) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	driver, err := local.New()
	if err != nil {
		return nil, fmt.Errorf("initialize local driver: %w", err)
	}
	return &Monitor{
		db:       db,
		driver:   driver,
		interval: interval,
	}, nil
}

func (m *Monitor) Start(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = m.checkOnce(ctx)
		}
	}
}

func (m *Monitor) checkOnce(ctx context.Context) error {
	rows, err := m.db.Query(ctx, `
		SELECT d.id, d.team_id, d.service_id, COALESCE(s.name, '')
		FROM deployments d
		JOIN services s ON s.id = d.service_id
		JOIN projects p ON p.id = s.project_id
		JOIN servers srv ON srv.id = p.server_id
		WHERE d.status = 'running'
		  AND s.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		  AND srv.deleted_at IS NULL
		  AND srv.is_localhost = true
		ORDER BY d.created_at DESC
		LIMIT 100
	`)
	if err != nil {
		return fmt.Errorf("query running deployments: %w", err)
	}
	defer rows.Close()

	type item struct {
		DeploymentID string
		TeamID       string
		ServiceID    string
		ServiceName  string
	}
	items := make([]item, 0)
	for rows.Next() {
		var it item
		if scanErr := rows.Scan(&it.DeploymentID, &it.TeamID, &it.ServiceID, &it.ServiceName); scanErr != nil {
			return fmt.Errorf("scan running deployment: %w", scanErr)
		}
		items = append(items, it)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate running deployments: %w", rowsErr)
	}

	for _, it := range items {
		containerID, cErr := m.lookupContainerID(ctx, it.DeploymentID)
		if cErr != nil || strings.TrimSpace(containerID) == "" {
			continue
		}
		info, iErr := m.driver.Inspect(ctx, containerID)
		if iErr != nil || info == nil || !info.Running {
			_, _ = m.db.Exec(ctx, `
				UPDATE deployments
				SET status = 'failed',
				    ai_diagnosis = COALESCE(ai_diagnosis, 'Health monitor detected container is not running.'),
				    ai_suggestion = COALESCE(ai_suggestion, 'Redeploy service and inspect container logs.'),
				    finished_at = now()
				WHERE id = $1::uuid AND status = 'running'
			`, it.DeploymentID)
			_, _ = m.db.Exec(ctx, `
				INSERT INTO deployment_logs (deployment_id, stream, line, ts)
				VALUES ($1::uuid, 'stderr', $2, now())
			`, it.DeploymentID, "health monitor: detected stopped container")
			_, _ = m.db.Exec(ctx, `
				INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id, meta)
				VALUES (NULL, 'health.alert', 'deployment', $1::uuid, $2::uuid,
				        jsonb_build_object('container_id', $3::text, 'service', $4::text))
			`, it.DeploymentID, it.TeamID, containerID, it.ServiceName)
		}
	}
	return nil
}

func (m *Monitor) lookupContainerID(ctx context.Context, deploymentID string) (string, error) {
	var line string
	err := m.db.QueryRow(ctx, `
		SELECT line
		FROM deployment_logs
		WHERE deployment_id = $1::uuid
		  AND stream = 'stdout'
		  AND line LIKE 'container started:%'
		ORDER BY id DESC
		LIMIT 1
	`, deploymentID).Scan(&line)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "container started:")), nil
}
