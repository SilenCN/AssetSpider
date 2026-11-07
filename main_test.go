package main

import (
	"fmt"
	"testing"
)

func TestBanner(t *testing.T) {
	want := fmt.Sprintf("AssetSpider v%s", version)
	if got := banner(); got != want {
		t.Errorf("banner() = %q, want %q", got, want)
	}
}
