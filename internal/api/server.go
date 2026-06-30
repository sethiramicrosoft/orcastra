package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sethiramicrosoft/orcastra/internal/auth"
	"github.com/sethiramicrosoft/orcastra/internal/deployqueue"
	"github.com/sethiramicrosoft/orcastra/internal/webhookingest"
)

type Server struct {
	cfg    Config
	db     *pgxpool.Pool
	signer *auth.JWTSigner
	queue  *deployqueue.Queue
}

func NewServer(cfg Config, db *pgxpool.Pool) (*Server, error) {
	signer, err := auth.NewJWTSigner(cfg.JWTSecret, cfg.JWTIssuer)
	if err != nil {
		return nil, fmt.Errorf("configure jwt signer: %w", err)
	}
	return &Server{
		cfg:    cfg,
		db:     db,
		signer: signer,
		queue:  deployqueue.New(db),
	}, nil
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)

	r.Post("/api/v1/auth/register", s.handleRegister)
	r.Post("/api/v1/auth/login", s.handleLogin)
	r.With(s.requireAuth).Get("/api/v1/auth/me", s.handleMe)
	r.With(s.requireAuth).Get("/api/v1/dashboard", s.handleDashboard)
	r.With(s.requireAuth).Get("/api/v1/deployments/recent", s.handleRecentDeployments)
	r.With(s.requireAuth).Get("/api/v1/deployments/{deploymentID}/stream", s.handleDeploymentStream)
	r.With(s.requireAuth).Get("/api/v1/services", s.handleListServices)
	r.With(s.requireAuth).Post("/api/v1/servers/localhost", s.handleEnsureLocalhostServer)
	r.With(s.requireAuth).Post("/api/v1/projects", s.handleCreateProject)
	r.With(s.requireAuth).Post("/api/v1/services", s.handleCreateService)
	r.With(s.requireAuth).Post("/api/v1/ai/provider", s.handleUpsertAIProvider)

	r.With(s.requireAuth).Post("/api/v1/services/{serviceID}/deploy", s.handleManualDeploy)
	r.Post("/api/v1/webhooks/github", s.handleGitHubWebhook)

	return r
}

