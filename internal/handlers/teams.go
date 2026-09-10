package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// TeamRequest represents create/update team request
type TeamRequest struct {
	Name                string                    `json:"name" validate:"required"`
	Description         string                    `json:"description"`
	AssignmentStrategy  models.AssignmentStrategy `json:"assignment_strategy"` // round_robin, load_balanced, manual
	PerAgentTimeoutSecs int                       `json:"per_agent_timeout_secs"`
	IsActive            bool                      `json:"is_active"`
}

// TeamMemberRequest represents add member request
type TeamMemberRequest struct {
	UserID string          `json:"user_id" validate:"required"`
	Role   models.TeamRole `json:"role"` // manager, agent
}

// TeamResponse represents team in API response
type TeamResponse struct {
	ID                  uuid.UUID                 `json:"id"`
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	AssignmentStrategy  models.AssignmentStrategy `json:"assignment_strategy"`
	PerAgentTimeoutSecs int                       `json:"per_agent_timeout_secs"`
	IsActive            bool                      `json:"is_active"`
	MemberCount         int                       `json:"member_count"`
	Members             []TeamMemberResponse      `json:"members,omitempty"`
	CreatedByID         *uuid.UUID                `json:"created_by_id,omitempty"`
	CreatedByName       string                    `json:"created_by_name,omitempty"`
	UpdatedByID         *uuid.UUID                `json:"updated_by_id,omitempty"`
	UpdatedByName       string                    `json:"updated_by_name,omitempty"`
	CreatedAt           time.Time                 `json:"created_at"`
	UpdatedAt           time.Time                 `json:"updated_at"`
}

// TeamMemberResponse represents team member in API response
type TeamMemberResponse struct {
	ID             uuid.UUID       `json:"id"`
	UserID         uuid.UUID       `json:"user_id"`
	FullName       string          `json:"full_name"`
	Email          string          `json:"email"`
	Role           models.TeamRole `json:"role"` // manager, agent
	IsAvailable    bool            `json:"is_available"`
	LastAssignedAt *time.Time      `json:"last_assigned_at,omitempty"`
}

