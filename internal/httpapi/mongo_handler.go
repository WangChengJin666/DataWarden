package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/querysvc"
)

// mongoQueryRequest 支持两种输入形式：
//
//  1. 结构化：{"collection":"users","filter":"{\"status\":\"active\"}"}
//  2. 命令行：{"command":"db.users.find({\"status\":\"active\"})"}
//
// 两者都最终归结为 collection + op + args。
type mongoQueryRequest struct {
	DatasourceID int64  `json:"datasource_id" binding:"required"`
	Database     string `json:"database"`
	// 形式一：集合 + 过滤条件
	Collection string `json:"collection"`
	Filter     string `json:"filter"`
	// 形式二：原生命令
	Command  string `json:"command"`
	RowLimit int    `json:"row_limit"`
}

// ExecuteMongo 执行受控 MongoDB 查询。
func (h *QueryHandler) ExecuteMongo(c *gin.Context) {
	if h.Service == nil || h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "query service not configured"})
		return
	}

	var req mongoQueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	collection, op, args, ok := resolveMongoInput(c, req)
	if !ok {
		return
	}

	// 先鉴权，再取连接。
	// 反过来的话，任何未授权用户都能触发一次目标库连接：
	// 既浪费连接资源，也让人可以借报错信息探测目标库是否存在。
	uid, username, _ := currentUser(c)
	subject := "u:" + strconv.FormatInt(uid, 10)

	// 集合名可能带库前缀（db.shop.users 形式解析出 "shop.users"）。
	// 鉴权必须用拆分后的 (库, 集合)，否则 ds:<id>:<库>:<集合> 会少一段，
	// keyMatch 的匹配范围不对。执行层仍然用原始 collection——
	// 适配器会自己处理前缀，拆了反而改变实际查询的库。
	//
	// 这里与 querysvc.ExecuteMongo 内部那道校验走同一个函数，
	// 不再维护两份实现（两份实现就等于两条会各自漂移的规则）。
	authzDB, authzColl, err := querysvc.SplitDBCollection(collection, req.Database)
	if err != nil {
		// 拆分失败说明集合名异常（如 "shop." 结尾）。按参数错误返回，
		// 而不是退化成原值——退化成原值会让鉴权与执行用两套库名。
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid collection: " + err.Error()})
		return
	}
	obj := fmt.Sprintf("ds:%d:%s:%s", req.DatasourceID, authzDB, authzColl)
	if !h.Service.Enforcer.Enforce(subject, obj, "read") {
		c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this collection"})
		return
	}

	ad, err := h.Factory(req.DatasourceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res, err := h.Service.ExecuteMongo(c.Request.Context(), querysvc.MongoRequest{
		Subject:      subject,
		ActorID:      uid,
		ActorName:    username,
		DatasourceID: req.DatasourceID,
		Database:     req.Database,
		Collection:   collection,
		Op:           op,
		Args:         args,
		RowLimit:     req.RowLimit,
		IP:           c.ClientIP(),
		UserAgent:    c.Request.UserAgent(),
	}, ad)

	if err != nil {
		switch {
		case errors.Is(err, querysvc.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this collection"})
		case errors.Is(err, querysvc.ErrNoTables):
			c.JSON(http.StatusBadRequest, gin.H{"error": "collection field is required for authorization"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, res)
}

// resolveMongoInput 把两种输入形式归一成 collection/op/args。
// 出错时已写入响应，返回 false。
func resolveMongoInput(c *gin.Context, req mongoQueryRequest) (collection, op, args string, ok bool) {
	cmd := strings.TrimSpace(req.Command)

	// 形式二优先：写了命令行就按命令行解析，避免与集合字段混用产生歧义
	if cmd != "" {
		q, err := adapter.ParseMongoQuery(cmd)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return "", "", "", false
		}
		return q.Collection, q.Op, q.Args, true
	}

	// 形式一：集合 + 过滤条件
	collection = strings.TrimSpace(req.Collection)
	if collection == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "collection field is required"})
		return "", "", "", false
	}

	filter := strings.TrimSpace(req.Filter)
	if filter == "" {
		filter = "{}"
	}
	// 只允许对象字面量，不接受把任意语句塞进 filter
	if !strings.HasPrefix(filter, "{") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "filter must be a JSON object"})
		return "", "", "", false
	}

	return collection, "find", "(" + filter + ")", true
}
