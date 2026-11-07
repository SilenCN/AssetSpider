// Package model 定义持久化数据模型。
package model

import "time"

// Asset 表示一条采集到的资产记录。
type Asset struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Host      string    `gorm:"size:255;not null;index" json:"host"`
	IP        string    `gorm:"size:64" json:"ip"`
	Category  string    `gorm:"size:64" json:"category"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
