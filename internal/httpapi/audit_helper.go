package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/opensourceprj/datawarden/internal/querysvc"
	"github.com/opensourceprj/datawarden/internal/store"
)

// EventRecorder 是 HTTP 层写审计的最小接口，复用 querysvc.Auditor。
type EventRecorder interface {
	Record(ctx context.Context, e querysvc.AuditEvent) error
}

// auditDeps 存放审计依赖，由路由装配时注入。
// 用包级变量是为了避免每个 handler 都多一个字段；
// 只在 NewRouter 里赋值一次，运行期只读。
var auditDeps EventRecorder

// SetAuditor 由 NewRouter 调用，注入审计实现。
func SetAuditor(a EventRecorder) { auditDeps = a }

// auditEvent 记录一次业务审计。审计失败不阻断请求，只影响可观测性。
func auditEvent(c *gin.Context, actorID int64, actorName, action string, datasourceID int64, detail map[string]any) {
	if auditDeps == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	object := "system"
	if datasourceID > 0 {
		object = "datasource:" + itoaInt64(datasourceID)
	}
	_ = auditDeps.Record(c.Request.Context(), querysvc.AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		Action:    action,
		Object:    object,
		Detail:    detail,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
}

func itoaInt64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// parseID 从路径参数取正整数 ID。失败时已写入 400 响应。
func parseID(c *gin.Context, name string) (int64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return id, true
}

// parseQueryID 解析查询参数里的正整数 id。失败时已写入 400 响应。
//
// 与 parseID 分开：parseID 只认路径参数（名字固定），查询参数需要
// 自带原始字符串，而且错误提示要带上参数名，否则前端看到
// 「invalid id」不知道是哪个参数错了。
func parseQueryID(c *gin.Context, name, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return id, true
}

// auditAccountEvent 记录账号体系（用户 / 组）的审计事件。
//
// 与 auditEvent 分开是因为后者把 object 钉死成 datasource:<id>，
// 而这里要记 group:<id> / user:<id>。硬塞进旧函数会让它变成
// 「猜对象类型」的函数，比多几行代码更糟。
func auditAccountEvent(c *gin.Context, actorID int64, actorName, action, object string, detail map[string]any) {
	if auditDeps == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	_ = auditDeps.Record(c.Request.Context(), querysvc.AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		Action:    action,
		Object:    object,
		Detail:    detail,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
}

// writeBindError 把 JSON 绑定失败转成对用户可读的中文提示。
//
// 为什么不能直接 c.JSON(..., gin.H{"error": err.Error()})：
// gin 的 binding 错误长这样——
//
//	Key: 'createUserReq.Role' Error:Field validation for 'Role' failed on the 'required' tag
//
// 它把内部类型名（createUserReq）、字段名、校验 tag 全暴露给前端，
// 既是信息泄露（能据此推断后端结构），也没法给用户看：
// 用户不知道 "createUserReq" 是什么，更不知道 "required tag" 该怎么处理。
//
// 输出的是**白名单内的 JSON 字段名**，而不是「反射出来的字段名」：
// 只有确认了这个字段确实对外公开，才把名字写进响应。认不出来就退化成
// 「请求参数不合法」，宁可少说，也不要把内部标识符漏出去。
func writeBindError(c *gin.Context, err error) {
	var ve validator.ValidationErrors
	if errors.As(err, &ve) && len(ve) > 0 {
		parts := make([]string, 0, len(ve))
		for _, fe := range ve {
			name, ok := publicFieldName(fe.StructField())
			if !ok {
				// 认不出这个字段：可能是不对外暴露的内部字段，
				// 也可能将来新增字段忘了登记。两种情况下都不该把名字漏出去。
				parts = append(parts, "请求参数不合法")
				continue
			}
			switch fe.Tag() {
			case "required":
				parts = append(parts, name+" 必填")
			default:
				parts = append(parts, name+" 格式不正确")
			}
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数不合法：" + strings.Join(parts, "，")})
		return
	}

	// 类型不匹配（如 {"group_id": "abc"} 传给 *int64）不会走 validator，
	// 而是 UnmarshalTypeError，文本形如：
	//   json: cannot unmarshal string into Go struct field createUserReq.group_id of type int64
	//
	// 注意 ute.Field 在嵌套结构里可能是 "Outer.Inner" 这种**含 Go 字段名**的路径，
	// 直接返回会有泄露风险。这里只取最后一段并按白名单校验，
	// 不通过就整段丢弃。同理不输出 ute.Type.String()（会得到 "*int64"）。
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		field := ute.Field
		if i := strings.LastIndexByte(field, '.'); i >= 0 {
			field = field[i+1:]
		}
		if !isPublicField(field) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数格式不正确"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数 " + field + " 的格式不正确"})
		return
	}

	// JSON 语法错误（不是合法的 JSON 对象）属于客户端问题，也不带内部细节。
	c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式不正确，应为 JSON 对象"})
}

// publicFieldNames 是允许出现在错误提示里的 JSON 字段名白名单。
//
// 用显式列表而不是反射遍历 json tag：反射会把**所有**带 tag 的字段都认为
// 「对外公开」，包括将来有人加的内部字段。白名单是开白名单（默认拒绝），
// 新加字段忘了登记时表现是提示语变笼统，而不是泄露内部名字——前者可接受。
var publicFieldNames = map[string]struct{}{
	"username": {}, "email": {}, "role": {}, "status": {}, "group_id": {},
	"name": {}, "description": {},
	// old_password / new_password 是自己改密码的字段。只把字段名放进白名单，
	// 值永远不会出现在提示里——writeBindError 只输出名字。
	"old_password": {}, "new_password": {},
}