// ListTeams returns teams based on user access
func (a *App) ListTeams(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	pg := parsePaginationHTTP(r)
	search := r.URL.Query().Get("search")
	var teams []models.Team
	var total int64

	// Users with teams:read permission can see all teams, others see only their teams
	if a.HasPermission(userID, models.ResourceTeams, models.ActionRead, orgID) {
		baseQuery := a.ScopeToOrg(a.DB, userID, orgID)
		if search != "" {
			baseQuery = baseQuery.Where("name ILIKE ?", "%"+search+"%")
		}
		baseQuery.Model(&models.Team{}).Count(&total)
		if err := pg.Apply(baseQuery.
			Preload("Members").Preload("Members.User").
			Order("name ASC")).Find(&teams).Error; err != nil {
			a.Log.Error("Failed to list teams", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list teams", nil, "")
			return
		}
	} else {
		// Users only see teams they belong to
		baseQuery := a.ScopeToOrg(a.DB.Joins("JOIN team_members ON team_members.team_id = teams.id"), userID, orgID).
			Where("team_members.user_id = ?", userID)
		if search != "" {
			baseQuery = baseQuery.Where("teams.name ILIKE ?", "%"+search+"%")
		}
		baseQuery.Model(&models.Team{}).Count(&total)
		if err := pg.Apply(baseQuery.
			Preload("Members").Preload("Members.User").
			Order("teams.name ASC")).Find(&teams).Error; err != nil {
			a.Log.Error("Failed to list teams", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list teams", nil, "")
			return
		}
	}

	// Build response
	response := make([]TeamResponse, len(teams))
	for i, t := range teams {
		response[i] = buildTeamResponse(&t, false)
	}

	SendEnvelope(w, listEnvelope("teams", response, total, pg))
	return
}

// GetTeam returns a single team with members
func (a *App) GetTeam(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	teamID, err := parsePathUUIDHTTP(w, r, "id", "team")
	if err != nil {
		return
	}

	var team models.Team
	if err := a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).
		Preload("Members").Preload("Members.User").
		Preload("CreatedBy").Preload("UpdatedBy").
		First(&team).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	// Check access: users with teams:read permission can see all teams, otherwise must be a member
	if !a.HasPermission(userID, models.ResourceTeams, models.ActionRead, orgID) {
		hasAccess := false
		for _, m := range team.Members {
			if m.UserID == userID {
				hasAccess = true
				break
			}
		}
		if !hasAccess {
			SendErrorEnvelope(w, http.StatusForbidden, "Access denied", nil, "")
			return
		}
	}

	SendEnvelope(w, map[string]any{"team": buildTeamResponse(&team, true)})
	return
}

// CreateTeam creates a new team
func (a *App) CreateTeam(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceTeams, models.ActionWrite)
	if err != nil {
		return
	}

	var req TeamRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.Name == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "Team name is required", nil, "")
		return
	}

	// Validate assignment strategy
	strategy := req.AssignmentStrategy
	if strategy == "" {
		strategy = models.AssignmentStrategyRoundRobin
	}
	if strategy != models.AssignmentStrategyRoundRobin && strategy != models.AssignmentStrategyLoadBalanced && strategy != models.AssignmentStrategyManual {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid assignment strategy", nil, "")
		return
	}

	team := models.Team{
		OrganizationID:      orgID,
		Name:                req.Name,
		Description:         req.Description,
		AssignmentStrategy:  strategy,
		PerAgentTimeoutSecs: req.PerAgentTimeoutSecs,
		IsActive:            true,
		CreatedByID:         &userID,
		UpdatedByID:         &userID,
	}

	if err := a.DB.Create(&team).Error; err != nil {
		a.Log.Error("Failed to create team", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create team", nil, "")
		return
	}

	// Preload relations for response
	a.DB.Preload("CreatedBy").Preload("UpdatedBy").First(&team, "id = ?", team.ID)

	a.logAudit(orgID, userID,
		"team", team.ID, models.AuditActionCreated, nil, &team)

	SendEnvelope(w, map[string]any{"team": buildTeamResponse(&team, false)})
	return
}

// UpdateTeam updates a team
func (a *App) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	teamID, err := parsePathUUIDHTTP(w, r, "id", "team")
	if err != nil {
		return
	}

	var team models.Team
	if err := a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).
		Preload("Members").First(&team).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	oldTeam := team // value copy for audit diff

	// Check access: users with teams:write permission OR team managers can update
	if !a.HasPermission(userID, models.ResourceTeams, models.ActionWrite, orgID) {
		isManager := false
		for _, m := range team.Members {
			if m.UserID == userID && m.Role == models.TeamRoleManager {
				isManager = true
				break
			}
		}
		if !isManager {
			SendErrorEnvelope(w, http.StatusForbidden, "Insufficient permissions", nil, "")
			return
		}
	}

	var req TeamRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Update fields
	if req.Name != "" {
		team.Name = req.Name
	}
	team.Description = req.Description
	team.IsActive = req.IsActive

	if req.AssignmentStrategy != "" {
		if req.AssignmentStrategy != models.AssignmentStrategyRoundRobin && req.AssignmentStrategy != models.AssignmentStrategyLoadBalanced && req.AssignmentStrategy != models.AssignmentStrategyManual {
			SendErrorEnvelope(w, http.StatusBadRequest, "Invalid assignment strategy", nil, "")
			return
		}
		team.AssignmentStrategy = req.AssignmentStrategy
	}
	team.PerAgentTimeoutSecs = req.PerAgentTimeoutSecs
	team.UpdatedByID = &userID

	if err := a.DB.Save(&team).Error; err != nil {
		a.Log.Error("Failed to update team", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update team", nil, "")
		return
	}

	if a.Assigner != nil {
		a.Assigner.InvalidateTeamCache(teamID)
	}

	// Preload relations for response
	a.DB.Preload("CreatedBy").Preload("UpdatedBy").Preload("Members").Preload("Members.User").First(&team, "id = ?", team.ID)

	a.logAudit(orgID, userID,
		"team", team.ID, models.AuditActionUpdated, &oldTeam, &team)

	SendEnvelope(w, map[string]any{"team": buildTeamResponse(&team, false)})
	return
}

