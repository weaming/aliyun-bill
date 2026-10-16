package main

import (
	"fmt"
	"strings"
	"time"

	bssopenapi "github.com/alibabacloud-go/bssopenapi-20171214/v6/client"
	"github.com/alibabacloud-go/tea/dara"
)

type xbarTrafficSummary struct {
	fullTime float64
	offPeak  float64
	other    float64
}

type xbarTrafficPeriod struct {
	name    string
	start   time.Time
	end     time.Time
	summary xbarTrafficSummary
}

type xbarSnapshot struct {
	currentBill  float64
	previousBill float64
	remaining    xbarTrafficSummary
	periods      []xbarTrafficPeriod
}

type xbarBillResult struct {
	value float64
	err   error
}

type xbarPackagesResult struct {
	value []trafficPackage
	err   error
}

type xbarUsageResult struct {
	value []trafficUsage
	err   error
}

func runXbar() error {
	client, err := createClient()
	if err != nil {
		return err
	}

	now := time.Now().In(currentLocation())
	currentMonthValue := fmt.Sprintf("%04d-%02d", now.Year(), now.Month())
	previousMonthValue := shiftMonth(currentMonthValue, -1)
	periods := buildXbarTrafficPeriods(now)
	usageStart := periods[0].start
	usageEnd := now.Add(time.Second)

	currentBillCh := make(chan xbarBillResult, 1)
	previousBillCh := make(chan xbarBillResult, 1)
	packagesCh := make(chan xbarPackagesResult, 1)
	usageCh := make(chan xbarUsageResult, 1)
	go func() {
		value, err := fetchBillTotal(client, currentMonthValue)
		currentBillCh <- xbarBillResult{value: value, err: err}
	}()
	go func() {
		value, err := fetchBillTotal(client, previousMonthValue)
		previousBillCh <- xbarBillResult{value: value, err: err}
	}()
	go func() {
		value, err := fetchAllTrafficPackages(client)
		packagesCh <- xbarPackagesResult{value: value, err: err}
	}()
	go func() {
		value, err := fetchTrafficUsageRange(
			client,
			"",
			formatTrafficAPITime(usageStart),
			formatTrafficAPITime(usageEnd),
		)
		usageCh <- xbarUsageResult{value: value, err: err}
	}()

	currentBillResult := <-currentBillCh
	if currentBillResult.err != nil {
		return currentBillResult.err
	}
	previousBillResult := <-previousBillCh
	if previousBillResult.err != nil {
		return previousBillResult.err
	}
	packagesResult := <-packagesCh
	if packagesResult.err != nil {
		return packagesResult.err
	}
	usageResult := <-usageCh
	if usageResult.err != nil {
		return usageResult.err
	}
	if err := aggregateXbarTraffic(periods, packagesResult.value, usageResult.value); err != nil {
		return err
	}

	printXbarSnapshot(xbarSnapshot{
		currentBill:  currentBillResult.value,
		previousBill: previousBillResult.value,
		remaining:    summarizeTrafficPackageRemaining(packagesResult.value),
		periods:      periods,
	})
	return nil
}

func summarizeTrafficPackageRemaining(packages []trafficPackage) xbarTrafficSummary {
	var summary xbarTrafficSummary
	for _, item := range packages {
		remaining := item.remainingAmount
		if remaining <= 0 {
			continue
		}
		remainingBytes := trafficAmountToBytes(remaining, item.remainingUnit)
		switch item.packageKind {
		case "全时":
			summary.fullTime += remainingBytes
		case "闲时":
			summary.offPeak += remainingBytes
		default:
			summary.other += remainingBytes
		}
	}
	return summary
}

func fetchBillTotal(client *bssopenapi.Client, billingCycle string) (float64, error) {
	items, err := fetchAllBills(client, billingCycle)
	if err != nil {
		return 0, err
	}

	var total float64
	for _, item := range items {
		if item != nil {
			total += float64(dara.Float32Value(item.PretaxAmount))
		}
	}
	return total, nil
}

func buildXbarTrafficPeriods(now time.Time) []xbarTrafficPeriod {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	previousMonthStart := monthStart.AddDate(0, -1, 0)

	return []xbarTrafficPeriod{
		{name: "上月", start: previousMonthStart, end: monthStart},
		{name: "本月", start: monthStart, end: now.Add(time.Second)},
		{name: "最近7天", start: dayStart.AddDate(0, 0, -6), end: now.Add(time.Second)},
		{name: "最近3天", start: dayStart.AddDate(0, 0, -2), end: now.Add(time.Second)},
		{name: "今天", start: dayStart, end: now.Add(time.Second)},
	}
}

func aggregateXbarTraffic(periods []xbarTrafficPeriod, packages []trafficPackage, usage []trafficUsage) error {
	packageKinds := make(map[string]string, len(packages))
	for _, item := range packages {
		packageKinds[item.instanceID] = item.packageKind
	}

	for _, item := range usage {
		usedAt, err := parseTrafficUsageTime(item.deductDate)
		if err != nil {
			return fmt.Errorf("解析流量抵扣时间失败: %w", err)
		}
		kind := packageKinds[item.packageInstanceID]
		for index := range periods {
			period := &periods[index]
			if usedAt.Before(period.start) || !usedAt.Before(period.end) {
				continue
			}
			switch kind {
			case "全时":
				period.summary.fullTime += item.amount
			case "闲时":
				period.summary.offPeak += item.amount
			default:
				period.summary.other += item.amount
			}
		}
	}
	return nil
}

func parseTrafficUsageTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layouts := []string{
		"2006/1/2 15:04:05",
		"2006/1/2 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04Z",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, currentLocation()); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法识别时间 %q", value)
}

func formatTrafficAPITime(value time.Time) string {
	return value.In(currentLocation()).Format("2006-01-02 15:04:05")
}

func printXbarSnapshot(snapshot xbarSnapshot) {
	todayTraffic := snapshot.periods[4].summary
	todayTrafficTotal := todayTraffic.fullTime + todayTraffic.offPeak + todayTraffic.other
	fmt.Printf("¥%.2f %s\n", snapshot.currentBill, formatBytesCompact(todayTrafficTotal))
	fmt.Println("---")
	fmt.Println("阿里云统计")
	fmt.Println("---")
	fmt.Println("月度消费")
	fmt.Printf("本月 ¥%.2f\n", snapshot.currentBill)
	fmt.Printf("上月 ¥%.2f\n", snapshot.previousBill)
	fmt.Println("---")
	fmt.Println("流量包使用")
	remainingTotal := snapshot.remaining.fullTime + snapshot.remaining.offPeak + snapshot.remaining.other
	fmt.Printf("剩余 全 %s 闲 %s 总 %s\n",
		formatBytesCompact(snapshot.remaining.fullTime),
		formatBytesCompact(snapshot.remaining.offPeak),
		formatBytesCompact(remainingTotal))
	for index := len(snapshot.periods) - 1; index >= 0; index-- {
		period := snapshot.periods[index]
		total := period.summary.fullTime + period.summary.offPeak + period.summary.other
		fmt.Printf("%s 全 %s 闲 %s 总 %s\n", period.name,
			formatBytesCompact(period.summary.fullTime),
			formatBytesCompact(period.summary.offPeak),
			formatBytesCompact(total))
	}
}

func formatBytesCompact(value float64) string {
	return strings.ReplaceAll(formatBytes(value), " ", "")
}
