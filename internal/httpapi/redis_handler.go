package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/querysvc"
	"github.com/opensourceprj/datawarden/internal/redisq"
)

// RedisTarget 描述一次 Redis 查询的目标，由工厂根据数据源配置解析。
type RedisTarget struct {
	Query    redisq.Queryer
	Database int // db 号，用于鉴权 object 拼接
}

// RedisTargetFactory 按数据源 ID 解析出可用的 Redis 连接与 db 号。
type RedisTargetFactory func(ctx context.Context, datasourceID int64) (*RedisTarget, error)

type RedisHandler struct {
	// Enforcer 做 key 级鉴权，与 SQL 的表级鉴权对应
	Enforcer querysvc.Enforcer
	Factory  RedisTargetFactory
}

type redisQueryReq struct {
	DatasourceID int64  `json:"datasource_id" binding:"required"`
	Key          string `json:"key"`
	Pattern      string `json:"pattern"`
	RowLimit     int    `json:"row_limit"`
}

// Query 执行 Redis 查询。
//
// 鉴权粒度：Redis 没有表的概念，用 ds:<id>:<db>:<key/pattern> 作为 object。
// 由于授权通常给的是某个前缀（如 user:1001），而实际 key 会比它长
// （user:1001:name），因此鉴权时逐级向上取候选，任意一个命中即放行。
//
// db 段固定写数据源自己配置的 db 号，而新口径下申请通过拿到的是
// 整数据源授权（ds:<id>:*:*）。这两者要对上，只能靠候选里那一项
// "*"——它拼出的对象是 ds:<id>:<db>:*，覆盖任意 db 下的任意 key。
// 因此 KeyCandidates 末尾的 "*" 不能去掉，否则整源授权对 Redis
// 完全无效（SQL / Mongo 走的是 ds:<id>:<库>:*，不受影响，症状会显得
// 很奇怪：「别的数据源能用，Redis 一律 403」）。
func (h *RedisHandler) Query(c *gin.Context) {
	if h.Factory == nil || h.Enforcer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis query not configured"})
		return
	}

	var req redisQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	subject := req.Key
	if subject == "" {
		subject = req.Pattern
	}
	if subject == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "either key or pattern is required"})
		return
	}

	uid, username, _ := currentUser(c)

	target, err := h.Factory(c.Request.Context(), req.DatasourceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user := SubjectOf(uid)
	if !h.EnforceKey(user, req.DatasourceID, target.Database, subject) {
		auditEvent(c, uid, username, "redis.denied", req.DatasourceID, map[string]any{
			"key":     req.Key,
			"pattern": req.Pattern,
		})
		c.JSON(http.StatusForbidden, gin.H{"error": "no permission for this redis key or pattern"})
		return
	}

	res, err := target.Query.Query(c.Request.Context(), redisq.QueryRequest{
		Key:      req.Key,
		Pattern:  req.Pattern,
		RowLimit: req.RowLimit,
	})
	if err != nil {
		auditEvent(c, uid, username, "redis.failed", req.DatasourceID, map[string]any{
			"key":     req.Key,
			"pattern": req.Pattern,
			"error":   err.Error(),
		})
		code := http.StatusInternalServerError
		if errors.Is(err, redisq.ErrPatternTooBroad) {
			code = http.StatusBadRequest
		}
		c.JSON(code, gin.H{"error": err.Error()})
		return
	}

	auditEvent(c, uid, username, "redis.execute", req.DatasourceID, map[string]any{
		"key":       req.Key,
		"pattern":   req.Pattern,
		"row_count": len(res.Items),
		"truncated": res.Truncated,
	})
	c.JSON(http.StatusOK, res)
}

// SubjectOf 把用户 ID 转成 Casbin 的 subject 形式。
func SubjectOf(uid int64) string {
	return "u:" + strconv.FormatInt(uid, 10)
}

// EnforceKey 判断用户是否有权访问给定的 key 或 pattern。
// 候选对象由具体到宽泛依次尝试，任意一个命中即放行。
func (h *RedisHandler) EnforceKey(user string, dsID int64, db int, subject string) bool {
	base := fmt.Sprintf("ds:%d:%d", dsID, db)
	for _, cand := range KeyCandidates(subject) {
		if h.Enforcer.Enforce(user, base+":"+cand, "read") {
			return true
		}
	}
	return false
}

// KeyCandidates 生成 key 的授权候选对象（由具体到宽泛）。
//
// 对 "user:1001:name" 依次返回：
//
//	user:1001:name          精确 key
//	user:1001:*             该前缀下的所有 key
//	user:1001
//	user:*
//	user
//	*                       整个 db（授权 ds:<id>:<db>:*）
//
// 最后那一项 "*" 是整数据源授权（ds:<id>:*:*）对 Redis 生效的唯一途径：
// 它拼出 ds:<id>:<db>:*，命中后即放行。删掉它，整源授权的用户
// 查 Redis 会一律 403。
//
// 若 subject 本身含通配符（如 "order:1:*"），先截取其字面前缀再展开，
// 避免把 * 当成层级的一部分。
func KeyCandidates(subject string) []string {
	if i := strings.IndexAny(subject, "*?["); i >= 0 {
		subject = strings.TrimSuffix(subject[:i], ":")
	}
	if subject == "" {
		return []string{"*"}
	}

	out := []string{subject}
	parts := strings.Split(subject, ":")
	for i := len(parts) - 1; i > 0; i-- {
		prefix := strings.Join(parts[:i], ":")
		out = append(out, prefix+":*", prefix)
	}
	out = append(out, "*")
	return out
}