// RoutesWithUI mounts all API routes plus a catch-all that serves the embedded SPA.
func (s *Server) RoutesWithUI(uiHandler http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)

	r.Post("/api/v1/auth/register", s.handleRegister)
	r.Post("/api/v1/auth/login", s.handleLogin)
	r.With(s.requireAuth).Get("/api/v1/auth/me", s.handleMe)
	r.With(s.requireAuth).Get("/api/v1/dashboard", s.handleDashboard)
	r.With(s.requireAuth).Get("/api/v1/deployments/recent", s.handleRecentDeployments)
	r.With(s.requireAuth).Get("/api/v1/deployments/{deploymentID}/stream", s.handleDeploymentStream)
	r.With(s.requireAuth).Get("/api/v1/services", s.handleListServices)
	r.With(s.requireAuth).Post("/api/v1/servers/localhost", s.handleEnsureLocalhostServer)
	r.With(s.requireAuth).Post("/api/v1/projects", s.handleCreateProject)
	r.With(s.requireAuth).Post("/api/v1/services", s.handleCreateService)
	r.With(s.requireAuth).Post("/api/v1/ai/provider", s.handleUpsertAIProvider)

	r.With(s.requireAuth).Post("/api/v1/services/{serviceID}/deploy", s.handleManualDeploy)
	r.Post("/api/v1/webhooks/github", s.handleGitHubWebhook)

	// Serve frontend SPA for all non-API routes.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		// If the path has a file extension and isn't found, let the file server 404.
		// Otherwise fall back to index.html for client-side routing.
		uiHandler.ServeHTTP(w, req)
	})
	r.Handle("/*", uiHandler)

	return r
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	TeamName    string `json:"teamName"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Token string `json:"token"`
	User  struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"user"`
	Team struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"team"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Email == "" || req.Password == "" || req.TeamName == "" {
		writeError(w, http.StatusBadRequest, "email, password and teamName are required")
		return
	}

	ctx := r.Context()
	exists, err := s.emailExists(ctx, req.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing user")
		return
	}
	if exists {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	userID := uuid.NewString()
	teamID := uuid.NewString()
	teamSlug := slugify(req.TeamName)
	if teamSlug == "" {
		teamSlug = "team"
	}
	teamSlug = fmt.Sprintf("%s-%s", teamSlug, uuid.NewString()[:8])

	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, is_root_admin)
		VALUES ($1::uuid, $2, $3, $4, false)
	`, userID, strings.ToLower(strings.TrimSpace(req.Email)), passwordHash, nullableString(req.DisplayName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO teams (id, name, slug)
		VALUES ($1::uuid, $2, $3)
	`, teamID, req.TeamName, teamSlug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create team")
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, 'owner')
	`, teamID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link user to team")
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id, meta)
		VALUES ($1::uuid, 'auth.register', 'user', $1::uuid, $2::uuid, jsonb_build_object('email', $3::text))
	`, userID, teamID, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil {
		log.Printf("audit insert failed (register): %v", err)
		writeError(w, http.StatusInternalServerError, "failed to write audit event")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit registration")
		return
	}

	token, err := s.signer.Sign(userID, teamID, s.cfg.JWTTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign token")
		return
	}

	var resp authResponse
	resp.Token = token
	resp.User.ID = userID
	resp.User.Email = strings.ToLower(strings.TrimSpace(req.Email))
	resp.User.DisplayName = req.DisplayName
	resp.Team.ID = teamID
	resp.Team.Name = req.TeamName
	resp.Team.Role = "owner"
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	ctx := r.Context()
	var (
		userID       string
		email        string
		displayName  string
		passwordHash string
		teamID       string
		teamName     string
		teamRole     string
	)
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, COALESCE(u.display_name, ''), u.password_hash,
		       t.id, t.name, tm.role::text
		FROM users u
		JOIN team_members tm ON tm.user_id = u.id
		JOIN teams t ON t.id = tm.team_id
		WHERE u.email = $1 AND u.deleted_at IS NULL AND t.deleted_at IS NULL
		ORDER BY tm.created_at ASC
		LIMIT 1
	`, strings.ToLower(strings.TrimSpace(req.Email))).Scan(&userID, &email, &displayName, &passwordHash, &teamID, &teamName, &teamRole)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch user")
		return
	}

	match, err := auth.VerifyPassword(passwordHash, req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify credentials")
		return
	}
	if !match {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	_, err = s.db.Exec(ctx, `UPDATE users SET last_login_at = $1, updated_at = $1 WHERE id = $2`, time.Now().UTC(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update login timestamp")
		return
	}

	token, err := s.signer.Sign(userID, teamID, s.cfg.JWTTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign token")
		return
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id)
		VALUES ($1::uuid, 'auth.login', 'user', $1::uuid, $2::uuid)
	`, userID, teamID)
	if err != nil {
		log.Printf("audit insert failed (login): %v", err)
		writeError(w, http.StatusInternalServerError, "failed to write audit event")
		return
	}

	var resp authResponse
	resp.Token = token
	resp.User.ID = userID
	resp.User.Email = email
	resp.User.DisplayName = displayName
	resp.Team.ID = teamID
	resp.Team.Name = teamName
	resp.Team.Role = teamRole
	writeJSON(w, http.StatusOK, resp)
}

type authContextKey string

