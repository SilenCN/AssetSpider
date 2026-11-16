package service

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func intp(v int) *int { return &v }

// TestSourceInputPageIntervalValidate 校验站点级每页间隔的合法范围：
// -1（跟随全局）/ 0..60000 合法，其余（<-1 或 >60000）拒绝。
func TestSourceInputPageIntervalValidate(t *testing.T) {
	base := SourceInput{Name: "站", APIBase: "https://example.com/api.php/provide/vod/"}
	for _, v := range []int{-1, 0, 1, 60000} {
		in := base
		in.PageIntervalMs = intp(v)
		if err := in.validate(); err != nil {
			t.Errorf("pageIntervalMs=%d 应合法，实际 %v", v, err)
		}
	}
	for _, v := range []int{-2, 60001} {
		in := base
		in.PageIntervalMs = intp(v)
		if err := in.validate(); err == nil {
			t.Errorf("pageIntervalMs=%d 应被拒绝", v)
		}
	}
	// 未提供（旧客户端）应合法。
	if err := base.validate(); err != nil {
		t.Errorf("未提供 pageIntervalMs 应合法，实际 %v", err)
	}
}

// TestSourceInputPageIntervalApply 验证 apply 的哨兵语义：
// 新建未提供取 -1（跟随全局），编辑未提供保留原值，提供则以站点值为准（含 0）。
func TestSourceInputPageIntervalApply(t *testing.T) {
	base := SourceInput{Name: "站", APIBase: "https://x.com/a"}

	// 新建未提供 → -1。
	got := base.apply(&model.Source{})
	if got.PageIntervalMs != -1 {
		t.Fatalf("新建未提供应为 -1，实际 %d", got.PageIntervalMs)
	}

	// 新建提供 0 → 0（显式不限速，覆盖全局）。
	in := base
	in.PageIntervalMs = intp(0)
	got = in.apply(&model.Source{})
	if got.PageIntervalMs != 0 {
		t.Fatalf("新建提供 0 应为 0，实际 %d", got.PageIntervalMs)
	}

	// 编辑未提供 → 保留原来的站点覆盖值 250。
	existing := &model.Source{ID: 7, PageIntervalMs: 250}
	got = base.apply(existing)
	if got.PageIntervalMs != 250 {
		t.Fatalf("编辑未提供应保留 250，实际 %d", got.PageIntervalMs)
	}

	// 编辑提供 -1 → 改回跟随全局。
	in = base
	in.PageIntervalMs = intp(-1)
	got = in.apply(&model.Source{ID: 7, PageIntervalMs: 250})
	if got.PageIntervalMs != -1 {
		t.Fatalf("编辑提供 -1 应为 -1，实际 %d", got.PageIntervalMs)
	}
}
