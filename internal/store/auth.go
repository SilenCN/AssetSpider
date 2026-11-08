package store

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// ---------- 用户 ----------

// CountUsers 返回账号总数。为 0 时表示系统尚未初始化，首个登录者将成为管理员。
func (s *Store) CountUsers() (int64, error) {
	var n int64
	err := s.db.Model(&model.User{}).Count(&n).Error
	return n, err
}

// CountEnabledAdmins 返回处于启用状态的管理员数量，用于防止系统失去最后一个管理员。
func (s *Store) CountEnabledAdmins() (int64, error) {
	var n int64
	err := s.db.Model(&model.User{}).
		Where("role = ? AND enabled = ?", model.RoleAdmin, true).Count(&n).Error
	return n, err
}

// CreateUser 新增账号并回填主键。用户名唯一索引会兜住并发下的重复注册。
func (s *Store) CreateUser(u *model.User) error {
	return s.db.Create(u).Error
}

// GetUser 按 ID 读取账号。
func (s *Store) GetUser(id uint) (*model.User, error) {
	var u model.User
	if err := s.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByUsername 按用户名读取账号。
func (s *Store) GetUserByUsername(username string) (*model.User, error) {
	var u model.User
	if err := s.db.Where("username = ?", username).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// ListUsers 返回全部账号，按 ID 正序（不含密码哈希）。
func (s *Store) ListUsers() ([]model.User, error) {
	var items []model.User
	if err := s.db.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// SaveUser 全量保存账号（密码哈希字段由调用方决定是否需要更新）。
func (s *Store) SaveUser(u *model.User) error {
	return s.db.Save(u).Error
}

// DeleteUser 删除账号，并级联清理其会话与站点授权。
func (s *Store) DeleteUser(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.Session{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.UserSource{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.User{}, id).Error
	})
}

// TouchLogin 记录最近一次成功登录时间。
func (s *Store) TouchLogin(id uint) error {
	return s.db.Model(&model.User{}).Where("id = ?", id).
		Update("last_login_at", time.Now()).Error
}

// ---------- 会话 ----------

// CreateSession 保存一条会话记录（token 以 SHA-256 摘要形式存储）。
func (s *Store) CreateSession(sess *model.Session) error {
	return s.db.Create(sess).Error
}

// GetSessionUser 用 token 摘要查找对应的有效账号。
// 令牌必须未过期，且账号必须处于启用状态；禁用用户会立即失去所有会话。
func (s *Store) GetSessionUser(tokenHash string) (*model.User, error) {
	var u model.User
	res := s.db.Model(&model.User{}).
		Select("users.*").
		Joins("JOIN sessions ON sessions.user_id = users.id").
		Where("sessions.token_hash = ? AND sessions.expires_at > ? AND users.enabled = ?",
			tokenHash, time.Now(), true).
		Limit(1).Find(&u)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &u, nil
}

// DeleteSession 吊销单个令牌（登出）。
func (s *Store) DeleteSession(tokenHash string) error {
	return s.db.Where("token_hash = ?", tokenHash).Delete(&model.Session{}).Error
}

// DeleteUserSessions 吊销某账号的全部令牌（改密、禁用、删除、权限变更时使用）。
func (s *Store) DeleteUserSessions(userID uint) error {
	return s.db.Where("user_id = ?", userID).Delete(&model.Session{}).Error
}

// PurgeExpiredSessions 清理已过期会话，登录时顺带执行。
func (s *Store) PurgeExpiredSessions() error {
	return s.db.Where("expires_at <= ?", time.Now()).Delete(&model.Session{}).Error
}

// ---------- 站点可见性授权 ----------

// ListSourceIDsByUser 返回某账号被授权可见的站点 ID 列表。
func (s *Store) ListSourceIDsByUser(userID uint) ([]uint, error) {
	var ids []uint
	err := s.db.Model(&model.UserSource{}).
		Where("user_id = ?", userID).Order("source_id ASC").Pluck("source_id", &ids).Error
	return ids, err
}

// ListSourceIDsByUsers 返回所有账号的可见站点 ID，供用户管理页展示。
func (s *Store) ListSourceIDsByUsers() (map[uint][]uint, error) {
	var rows []model.UserSource
	if err := s.db.Order("user_id ASC, source_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint][]uint, len(rows))
	for _, r := range rows {
		out[r.UserID] = append(out[r.UserID], r.SourceID)
	}
	return out, nil
}

// SetUserSources 全量替换某账号的可见站点授权。
// 不存在的站点 ID 会被忽略，重复 ID 会去重，空列表表示不授权任何站点。
func (s *Store) SetUserSources(userID uint, sourceIDs []uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserSource{}).Error; err != nil {
			return err
		}
		seen := make(map[uint]bool, len(sourceIDs))
		rows := make([]model.UserSource, 0, len(sourceIDs))
		for _, id := range sourceIDs {
			if id == 0 || seen[id] {
				continue
			}
			seen[id] = true
			var cnt int64
			if err := tx.Model(&model.Source{}).Where("id = ?", id).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt == 0 {
				continue
			}
			rows = append(rows, model.UserSource{UserID: userID, SourceID: id})
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
}