const claimsContextKey authContextKey = "claims"

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		token := strings.TrimPrefix(authz, "Bearer ")
		claims, err := s.signer.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}

	var (
		email       string
		displayName string
		teamName    string
		role        string
	)
	err := s.db.QueryRow(r.Context(), `
		SELECT u.email, COALESCE(u.display_name, ''), t.name, tm.role::text
		FROM users u
		JOIN team_members tm ON tm.user_id = u.id AND tm.team_id = $2
		JOIN teams t ON t.id = tm.team_id
		WHERE u.id = $1 AND u.deleted_at IS NULL AND t.deleted_at IS NULL
	`, claims.UserID, claims.TeamID).Scan(&email, &displayName, &teamName, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch profile")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]string{
			"id":          claims.UserID,
			"email":       email,
			"displayName": displayName,
		},
		"team": map[string]string{
			"id":   claims.TeamID,
			"name": teamName,
			"role": role,
		},
	})
}

type manualDeployResponse struct {
	DeploymentID string `json:"deploymentId"`
	Status       string `json:"status"`
	CommitSHA    string `json:"commitSha,omitempty"`
}

func (s *Server) handleManualDeploy(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	serviceID := chi.URLParam(r, "serviceID")
	if serviceID == "" {
		writeError(w, http.StatusBadRequest, "serviceID is required")
		return
	}

	var req struct {
		CommitSHA     string `json:"commitSha"`
		CommitMessage string `json:"commitMessage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	allowed, err := s.userCanDeployToService(r.Context(), claims.UserID, claims.TeamID, serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate service access")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "service not found or not in your team")
		return
	}

	userID := claims.UserID
	dep, err := s.queue.Enqueue(r.Context(), deployqueue.EnqueueInput{
		ServiceID:       serviceID,
		TeamID:          claims.TeamID,
		TriggerType:     "manual",
		CommitSHA:       strings.TrimSpace(req.CommitSHA),
		CommitMessage:   strings.TrimSpace(req.CommitMessage),
		TriggeredByUser: &userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue deployment")
		return
	}

	_, err = s.db.Exec(r.Context(), `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id, meta)
		VALUES ($1::uuid, 'service.deploy.manual', 'deployment', $2::uuid, $3::uuid, jsonb_build_object('serviceId', $4::text, 'commitSha', $5::text))
	`, claims.UserID, dep.ID, claims.TeamID, serviceID, nullableString(req.CommitSHA))
	if err != nil {
		log.Printf("audit insert failed (manual deploy): %v", err)
		writeError(w, http.StatusInternalServerError, "failed to write audit event")
		return
	}

	writeJSON(w, http.StatusAccepted, manualDeployResponse{
		DeploymentID: dep.ID,
		Status:       dep.Status,
		CommitSHA:    dep.CommitSHA,
	})
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GitHubWebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "github webhook secret not configured")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "push" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read payload")
		return
	}

	signature := r.Header.Get("X-Hub-Signature-256")
	if !webhookingest.VerifyGitHubSignature(s.cfg.GitHubWebhookSecret, body, signature) {
		writeError(w, http.StatusUnauthorized, "invalid github signature")
		return
	}

	pushEvent, err := webhookingest.ParseGitHubPushEvent(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	branch := webhookingest.BranchFromRef(pushEvent.Ref)
	repoCandidates := []string{
		webhookingest.NormalizeRepo(pushEvent.Repository.FullName),
		webhookingest.NormalizeRepo(pushEvent.Repository.CloneURL),
		webhookingest.NormalizeRepo(pushEvent.Repository.HTMLURL),
		webhookingest.NormalizeRepo(pushEvent.Repository.SSHURL),
	}

	type serviceRow struct {
		ID      string
		TeamID  string
		RepoURL string
		Branch  string
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT id, team_id, COALESCE(git_repo_url, ''), COALESCE(git_branch, 'main')
		FROM services
		WHERE deleted_at IS NULL AND git_repo_url IS NOT NULL
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load services")
		return
	}
	defer rows.Close()

	enqueued := 0
	for rows.Next() {
		var svc serviceRow
		if err := rows.Scan(&svc.ID, &svc.TeamID, &svc.RepoURL, &svc.Branch); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read services")
			return
		}
		if !matchesRepo(repoCandidates, webhookingest.NormalizeRepo(svc.RepoURL)) {
			continue
		}
		if strings.TrimSpace(svc.Branch) != branch {
			continue
		}

		_, err := s.queue.Enqueue(r.Context(), deployqueue.EnqueueInput{
			ServiceID:       svc.ID,
			TeamID:          svc.TeamID,
			TriggerType:     "webhook",
			CommitSHA:       strings.TrimSpace(pushEvent.After),
			CommitMessage:   strings.TrimSpace(pushEvent.HeadCommit.Message),
			TriggeredByUser: nil,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to enqueue webhook deployment")
			return
		}
		enqueued++
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate services")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":   "accepted",
		"enqueued": enqueued,
		"branch":   branch,
		"repo":     pushEvent.Repository.FullName,
	})
}

func (s *Server) userCanDeployToService(ctx context.Context, userID, teamID, serviceID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM services s
			JOIN team_members tm ON tm.team_id = s.team_id
			WHERE s.id = $1 AND s.team_id = $2 AND tm.user_id = $3 AND s.deleted_at IS NULL
		)
	`, serviceID, teamID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (s *Server) emailExists(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM users WHERE email = $1 AND deleted_at IS NULL
		)
	`, strings.ToLower(strings.TrimSpace(email))).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func getClaims(r *http.Request) (*auth.Claims, bool) {
	claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
	if !ok || claims == nil {
		return nil, false
	}
	return claims, true
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}

	var deploymentsToday int
	var queueWaiting int
	var failed int
	var services int
	err := s.db.QueryRow(r.Context(), `
		SELECT
		  COUNT(*) FILTER (WHERE created_at::date = CURRENT_DATE) AS deployments_today,
		  COUNT(*) FILTER (WHERE status IN ('queued', 'building', 'deploying')) AS queue_waiting,
		  COUNT(*) FILTER (WHERE status = 'failed') AS failed_count
		FROM deployments
		WHERE team_id = $1
	`, claims.TeamID).Scan(&deploymentsToday, &queueWaiting, &failed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dashboard metrics")
		return
	}
	err = s.db.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM services WHERE team_id = $1 AND deleted_at IS NULL
	`, claims.TeamID).Scan(&services)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load services count")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"deploymentsToday": deploymentsToday,
		"queueWaiting":     queueWaiting,
		"failedBuilds":     failed,
		"services":         services,
	})
}