// isPublicField 判断该 JSON 字段名是否允许出现在响应里。
func isPublicField(name string) bool {
	_, ok := publicFieldNames[name]
	return ok
}

// publicFieldName 把 Go 结构体字段名映射成白名单内的 JSON 字段名。
//
// 查 tag 而不是硬编码 structField→json 映射：字段改名后这边不用跟着改。
// 返回 ok=false 表示「这个字段不该对外露出名字」，由调用方降级处理。
func publicFieldName(structField string) (string, bool) {
	types := []reflect.Type{
		reflect.TypeOf(createUserReq{}),
		reflect.TypeOf(updateUserReq{}),
		reflect.TypeOf(groupReq{}),
		reflect.TypeOf(changeOwnPasswordReq{}),
	}
	for _, t := range types {
		f, ok := t.FieldByName(structField)
		if !ok {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			return "", false
		}
		name, _, _ := strings.Cut(tag, ",")
		if !isPublicField(name) {
			return "", false
		}
		return name, true
	}
	return "", false
}

// writeStoreError 把 store 层的哨兵错误映射成 HTTP 状态码。
//
// 分档依据是「调用方该怎么处理」而不是「技术上哪里错了」：
//   - 404：目标资源不存在，换个 id 再来
//   - 409：资源存在，但当前状态不允许这个操作（已删除、最后一名超管、重名）
//   - 503：并发冲突下的暂时忙，等一会儿重试
//   - 500：我们自己出问题了，且响应体里不能带内部细节
//
// 关于错误文案的语言，本项目按「这个错误的文本是否原本就写给用户看」分四类，
// 不要机械统一成某一种写法（会二选一地弄坏某一类）：
//
//  1. 主动拼给用户看的业务提示（用户名已占用、只剩最后一名超管……）
//     → 写中文常量，直接回显。本函数 switch 里的分支属于这一类。
//  2. 驱动 / 数据库的原始错误（SQL 语法错、Mongo 管道不合法、连接失败）
//     → **保留原文**，因为那正是用户排错所需；但要经
//       conn_test_handler.go 里 friendlyConnError 那种「分类后附加可读说明」
//       的处理，而不是原样透传。注意那套样板是「消费英文」，不是「保留原文」。
//  3. 我们自己代码层的故障（panic 恢复、内部 invariant 失败）
//     → 必须有中文同类（"internal error"），不能把 Go 的 panic 文本回给前端。
//  4. 认证类失败（登录、令牌）
//     → 保持英文，且刻意不区分「用户不存在」与「密码错」，避免被用来枚举账号。
//
// 判据的落点：能回显的前提是「文本里不含内部标识符」（结构体名、表名、
// SQLSTATE）。binding 错误、不必回显的 store 错误都走过脱敏，
// 所以 writeBindError 输出的是白名单内的 JSON 字段名。
func writeStoreError(c *gin.Context, err error) {
	// 客户端主动取消（关标签页、切页面）时不该记 5xx。
	// 用 c.Abort 而不是 c.JSON：连接已断，写 body 只会产生
	// "write on hijacked connection" 这类噪声日志。
	if errors.Is(err, context.Canceled) {
		c.Abort()
		return
	}

	switch {
	case errors.Is(err, store.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在或已被删除"})
	case errors.Is(err, store.ErrGroupNotFound):
		// 与 ErrGroupMissing 刻意区分：这里是「对着一个已删的组直接操作」，
		// 用户该做的是返回列表刷新；而 GroupMissing 是「表单里带了个已删的组」，
		// 该做的是重开组下拉重选。文案相同会让用户不知道点哪儿。
		c.JSON(http.StatusNotFound, gin.H{"error": "该用户组已被删除或不存在"})
	case errors.Is(err, store.ErrGroupMissing):
		c.JSON(http.StatusNotFound, gin.H{"error": "你选择的用户组已不存在，请刷新后重新选择"})
	case errors.Is(err, store.ErrUsernameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已被占用，请换一个"})
	case errors.Is(err, store.ErrGroupNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "组名已被占用，请换一个"})
	case errors.Is(err, store.ErrUserDeleted):
		c.JSON(http.StatusConflict, gin.H{"error": "该账号已被删除，无法再修改"})
	case errors.Is(err, store.ErrLastSuperAdmin):
		c.JSON(http.StatusConflict, gin.H{"error": "不能移除最后一名可用的超级管理员"})
	case errors.Is(err, store.ErrAccountBusy):
		// 等超管集合锁超时。用 503 而不是 500：这不是服务坏了，
		// 是并发冲突下的暂时忙，调用方等一会儿重试即可。
		// 也不能用 429——那表示「你请求太频繁」，而这里是内部串行锁竞争。
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "账号管理操作繁忙，请稍后重试"})
	default:
		// 绝不把内部错误原文返回：err.Error() 可能包含 SQL 片段、表名、
		// 驱动层的连接串信息。前端展示「操作失败，请查看服务端日志」足够，
		// 真实原因留给服务端日志。
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
