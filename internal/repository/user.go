package repository

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// UserRepository 是账号、会话与站点授权的数据访问契约。
// 三者同属「访问控制」这一实体族，因此共用一个仓储对象。
type UserRepository interface {
	CountUsers() (int64, error)
	CountEnabledAdmins() (int64, error)
	CreateUser(u *model.User) error
	GetUser(id uint) (*model.User, error)
	GetUserByUsername(username string) (*model.User, error)
	GetUserByApiKey(key string) (*model.User, error)
	SetUserApiKey(id uint, key string) error
	ListUsers() ([]model.User, error)
	SaveUser(u *model.User) error
	DeleteUser(id uint) error
	TouchLogin(id uint) error

	CreateSession(sess *model.Session) error
	GetSessionUser(tokenHash string) (*model.User, error)
	DeleteSession(tokenHash string) error
	DeleteUserSessions(userID uint) error
	PurgeExpiredSessions() error

	ListSourceIDsByUser(userID uint) ([]uint, error)
	ListSourceIDsByUsers() (map[uint][]uint, error)
	SetUserSources(userID uint, sourceIDs []uint) error

	// TVBox 站点自选：mode 见 model.User.TVBoxSourcesMode；勾选表只在 selected 模式生效。
	ListTVBoxSourceIDsByUser(userID uint) ([]uint, error)
	SetUserTVBoxSources(userID uint, mode string, sourceIDs []uint) error
}

// userRepository 是账号实体专属的仓储对象。
type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

var _ UserRepository = (*userRepository)(nil)

// ---------- 账号 ----------

func (r *userRepository) CountUsers() (int64, error) {
	var n int64
	err := r.db.Model(&model.User{}).Count(&n).Error
	return n, err
}

func (r *userRepository) CountEnabledAdmins() (int64, error) {
	var n int64
	err := r.db.Model(&model.User{}).
		Where("role = ? AND enabled = ?", model.RoleAdmin, true).Count(&n).Error
	return n, err
}

func (r *userRepository) CreateUser(u *model.User) error {
	return r.db.Create(u).Error
}

// GetUser / GetUserByUsername 用 Find 而非 First，避免「记录不存在」时
// GORM 打印一条 warn 日志（这是登录校验与用户校验的正常分支）。
func (r *userRepository) GetUser(id uint) (*model.User, error) {
	var u model.User
	res := r.db.Limit(1).Find(&u, id)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &u, nil
}

func (r *userRepository) GetUserByUsername(username string) (*model.User, error) {
	var u model.User
	res := r.db.Where("username = ?", username).Limit(1).Find(&u)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &u, nil
}

// GetUserByApiKey 按 TVBox 接口鉴权键查找「启用」用户。
// 空 key 直接返回 ErrNotFound，避免误命中 api_key 为空的历史用户。
func (r *userRepository) GetUserByApiKey(key string) (*model.User, error) {
	if key == "" {
		return nil, ErrNotFound
	}
	var u model.User
	res := r.db.Where("api_key = ? AND enabled = ?", key, true).Limit(1).Find(&u)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &u, nil
}

// SetUserApiKey 设置用户的 TVBox 鉴权键；传空字符串表示撤销。
// 撤销时写 NULL（而非空串），因为 api_key 有唯一索引，多个空串会互相冲突。
func (r *userRepository) SetUserApiKey(id uint, key string) error {
	var value interface{}
	if key == "" {
		value = nil
	} else {
		value = key
	}
	return r.db.Model(&model.User{}).Where("id = ?", id).Update("api_key", value).Error
}

func (r *userRepository) ListUsers() ([]model.User, error) {
	var items []model.User
	if err := r.db.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *userRepository) SaveUser(u *model.User) error {
	return r.db.Save(u).Error
}

// DeleteUser 删除账号，并在同一事务里级联清理其会话与站点授权。
func (r *userRepository) DeleteUser(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.Session{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.UserSource{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.UserTVBoxSource{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.User{}, id).Error
	})
}

func (r *userRepository) TouchLogin(id uint) error {
	return r.db.Model(&model.User{}).Where("id = ?", id).
		Update("last_login_at", time.Now()).Error
}

// ---------- 会话 ----------

func (r *userRepository) CreateSession(sess *model.Session) error {
	return r.db.Create(sess).Error
}

