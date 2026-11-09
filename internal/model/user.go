package model

import "time"

// 用户角色。管理员拥有全部权限；普通用户仅能浏览被授权的站点及其数据。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// User 是一个可登录账号。
//
// PasswordHash 永远不序列化给前端（json:"-"）。系统第一个通过登录接口
// 建立的账号自动成为管理员；之后的账号只能由管理员创建。
type User struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Username     string `gorm:"size:64;not null;uniqueIndex" json:"username"`
	PasswordHash string `gorm:"size:128;not null" json:"-"`
	Role         string `gorm:"size:16;not null;index" json:"role"`
	// 不设 default:true，否则 GORM 会把显式的 false 当作零值跳过并套用默认值。
	Enabled     bool       `gorm:"not null;index" json:"enabled"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// IsAdmin 判断该用户是否为管理员。
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Session 是一次登录会话。数据库只保存 token 的 SHA-256 摘要，
// 即使数据库文件泄露也不会直接暴露可用的登录令牌。
type Session struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	TokenHash string `gorm:"size:64;not null;uniqueIndex" json:"-"`
	UserID    uint   `gorm:"not null;index" json:"userId"`
	// ExpiresAt 过期后令牌立即失效。
	ExpiresAt time.Time `gorm:"not null;index" json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// UserSource 表示「某个普通用户可见某个站点」的授权关系。
// 管理员不需要记录（默认可见全部站点）。
type UserSource struct {
	UserID    uint      `gorm:"primaryKey;autoIncrement:false" json:"userId"`
	SourceID  uint      `gorm:"primaryKey;autoIncrement:false;index" json:"sourceId"`
	CreatedAt time.Time `json:"createdAt"`
}

// TableName 指定关联表名。
func (UserSource) TableName() string { return "user_sources" }
