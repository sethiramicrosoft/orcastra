package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sethiramicrosoft/orcastra/internal/auth"
)

type Server struct {
	cfg    Config
	db     *pgxpool.Pool
	signer *auth.JWTSigner
}

func NewServer(cfg Config, db *pgxpool.Pool) *Server {
	signer, err := auth.NewJWTSigner(cfg.JWTSecret, cfg.JWTIssuer)
	if err != nil {
		panic(err)
	}
	return &Server{
		cfg:    cfg,
		db:     db,
		signer: signer,
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)
	r.Post("/api/v1/auth/register", s.handleRegister)
	r.Post("/api/v1/auth/login", s.handleLogin)
	r.With(s.requireAuth).Get("/api/v1/auth/me", s.handleMe)
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
	`, userID, req.Email, passwordHash, nullableString(req.DisplayName))
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
	`, userID, teamID, req.Email)
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

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
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
		userID      string
		email       string
		displayName string
		passwordHash string
		teamID      string
		teamName    string
		teamRole    string
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
	out := strings.Trim(b.String(), "-")
	return out
}