// DeleteTeam deletes a team
func (a *App) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceTeams, models.ActionDelete)
	if err != nil {
		return
	}

	teamID, err := parsePathUUIDHTTP(w, r, "id", "team")
	if err != nil {
		return
	}

	// Load team for audit log before deleting
	var teamForAudit models.Team
	a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).First(&teamForAudit)

	// Delete team members first
	if err := a.DB.Where("team_id = ?", teamID).Delete(&models.TeamMember{}).Error; err != nil {
		a.Log.Error("Failed to delete team members", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete team", nil, "")
		return
	}

	// Delete team
	result := a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).Delete(&models.Team{})
	if result.Error != nil {
		a.Log.Error("Failed to delete team", "error", result.Error)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete team", nil, "")
		return
	}

	if result.RowsAffected == 0 {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	if a.Assigner != nil {
		a.Assigner.InvalidateTeamCache(teamID)
	}

	a.logAudit(orgID, userID,
		"team", teamID, models.AuditActionDeleted, &teamForAudit, nil)

	SendEnvelope(w, map[string]string{"message": "Team deleted"})
	return
}

// ListTeamMembers lists members of a team
func (a *App) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	teamID, err := parsePathUUIDHTTP(w, r, "id", "team")
	if err != nil {
		return
	}

	// Verify team exists and user has access
	var team models.Team
	if err := a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).
		Preload("Members").Preload("Members.User").
		First(&team).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	// Check access: users with teams:read permission can see all, otherwise must be a member
	if !a.HasPermission(userID, models.ResourceTeams, models.ActionRead, orgID) {
		hasAccess := false
		for _, m := range team.Members {
			if m.UserID == userID {
				hasAccess = true
				break
			}
		}
		if !hasAccess {
			SendErrorEnvelope(w, http.StatusForbidden, "Access denied", nil, "")
			return
		}
	}

	members := make([]TeamMemberResponse, len(team.Members))
	for i, m := range team.Members {
		members[i] = TeamMemberResponse{
			ID:             m.ID,
			UserID:         m.UserID,
			FullName:       m.User.FullName,
			Email:          m.User.Email,
			Role:           m.Role,
			IsAvailable:    m.User.IsAvailable,
			LastAssignedAt: m.LastAssignedAt,
		}
	}

	SendEnvelope(w, map[string]any{"members": members})
	return
}

// AddTeamMember adds a member to a team
func (a *App) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	teamID, err := parsePathUUIDHTTP(w, r, "id", "team")
	if err != nil {
		return
	}

	// Verify team exists
	var team models.Team
	if err := a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).
		Preload("Members").First(&team).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	hasWritePermission := a.HasPermission(userID, models.ResourceTeams, models.ActionWrite, orgID)

	// Check access: users with teams:write permission OR team managers can add members
	if !hasWritePermission {
		isManager := false
		for _, m := range team.Members {
			if m.UserID == userID && m.Role == models.TeamRoleManager {
				isManager = true
				break
			}
		}
		if !isManager {
			SendErrorEnvelope(w, http.StatusForbidden, "Insufficient permissions", nil, "")
			return
		}
	}

	var req TeamMemberRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	memberUserID, err := uuid.Parse(req.UserID)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid user ID", nil, "")
		return
	}

	// Verify user exists in org
	user, err := findByIDAndOrgHTTP[models.User](a.DB, w, memberUserID, orgID, "User")
	if err != nil {
		return
	}

	// Check if already a member
	var existingMember models.TeamMember
	if err := a.DB.Where("team_id = ? AND user_id = ?", teamID, memberUserID).First(&existingMember).Error; err == nil {
		SendErrorEnvelope(w, http.StatusConflict, "User is already a member of this team", nil, "")
		return
	}

	// Validate role
	role := req.Role
	if role == "" {
		role = models.TeamRoleAgent
	}
	if role != models.TeamRoleManager && role != models.TeamRoleAgent {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid role. Must be 'manager' or 'agent'", nil, "")
		return
	}

	// Only users with teams:write permission can add managers
	if !hasWritePermission && role == models.TeamRoleManager {
		SendErrorEnvelope(w, http.StatusForbidden, "Insufficient permissions to add managers", nil, "")
		return
	}

	member := models.TeamMember{
		TeamID: teamID,
		UserID: memberUserID,
		Role:   role,
	}

	if err := a.DB.Create(&member).Error; err != nil {
		a.Log.Error("Failed to add team member", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to add member", nil, "")
		return
	}

	if a.Assigner != nil {
		a.Assigner.InvalidateTeamCache(teamID)
	}

	SendEnvelope(w, map[string]any{"member": TeamMemberResponse{
		ID:          member.ID,
		UserID:      member.UserID,
		FullName:    user.FullName,
		Email:       user.Email,
		Role:        member.Role,
		IsAvailable: user.IsAvailable,
	}})
	return
}

