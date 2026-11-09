package repository

import "gorm.io/gorm"

// 站点可见性是横切在多个实体仓储上的查询约束，单独放到一个文件里，
// 避免每个实体文件重复实现「管理员全可见 / 普通用户仅授权站点」的规则。
//
// 约定（与 service.Principal.Visible 对应）：
//   - 完全不传参数：服务内部调用或管理员，不附加任何过滤；
//   - 传入 nil：管理员，同样不过滤；
//   - 传入长度为 0 的非 nil 切片：普通用户且没有授权，结果恒为空；
//   - 传入非空切片：只返回这些站点 ID 的数据。

// firstVisible 从可选参数中取出可见站点集合。
func firstVisible(args ...[]uint) []uint {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

// applyVisibility 把「可见站点 ID 集合」应用到查询上。
func applyVisibility(q *gorm.DB, column string, visible []uint) *gorm.DB {
	if visible == nil {
		return q
	}
	if len(visible) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where(column+" IN ?", visible)
}
