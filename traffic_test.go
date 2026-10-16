package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMonthTimeRange(t *testing.T) {
	start, end, err := monthTimeRange("2026-02")
	if err != nil {
		t.Fatalf("monthTimeRange 返回错误: %v", err)
	}
	if start != "2026-02-01 00:00:00" {
		t.Fatalf("开始时间错误: %s", start)
	}
	if end != "2026-02-28 23:59:59" {
		t.Fatalf("结束时间错误: %s", end)
	}
}

func TestMonthTimeRangeRejectsInvalidMonth(t *testing.T) {
	if _, _, err := monthTimeRange("2026-13"); err == nil {
		t.Fatal("无效月份应返回错误")
	}
}

func TestParseAmount(t *testing.T) {
	if got := parseAmount(" 1024.50 "); got != 1024.5 {
		t.Fatalf("解析金额错误: %v", got)
	}
	if got := parseAmount("invalid"); got != 0 {
		t.Fatalf("无效金额应返回 0: %v", got)
	}
}

func TestFormatBytes(t *testing.T) {
	if got := formatBytes(1024); got != "1.00 KB" {
		t.Fatalf("格式化字节数错误: %s", got)
	}
}

func TestTrafficAmountToBytes(t *testing.T) {
	if got := trafficAmountToBytes(1, "GB"); got != 1024*1024*1024 {
		t.Fatalf("GB 换算错误: %v", got)
	}
	if got := trafficAmountToBytes(2, "MB"); got != 2*1024*1024 {
		t.Fatalf("MB 换算错误: %v", got)
	}
}

func TestTrafficPackageSelectionMovesWithinPackageList(t *testing.T) {
	modelState := model{
		view:      "data",
		activeTab: 2,
		pageSize:  20,
		trafficPackages: []trafficPackage{
			{instanceID: "package-1"},
			{instanceID: "package-2"},
		},
	}

	updated, _ := modelState.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := updated.(model).packageOffset; got != 1 {
		t.Fatalf("流量包选择位置错误: %d", got)
	}
}

func TestClassifyTrafficPackage(t *testing.T) {
	if got := classifyTrafficPackage("亚太闲时", ""); got != "闲时" {
		t.Fatalf("闲时流量包分类错误: %s", got)
	}
	if got := classifyTrafficPackage("亚太全时", "flowbag_fulltime"); got != "全时" {
		t.Fatalf("全时流量包分类错误: %s", got)
	}
}

func TestResolveTrafficRegion(t *testing.T) {
	if got := resolveTrafficRegion("*", "共享流量包-亚太", ""); got != "亚太" {
		t.Fatalf("亚太流量包区域错误: %s", got)
	}
	if got := resolveTrafficRegion("*", "", ""); got != "多地域" {
		t.Fatalf("未知多地域流量包区域错误: %s", got)
	}
}

func TestSortTrafficPackagesPutsEmptyPackagesLast(t *testing.T) {
	packages := []trafficPackage{
		{instanceID: "empty", remainingAmount: 0},
		{instanceID: "available", remainingAmount: 1},
	}

	sortTrafficPackages(packages)
	if packages[0].instanceID != "available" || packages[1].instanceID != "empty" {
		t.Fatalf("流量包排序错误: %#v", packages)
	}
}

func TestParseTrafficUsageTime(t *testing.T) {
	parsed, err := parseTrafficUsageTime("2026-10-06T22:26Z")
	if err != nil {
		t.Fatalf("解析实际 API 时间失败: %v", err)
	}
	if parsed.Year() != 2026 || parsed.Month() != time.October || parsed.Day() != 6 {
		t.Fatalf("解析时间结果错误: %v", parsed)
	}
}

func TestBuildXbarTrafficPeriods(t *testing.T) {
	location := currentLocation()
	now := time.Date(2026, 10, 6, 12, 30, 0, 0, location)
	periods := buildXbarTrafficPeriods(now)
	if len(periods) != 5 {
		t.Fatalf("流量时间段数量错误: %d", len(periods))
	}
	if periods[0].start.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("上月开始时间错误: %s", periods[0].start)
	}
	if periods[4].start.Format("2006-01-02") != "2026-10-06" {
		t.Fatalf("今天开始时间错误: %s", periods[4].start)
	}
}