func (s *Server) handleRecentDeployments(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT d.id, d.status, COALESCE(d.ai_diagnosis, ''), COALESCE(d.ai_suggestion, ''), d.created_at,
		       COALESCE(d.commit_sha, ''), COALESCE(s.name, '')
		FROM deployments d
		JOIN services s ON s.id = d.service_id
		WHERE d.team_id = $1
		ORDER BY d.created_at DESC
		LIMIT 25
	`, claims.TeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load deployments")
		return
	}
	defer rows.Close()

	type deploymentRow struct {
		ID          string    `json:"id"`
		Status      string    `json:"status"`
		Diagnosis   string    `json:"diagnosis,omitempty"`
		Suggestion  string    `json:"suggestion,omitempty"`
		CreatedAt   time.Time `json:"createdAt"`
		CommitSHA   string    `json:"commitSha,omitempty"`
		ServiceName string    `json:"serviceName"`
	}

	items := make([]deploymentRow, 0, 25)
	for rows.Next() {
		var item deploymentRow
		if err := rows.Scan(&item.ID, &item.Status, &item.Diagnosis, &item.Suggestion, &item.CreatedAt, &item.CommitSHA, &item.ServiceName); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to parse deployments")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate deployments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDeploymentStream(w http.ResponseWriter, r *http.Request) {
	// Allow token via query param since EventSource can't set Authorization header
	tokenStr := r.URL.Query().Get("token")
	var claims *auth.Claims
	var ok bool
	if tokenStr != "" {
		var parseErr error
		claims, parseErr = s.signer.Parse(tokenStr)
		ok = parseErr == nil && claims != nil
	} else {
		claims, ok = getClaims(r)
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	deploymentID := chi.URLParam(r, "deploymentID")
	if deploymentID == "" {
		writeError(w, http.StatusBadRequest, "deploymentID is required")
		return
	}

	var allowed bool
	err := s.db.QueryRow(r.Context(), `
		SELECT EXISTS(
			SELECT 1 FROM deployments WHERE id = $1 AND team_id = $2
		)
	`, deploymentID, claims.TeamID).Scan(&allowed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate deployment access")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "deployment not found")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ctx := r.Context()
	lastID := int64(0)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	terminalStates := map[string]bool{"running": true, "failed": true, "cancelled": true, "superseded": true}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := s.db.Query(ctx, `
				SELECT id, stream, line, ts
				FROM deployment_logs
				WHERE deployment_id = $1 AND id > $2
				ORDER BY id ASC
			`, deploymentID, lastID)
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", marshalJSON(map[string]string{"error": "failed to read logs"}))
				flusher.Flush()
				return
			}

			for rows.Next() {
				var id int64
				var stream, line string
				var ts time.Time
				if err := rows.Scan(&id, &stream, &line, &ts); err != nil {
					rows.Close()
					return
				}
				lastID = id
				payload := map[string]any{
					"id":     id,
					"stream": stream,
					"line":   line,
					"ts":     ts.UTC().Format(time.RFC3339),
				}
				fmt.Fprintf(w, "event: log\ndata: %s\n\n", marshalJSON(payload))
				flusher.Flush()
			}
			rows.Close()

			// Check if deployment reached a terminal state
			var status string
			if qerr := s.db.QueryRow(ctx, `SELECT status FROM deployments WHERE id = $1`, deploymentID).Scan(&status); qerr == nil {
				if terminalStates[status] {
					fmt.Fprintf(w, "event: done\ndata: %s\n\n", marshalJSON(map[string]string{"status": status}))
					flusher.Flush()
					return
				}
			}
		}
	}
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT s.id, s.name, COALESCE(s.type::text, 'app'), COALESCE(s.docker_image, ''), COALESCE(s.git_repo_url, ''), COALESCE(s.git_branch, 'main')
		FROM services s
		WHERE s.team_id = $1 AND s.deleted_at IS NULL
		ORDER BY s.created_at DESC
	`, claims.TeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list services")
		return
	}
	defer rows.Close()

	type serviceItem struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		DockerImage string `json:"dockerImage"`
		GitRepoURL  string `json:"gitRepoUrl,omitempty"`
		GitBranch   string `json:"gitBranch,omitempty"`
	}

	services := make([]serviceItem, 0)
	for rows.Next() {
		var item serviceItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.DockerImage, &item.GitRepoURL, &item.GitBranch); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to parse services")
			return
		}
		services = append(services, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate services")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": services})
}

