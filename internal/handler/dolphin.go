package handler

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// DolphinHandler hosts cross-workspace admin endpoints mounted under
// /dolphin. These are control-plane style APIs aimed at operators: the
// caller authenticates with a platform (admin) API key created via
// /system/admin/api-keys, identifies a target user by email, and reads
// data on that user's behalf across every workspace they belong to.
//
// Because the endpoints are not bound to the caller's own workspace,
// they deliberately do NOT read tenant/user from the auth context: the
// target user is resolved from the request parameter, and the caller's
// only authority is the platform API-key capability policy registered
// in the router (system_tenants_read / system_tenants_manage).
type DolphinHandler struct {
	userService     interfaces.UserService
	tenantMemberSvc interfaces.TenantMemberService
	tenantService   interfaces.TenantService
	kbService       interfaces.KnowledgeBaseService
	kbShareService  interfaces.KBShareService
}

// NewDolphinHandler creates a new dolphin handler.
func NewDolphinHandler(
	userService interfaces.UserService,
	tenantMemberSvc interfaces.TenantMemberService,
	tenantService interfaces.TenantService,
	kbService interfaces.KnowledgeBaseService,
	kbShareService interfaces.KBShareService,
) *DolphinHandler {
	return &DolphinHandler{
		userService:     userService,
		tenantMemberSvc: tenantMemberSvc,
		tenantService:   tenantService,
		kbService:       kbService,
		kbShareService:  kbShareService,
	}
}

// resolveUserByEmail looks up a user by email, tolerating case
// differences in the input. Returns (nil, nil) when no user matches.
func (h *DolphinHandler) resolveUserByEmail(ctx context.Context, email string) (*types.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	user, err := h.userService.GetUserByEmail(ctx, email)
	if err == nil && user != nil {
		return user, nil
	}
	if err != nil && !stderrors.Is(err, repository.ErrUserNotFound) {
		return nil, err
	}
	// Exact match missed; retry with a lowercased form. Emails are
	// conventionally case-insensitive even though registration stores
	// whatever the client sent.
	lower := strings.ToLower(email)
	if lower == email {
		return nil, nil
	}
	user, err = h.userService.GetUserByEmail(ctx, lower)
	if err != nil {
		if stderrors.Is(err, repository.ErrUserNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}

// ListUserKnowledgeBases godoc
// @Summary      获取指定用户的所有知识库（含共享）
// @Description  使用平台 admin API key 鉴权，通过用户 email 查询该用户在所有工作区下拥有的知识库，
//
//	以及共享给该用户所在工作区的知识库。跨工作区控制面接口，不依赖调用者自身的
//	X-Tenant-ID / JWT 上下文。
//
// @Tags         Dolphin
// @Produce      json
// @Param        email query string false "用户 email（与 path 二选一）"
// @Param        email path string false "用户 email（与 query 二选一）"
// @Success      200 {object} map[string]interface{} "用户及其知识库列表"
// @Failure      400 {object} errors.AppError "缺少 email 参数"
// @Failure      404 {object} errors.AppError "用户不存在"
// @Failure      500 {object} errors.AppError "服务器错误"
// @Security     ApiKeyAuth
// @Router       /dolphin/knowledge-bases [get]
func (h *DolphinHandler) ListUserKnowledgeBases(c *gin.Context) {
	ctx := c.Request.Context()

	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		email = strings.TrimSpace(c.Param("email"))
	}
	if email == "" {
		c.Error(apperrors.NewBadRequestError("email is required (query or path)"))
		return
	}

	user, err := h.resolveUserByEmail(ctx, email)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{
			"email": sanitizeForLog(email),
		})
		c.Error(apperrors.NewInternalServerError("failed to look up user by email"))
		return
	}
	if user == nil {
		c.Error(apperrors.NewNotFoundError("user not found for email"))
		return
	}

	// Collect the workspaces the user belongs to: every active
	// membership, plus the home tenant as a legacy fallback for rows
	// created before memberships were backfilled.
	tenantIDs := make([]uint64, 0)
	roleByTenant := make(map[uint64]types.TenantRole)
	memberships, err := h.tenantMemberSvc.ListByUser(ctx, user.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{"user_id": user.ID})
		c.Error(apperrors.NewInternalServerError("failed to list user memberships"))
		return
	}
	for _, m := range memberships {
		if m == nil || m.TenantID == 0 || m.Status != types.TenantMemberStatusActive {
			continue
		}
		if _, seen := roleByTenant[m.TenantID]; !seen {
			tenantIDs = append(tenantIDs, m.TenantID)
			roleByTenant[m.TenantID] = m.Role
		}
	}
	if user.TenantID > 0 {
		if _, seen := roleByTenant[user.TenantID]; !seen {
			tenantIDs = append(tenantIDs, user.TenantID)
			roleByTenant[user.TenantID] = types.TenantRoleOwner
		}
	}

	entries := make([]gin.H, 0, 32)
	seenKB := make(map[string]bool)

	for _, tenantID := range tenantIDs {
		tenantName := ""
		if t, terr := h.tenantService.GetTenantByID(ctx, tenantID); terr == nil && t != nil {
			tenantName = t.Name
		}

		// 1) Knowledge bases the user created in this workspace.
		kbs, kerr := h.kbService.ListKnowledgeBasesByTenantID(ctx, tenantID)
		if kerr != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"user_id":   user.ID,
				"tenant_id": tenantID,
			}, "[dolphin] list own KBs failed; skipping tenant")
			kbs = nil
		}
		own := make([]*types.KnowledgeBase, 0, len(kbs))
		for _, kb := range kbs {
			if kb == nil || kb.DeletedAt.Valid || kb.CreatorID != user.ID {
				continue
			}
			if seenKB[kb.ID] {
				continue
			}
			seenKB[kb.ID] = true
			own = append(own, kb)
		}
		enrichKBCreatorNames(ctx, h.userService, own)
		for _, kb := range own {
			entries = append(entries, gin.H{
				"source":         "own",
				"tenant_id":      tenantID,
				"tenant_name":    tenantName,
				"permission":     string(roleByTenant[tenantID]),
				"knowledge_base": buildKBResponse(kb, types.DefaultStoreDisplay(), nil),
			})
		}

		// 2) Knowledge bases shared into this workspace (org-shared,
		//    cross-tenant). ListSharedKnowledgeBases already skips
		//    same-workspace shares and applies the 3-D permission cap
		//    using the user's role in the workspace.
		if h.kbShareService == nil {
			continue
		}
		shared, serr := h.kbShareService.ListSharedKnowledgeBases(ctx, tenantID, roleByTenant[tenantID])
		if serr != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"user_id":   user.ID,
				"tenant_id": tenantID,
				"error":     serr.Error(),
			}, "[dolphin] list shared KBs failed; skipping tenant")
			continue
		}
		for _, info := range shared {
			if info == nil || info.KnowledgeBase == nil {
				continue
			}
			if seenKB[info.KnowledgeBase.ID] {
				continue
			}
			seenKB[info.KnowledgeBase.ID] = true
			row := sharedKBRow(info, nil)
			row["source"] = "shared"
			row["tenant_id"] = tenantID
			row["tenant_name"] = tenantName
			entries = append(entries, row)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"user": gin.H{
				"id":       user.ID,
				"email":    user.Email,
				"username": user.Username,
			},
			"knowledge_bases": entries,
			"total":           len(entries),
		},
	})
}

// sanitizeForLog trims and lowercases an email for safe logging.
func sanitizeForLog(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