// RemoveTeamMember removes a member from a team
func (a *App) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	teamID, err := parsePathUUIDHTTP(w, r, "id", "team")
	if err != nil {
		return
	}

	memberUserID, err := parsePathUUIDHTTP(w, r, "member_user_id", "user")
	if err != nil {
		return
	}

	// Verify team exists
	var team models.Team
	if err := a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).
		Preload("Members").First(&team).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	hasWritePermission := a.HasPermission(userID, models.ResourceTeams, models.ActionWrite, orgID)

	// Check access: users with teams:write permission OR team managers can remove members
	if !hasWritePermission {
		isManager := false
		for _, m := range team.Members {
			if m.UserID == userID && m.Role == models.TeamRoleManager {
				isManager = true
				break
			}
		}
		if !isManager {
			SendErrorEnvelope(w, http.StatusForbidden, "Insufficient permissions", nil, "")
			return
		}

		// Team managers cannot remove other managers
		for _, m := range team.Members {
			if m.UserID == memberUserID && m.Role == models.TeamRoleManager {
				SendErrorEnvelope(w, http.StatusForbidden, "Insufficient permissions to remove managers", nil, "")
				return
			}
		}
	}

	result := a.DB.Where("team_id = ? AND user_id = ?", teamID, memberUserID).Delete(&models.TeamMember{})
	if result.Error != nil {
		a.Log.Error("Failed to remove team member", "error", result.Error)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to remove member", nil, "")
		return
	}

	if result.RowsAffected == 0 {
		SendErrorEnvelope(w, http.StatusNotFound, "Member not found in team", nil, "")
		return
	}

	if a.Assigner != nil {
		a.Assigner.InvalidateTeamCache(teamID)
	}

	SendEnvelope(w, map[string]string{"message": "Member removed from team"})
	return
}

// Helper function to build team response
func buildTeamResponse(team *models.Team, includeMembers bool) TeamResponse {
	resp := TeamResponse{
		ID:                  team.ID,
		Name:                team.Name,
		Description:         team.Description,
		AssignmentStrategy:  team.AssignmentStrategy,
		PerAgentTimeoutSecs: team.PerAgentTimeoutSecs,
		IsActive:            team.IsActive,
		MemberCount:         len(team.Members),
		CreatedByID:         team.CreatedByID,
		UpdatedByID:         team.UpdatedByID,
		CreatedAt:           team.CreatedAt,
		UpdatedAt:           team.UpdatedAt,
	}
	if team.CreatedBy != nil {
		resp.CreatedByName = team.CreatedBy.FullName
	}
	if team.UpdatedBy != nil {
		resp.UpdatedByName = team.UpdatedBy.FullName
	}

	if includeMembers && len(team.Members) > 0 {
		resp.Members = make([]TeamMemberResponse, len(team.Members))
		for i, m := range team.Members {
			resp.Members[i] = TeamMemberResponse{
				ID:             m.ID,
				UserID:         m.UserID,
				Role:           m.Role,
				LastAssignedAt: m.LastAssignedAt,
			}
			if m.User != nil {
				resp.Members[i].FullName = m.User.FullName
				resp.Members[i].Email = m.User.Email
				resp.Members[i].IsAvailable = m.User.IsAvailable
			}
		}
	}

	return resp
}
