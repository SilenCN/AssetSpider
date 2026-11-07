package main

import (
	"fmt"
	"testing"
)

func TestBanner(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "未指定目标",
			target: "",
			want:   fmt.Sprintf("AssetSpider v%s", version),
		},
		{
			name:   "指定目标",
			target: "example.com",
			want:   fmt.Sprintf("AssetSpider v%s | target: example.com (采集逻辑待实现)", version),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := banner(tt.target); got != tt.want {
				t.Errorf("banner(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}
