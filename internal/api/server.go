package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	r.With(s.requireAuth).Post("/api/v1/services/{serviceID}/deploy", s.handleManualDeploy)
	r.Post("/api/v1/webhooks/github", s.handleGitHubWebhook)

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
		VALUES ($1, $2, $3, $4, false)
	`, userID, strings.ToLower(strings.TrimSpace(req.Email)), passwordHash, nullableString(req.DisplayName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO teams (id, name, slug)
		VALUES ($1, $2, $3)
	`, teamID, req.TeamName, teamSlug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create team")
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, teamID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link user to team")
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, team_id, meta)
		VALUES ($1, 'auth.register', 'user', $1, $2, jsonb_build_object('email', $3))
	`, userID, teamID, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil {
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
		VALUES ($1, 'auth.login', 'user', $1, $2)
	`, userID, teamID)
	if err != nil {
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
		VALUES ($1, 'service.deploy.manual', 'deployment', $2, $3, jsonb_build_object('serviceId', $4, 'commitSha', $5))
	`, claims.UserID, dep.ID, claims.TeamID, serviceID, nullableString(req.CommitSHA))
	if err != nil {
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
