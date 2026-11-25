package store

import "errors"

// 账号管理相关的哨兵错误。handler 靠它们映射 HTTP 状态码，
// 而不是解析错误文本——文本是给人看的，随时会改。
var (
	// ErrUserNotFound 表示用户不存在。
	ErrUserNotFound = errors.New("user not found")
	// ErrUsernameTaken 表示登录名已被占用（含已被逻辑删除后未释放的情况）。
	ErrUsernameTaken = errors.New("username already taken")
	// ErrUserDeleted 表示目标是已逻辑删除的账号：不可再修改、不可恢复。
	ErrUserDeleted = errors.New("user is deleted")
	// ErrLastSuperAdmin 表示该操作会让平台失去最后一名可用的超级管理员。
	ErrLastSuperAdmin = errors.New("cannot remove the last active superadmin")
	// ErrGroupMissing 表示指定的组不存在。
	ErrGroupMissing = errors.New("group not found")
	// ErrOldPasswordMismatch 表示修改密码时当前密码校验失败。
	//
	// 单独一个哨兵而不是复用 ErrUserNotFound：这条错误要映射成 403 而不是 404，
	// 而且它的含义是「凭据不对」，与「这个人不存在」是两件事。
	// 注意 handler 回给前端的文案不要区分这两种情况的具体原因。
	ErrOldPasswordMismatch = errors.New("old password mismatch")
)
