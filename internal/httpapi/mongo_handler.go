package httpapi

import (
	"errors"
	"fmt"
	"net/http"
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
	//
	// 鉴权粒度是「数据源 + 库」：ds:<id>:<库>:*。集合名不参与判定——
	// 有整库授权就能查该库下任何集合，与 SQL 侧一致。
	//
	// querysvc.ExecuteMongo 里也会做同一次库级判定。这里是**有意重复**的：
	// handler 必须先挡掉未授权请求，才不会替它去连目标库。两处用的是同一个
	// object 形态，不存在「谁先放开谁后收紧」的漂移风险；
	// 历史上重复出问题是因为当时一边按集合、一边按整库，形态不一致。
	uid, username, _ := currentUser(c)
	subject := SubjectOf(uid)

	// 集合名可能带库前缀（db.shop.users 解析出 "shop.users"），
	// 鉴权的库名要取拆分后的那一半，执行层仍用原始 collection——
	// 适配器会自己处理前缀，拆了反而改变实际查询的库。
	authzDB, _, err := querysvc.SplitDBCollection(collection, req.Database)
	if err != nil {
		// 拆分失败说明集合名异常（如 "shop." 结尾）。按参数错误返回，
		// 而不是退化成原值——退化成原值会让鉴权与执行用两套库名。
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid collection: " + err.Error()})
		return
	}
	if authzDB == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择数据库（database 字段必填）"})
		return
	}
	if !h.Service.Enforcer.Enforce(subject, fmt.Sprintf("ds:%d:%s:*", req.DatasourceID, authzDB), "read") {
		c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this database"})
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
			// 鉴权在库级完成，拒绝时说明也是库级
			c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this database"})
		case errors.Is(err, querysvc.ErrNoDatabase):
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择数据库（database 字段必填）"})
		case errors.Is(err, querysvc.ErrNoCollection):
			// 集合名不再是鉴权输入，而是执行目标：缺了就没法查，
			// 这与 SQL 里缺 SELECT 语句是同一类问题，不是权限问题。
			c.JSON(http.StatusBadRequest, gin.H{"error": "请指定集合名或命令（如 db.users.find({})）"})
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
