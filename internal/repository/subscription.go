package repository

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// SubscriptionRepository 是 Subscription 实体的数据访问契约。
type SubscriptionRepository interface {
	ListSubscriptions() ([]model.Subscription, error)
	GetSubscription(id uint) (*model.Subscription, error)
	CreateSubscription(sub *model.Subscription) error
	// UpdateSubscription 只更新可编辑字段，刷新状态（Last*）保持原值。
	UpdateSubscription(id uint, in *model.Subscription) (*model.Subscription, error)
	// DeleteSubscription 删除订阅，并把它名下的站点解绑为手动站点：
	// 站点与已入库影片不受影响，只是不再随订阅刷新。
	DeleteSubscription(id uint) error
	// SaveSubscriptionResult 回写一次刷新的结果（状态、失败原因、时间与各项计数）。
	SaveSubscriptionResult(id uint, status, errMsg string, at time.Time, stats SubscriptionStats) error
}

// SubscriptionStats 是一次订阅刷新实际产生的变更数量。
type SubscriptionStats struct {
	Created  int `json:"created"`
	Updated  int `json:"updated"`
	Disabled int `json:"disabled"`
	Skipped  int `json:"skipped"`
}

type subscriptionRepository struct {
	db *gorm.DB
}

func NewSubscriptionRepository(db *gorm.DB) SubscriptionRepository {
	return &subscriptionRepository{db: db}
}

var _ SubscriptionRepository = (*subscriptionRepository)(nil)

func (r *subscriptionRepository) ListSubscriptions() ([]model.Subscription, error) {
	var items []model.Subscription
	if err := r.db.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *subscriptionRepository) GetSubscription(id uint) (*model.Subscription, error) {
	var sub model.Subscription
	if err := r.db.First(&sub, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &sub, nil
}

func (r *subscriptionRepository) CreateSubscription(sub *model.Subscription) error {
	return r.db.Create(sub).Error
}

// UpdateSubscription 按 ID 更新订阅的可编辑字段。
//
// 刷新状态由刷新流程单独回写（SaveSubscriptionResult），所以这里显式保留 Last*：
// 用户改个名字不应该把「上次刷新失败的原因」清掉。
func (r *subscriptionRepository) UpdateSubscription(id uint, in *model.Subscription) (*model.Subscription, error) {
	sub, err := r.GetSubscription(id)
	if err != nil {
		return nil, err
	}
	sub.Name = in.Name
	sub.URL = in.URL
	sub.IntervalHours = in.IntervalHours
	sub.Enabled = in.Enabled
	if err := r.db.Save(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

func (r *subscriptionRepository) DeleteSubscription(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 先解绑：订阅没了，站点还在，只是回到「手动添加」的语义。
		// 这里不删站点，删除会连带删掉已采集的影片与采集记录，代价远大于收益。
		if err := tx.Model(&model.Source{}).Where("subscription_id = ?", id).
			UpdateColumn("subscription_id", 0).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Subscription{}, id).Error
	})
}

func (r *subscriptionRepository) SaveSubscriptionResult(id uint, status, errMsg string, at time.Time, stats SubscriptionStats) error {
	// 刷新状态不是「用户改了订阅配置」，用 Updates 会把 updated_at 一起刷新，
	// 列表上会误显示成刚编辑过；因此这里只写状态列。
	return r.db.Model(&model.Subscription{}).Where("id = ?", id).UpdateColumns(map[string]interface{}{
		"last_status":   status,
		"last_error":    errMsg,
		"last_sync_at":  at,
		"last_created":  stats.Created,
		"last_updated":  stats.Updated,
		"last_disabled": stats.Disabled,
		"last_skipped":  stats.Skipped,
	}).Error
}