// GetSessionUser 用令牌摘要查找有效账号：令牌必须未过期且账号必须启用，
// 因此停用账号会立即失去所有会话。
func (r *userRepository) GetSessionUser(tokenHash string) (*model.User, error) {
	var u model.User
	res := r.db.Model(&model.User{}).
		Select("users.*").
		Joins("JOIN sessions ON sessions.user_id = users.id").
		Where("sessions.token_hash = ? AND sessions.expires_at > ? AND users.enabled = ?",
			tokenHash, time.Now(), true).
		Limit(1).Find(&u)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &u, nil
}

func (r *userRepository) DeleteSession(tokenHash string) error {
	return r.db.Where("token_hash = ?", tokenHash).Delete(&model.Session{}).Error
}

func (r *userRepository) DeleteUserSessions(userID uint) error {
	return r.db.Where("user_id = ?", userID).Delete(&model.Session{}).Error
}

func (r *userRepository) PurgeExpiredSessions() error {
	return r.db.Where("expires_at <= ?", time.Now()).Delete(&model.Session{}).Error
}

// ---------- 站点可见性授权 ----------

func (r *userRepository) ListSourceIDsByUser(userID uint) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&model.UserSource{}).
		Where("user_id = ?", userID).Order("source_id ASC").Pluck("source_id", &ids).Error
	return ids, err
}

func (r *userRepository) ListSourceIDsByUsers() (map[uint][]uint, error) {
	var rows []model.UserSource
	if err := r.db.Order("user_id ASC, source_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint][]uint, len(rows))
	for _, row := range rows {
		out[row.UserID] = append(out[row.UserID], row.SourceID)
	}
	return out, nil
}

// validSourceIDs 把调用方传入的站点 ID 归一化：忽略 0 与重复值（保留首次出现
// 的顺序），并用一次 WHERE id IN (?) 查询剔除库中不存在的站点——历史实现逐条
// Count，N 个 ID 要发 N 条 SQL。返回值可直接用于构造关联行。
func validSourceIDs(tx *gorm.DB, ids []uint) ([]uint, error) {
	candidates := make([]uint, 0, len(ids))
	seen := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		candidates = append(candidates, id)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	var existing []uint
	if err := tx.Model(&model.Source{}).Where("id IN ?", candidates).
		Pluck("id", &existing).Error; err != nil {
		return nil, err
	}
	exists := make(map[uint]bool, len(existing))
	for _, id := range existing {
		exists[id] = true
	}
	valid := make([]uint, 0, len(existing))
	for _, id := range candidates {
		if exists[id] {
			valid = append(valid, id)
		}
	}
	return valid, nil
}

// SetUserSources 全量替换某账号的可见站点授权。
// 不存在的站点 ID 会被忽略，重复 ID 会去重，空列表表示不授权任何站点。
func (r *userRepository) SetUserSources(userID uint, sourceIDs []uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserSource{}).Error; err != nil {
			return err
		}
		valid, err := validSourceIDs(tx, sourceIDs)
		if err != nil {
			return err
		}
		if len(valid) == 0 {
			return nil
		}
		rows := make([]model.UserSource, 0, len(valid))
		for _, id := range valid {
			rows = append(rows, model.UserSource{UserID: userID, SourceID: id})
		}
		return tx.Create(&rows).Error
	})
}

// ---------- TVBox 站点自选 ----------

func (r *userRepository) ListTVBoxSourceIDsByUser(userID uint) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&model.UserTVBoxSource{}).
		Where("user_id = ?", userID).Order("source_id ASC").Pluck("source_id", &ids).Error
	return ids, err
}

// SetUserTVBoxSources 在一个事务里更新用户的 TVBox 站点模式并全量替换勾选列表。
// mode 与 sourceIDs 都来自服务层校验后的数据；空列表在 selected 模式下表示
// 「一个站点都不开放」（这是用户的合法选择），不能与「未设置」混淆——后者由 mode=all 表达。
func (r *userRepository) SetUserTVBoxSources(userID uint, mode string, sourceIDs []uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			Update("tvbox_sources_mode", mode).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserTVBoxSource{}).Error; err != nil {
			return err
		}
		valid, err := validSourceIDs(tx, sourceIDs)
		if err != nil {
			return err
		}
		if len(valid) == 0 {
			return nil
		}
		rows := make([]model.UserTVBoxSource, 0, len(valid))
		for _, id := range valid {
			rows = append(rows, model.UserTVBoxSource{UserID: userID, SourceID: id})
		}
		return tx.Create(&rows).Error
	})
}