func (s *Server) handleEnsureLocalhostServer(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	ctx := r.Context()
	var existingID string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM servers
		WHERE team_id = $1 AND is_localhost = true AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, claims.TeamID).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": existingID, "created": false})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check localhost server")
		return
	}

	id := uuid.NewString()
	_, err = s.db.Exec(ctx, `
		INSERT INTO servers (id, team_id, name, host, port, ssh_user, ssh_key_ct, ssh_key_kid, status, is_localhost, docker_version)
		VALUES ($1::uuid, $2::uuid, 'localhost', '127.0.0.1', 22, 'local', $3, 'localhost', 'reachable', true, 'local-docker')
	`, id, claims.TeamID, []byte("localhost"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create localhost server")
		return
	}
	_, _ = s.db.Exec(ctx, `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id)
		VALUES ($1::uuid, 'server.localhost.create', 'server', $2::uuid, $3::uuid)
	`, claims.UserID, id, claims.TeamID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "created": true})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ServerID    string `json:"serverId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ServerID) == "" {
		writeError(w, http.StatusBadRequest, "name and serverId are required")
		return
	}

	var serverExists bool
	err := s.db.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1 AND team_id = $2 AND deleted_at IS NULL)
	`, req.ServerID, claims.TeamID).Scan(&serverExists)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate server")
		return
	}
	if !serverExists {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}

	projectID := uuid.NewString()
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO projects (id, team_id, server_id, name, description)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)
	`, projectID, claims.TeamID, req.ServerID, strings.TrimSpace(req.Name), nullableString(req.Description))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}
	_, _ = s.db.Exec(r.Context(), `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id)
		VALUES ($1::uuid, 'project.create', 'project', $2::uuid, $3::uuid)
	`, claims.UserID, projectID, claims.TeamID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": projectID})
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	var req struct {
		ProjectID   string `json:"projectId"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		DockerImage string `json:"dockerImage"`
		GitRepoURL  string `json:"gitRepoUrl"`
		GitBranch   string `json:"gitBranch"`
		Port        *int   `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.DockerImage) == "" {
		writeError(w, http.StatusBadRequest, "projectId, name and dockerImage are required")
		return
	}

	var projectExists bool
	err := s.db.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND team_id = $2 AND deleted_at IS NULL)
	`, req.ProjectID, claims.TeamID).Scan(&projectExists)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate project")
		return
	}
	if !projectExists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	serviceID := uuid.NewString()
	serviceType := strings.TrimSpace(req.Type)
	if serviceType == "" {
		serviceType = "app"
	}
	gitBranch := strings.TrimSpace(req.GitBranch)
	if gitBranch == "" {
		gitBranch = "main"
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO services (id, project_id, team_id, name, type, docker_image, git_repo_url, git_branch, port)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::service_type, $6, $7, $8, $9)
	`, serviceID, req.ProjectID, claims.TeamID, strings.TrimSpace(req.Name), serviceType, strings.TrimSpace(req.DockerImage), nullableString(req.GitRepoURL), gitBranch, req.Port)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create service")
		return
	}
	_, _ = s.db.Exec(r.Context(), `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id)
		VALUES ($1::uuid, 'service.create', 'service', $2::uuid, $3::uuid)
	`, claims.UserID, serviceID, claims.TeamID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": serviceID})
}

func (s *Server) handleUpsertAIProvider(w http.ResponseWriter, r *http.Request) {
	claims, ok := getClaims(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth claims")
		return
	}
	var req struct {
		ProviderType string `json:"providerType"`
		DisplayName  string `json:"displayName"`
		BaseURL      string `json:"baseUrl"`
		Model        string `json:"model"`
		APIKey       string `json:"apiKey"`
		Enabled      *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.ProviderType) == "" || strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, "providerType and model are required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	id := uuid.NewString()
	_, err := s.db.Exec(r.Context(), `
		INSERT INTO ai_provider_configs (id, team_id, provider_type, display_name, base_url, model, api_key_ct, api_key_kid, is_enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (team_id)
		DO UPDATE SET provider_type = EXCLUDED.provider_type,
		              display_name = EXCLUDED.display_name,
		              base_url = EXCLUDED.base_url,
		              model = EXCLUDED.model,
		              api_key_ct = EXCLUDED.api_key_ct,
		              api_key_kid = EXCLUDED.api_key_kid,
		              is_enabled = EXCLUDED.is_enabled,
		              updated_at = now()
	`, id, claims.TeamID, strings.TrimSpace(req.ProviderType), nonEmptyOrFallback(strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.ProviderType)), nullableString(req.BaseURL), strings.TrimSpace(req.Model), nullableBytes(strings.TrimSpace(req.APIKey)), keyIDOrNil(strings.TrimSpace(req.APIKey)), enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to upsert AI provider config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func nullableBytes(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return []byte(strings.TrimSpace(v))
}

func nonEmptyOrFallback(v string, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func keyIDOrNil(apiKey string) any {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	return "plain-v1"
}

func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(b)
}

func matchesRepo(candidates []string, serviceRepo string) bool {
	for _, c := range candidates {
		if c != "" && c == serviceRepo {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.TrimSpace(v)
}

func slugify(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	prevDash := false
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
