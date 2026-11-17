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
	Username     string `gorm:"type:varchar(64);not null;uniqueIndex" json:"username"`
	PasswordHash string `gorm:"type:varchar(128);not null" json:"-"`
	Role         string `gorm:"type:varchar(16);not null;index" json:"role"`
	// 不设 default:true，否则 GORM 会把显式的 false 当作零值跳过并套用默认值。
	Enabled     bool       `gorm:"not null;index" json:"enabled"`
	LastLoginAt *time.Time `json:"lastLoginAt"`

	// ApiKey 是该用户的 TVBox 专属接口鉴权键（32 字节随机数的十六进制，64 字符）。
	// 用指针是为了让"未生成"存成 NULL 而非空串：SQL 唯一索引允许多个 NULL，
	// 但不允许多个空串，否则批量建用户会在 api_key 上撞唯一约束。
	// 用户在「我的接口」页自助生成 / 重置；它不同于会过期的 Session，长期有效直至重置。
	// json:"-" 避免随普通用户列表 / me 接口泄露，只在专门的 api-key 接口显式返回。
	ApiKey *string `gorm:"type:varchar(64);uniqueIndex" json:"-"`

	// TVBoxSourcesMode 决定该用户的 TVBox 配置接口列出哪些站点：
	//   - "all"（默认）：可见站点全部开放。空串也按 all 处理，保证历史用户升级后行为不变。
	//   - "selected"：只开放 UserTVBoxSource 里勾选的站点（仍受可见范围约束）。
	// 这是用户对自己接口的选择，不影响登录态的可见站点范围。
	TVBoxSourcesMode string `gorm:"column:tvbox_sources_mode;type:varchar(16);not null;default:'all'" json:"-"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IsAdmin 判断该用户是否为管理员。
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Session 是一次登录会话。数据库只保存 token 的 SHA-256 摘要，
// 即使数据库文件泄露也不会直接暴露可用的登录令牌。
type Session struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	TokenHash string `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
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

// UserTVBoxSource 表示「某个用户在自己的 TVBox 接口里勾选开放了某个站点」。
// 与 UserSource（管理员授予的可见范围）的区别：
//   - UserSource 是访问控制的硬边界，决定用户能否看到某站点的数据；
//   - UserTVBoxSource 是用户在该边界内对自己 TVBox 接口的软选择，只影响
//     /tvbox/:key/config 列出哪些站点，不会放大可见范围。
//
// 只有在 User.TVBoxSourcesMode == "selected" 时这张表才参与过滤。
type UserTVBoxSource struct {
	UserID    uint      `gorm:"primaryKey;autoIncrement:false" json:"userId"`
	SourceID  uint      `gorm:"primaryKey;autoIncrement:false;index" json:"sourceId"`
	CreatedAt time.Time `json:"createdAt"`
}

// TableName 指定关联表名。
func (UserTVBoxSource) TableName() string { return "user_tvbox_sources" }
