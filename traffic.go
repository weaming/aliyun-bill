package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	bssopenapi "github.com/alibabacloud-go/bssopenapi-20171214/v6/client"
	"github.com/alibabacloud-go/tea/dara"
	tea "github.com/charmbracelet/bubbletea"
)

const trafficPackageProductCode = "flowbag"

type trafficPackage struct {
	instanceID      string
	remark          string
	region          string
	effectiveTime   string
	expiryTime      string
	totalAmount     float64
	remainingAmount float64
	totalUnit       string
	remainingUnit   string
	packageKind     string
	packageRegion   string
}

type trafficUsage struct {
	deductDate         string
	packageInstanceID  string
	deductedInstanceID string
	deductedProduct    string
	region             string
	amount             float64
}

type trafficDetailsLoadedMsg struct {
	details []trafficUsage
	err     error
}

func (m model) startTrafficDetail() (tea.Model, tea.Cmd) {
	if len(m.trafficPackages) == 0 {
		m.trafficDetailError = "没有可查询的有效共享流量包"
		return m, nil
	}

	if m.packageOffset >= len(m.trafficPackages) {
		m.packageOffset = len(m.trafficPackages) - 1
	}
	if m.packageOffset < 0 {
		m.packageOffset = 0
	}

	trafficPackage := m.trafficPackages[m.packageOffset]
	m.showTrafficDetails = true
	m.trafficDetailLoading = true
	m.trafficDetailError = ""
	m.trafficDetails = nil
	m.trafficDetailOffset = 0
	m.statusMsg = fmt.Sprintf("正在查询 %s 的 %s 抵扣明细...", trafficPackage.instanceID, m.month)

	return m, loadTrafficDetailsCmd(m.client, trafficPackage.instanceID, m.month)
}

func loadTrafficDetailsCmd(client *bssopenapi.Client, instanceID string, month string) tea.Cmd {
	return func() tea.Msg {
		details, err := fetchAllTrafficUsage(client, instanceID, month)
		return trafficDetailsLoadedMsg{details: details, err: err}
	}
}

func fetchAllTrafficPackages(client *bssopenapi.Client) ([]trafficPackage, error) {
	var result []trafficPackage
	pageNum := int32(1)
	pageSize := int32(300)

	for {
		request := &bssopenapi.QueryResourcePackageInstancesRequest{
			ProductCode: dara.String(trafficPackageProductCode),
			PageNum:     dara.Int32(pageNum),
			PageSize:    dara.Int32(pageSize),
		}
		response, err := client.QueryResourcePackageInstances(request)
		if err != nil {
			return nil, fmt.Errorf("QueryResourcePackageInstances 调用失败: %w", err)
		}
		if response.Body == nil {
			return nil, fmt.Errorf("QueryResourcePackageInstances 返回空响应")
		}
		if !dara.BoolValue(response.Body.Success) {
			return nil, fmt.Errorf("QueryResourcePackageInstances 返回错误: code=%s, message=%s",
				dara.StringValue(response.Body.Code), dara.StringValue(response.Body.Message))
		}

		if response.Body.Data != nil && response.Body.Data.Instances != nil {
			for _, item := range response.Body.Data.Instances.Instance {
				if item != nil {
					result = append(result, convertTrafficPackage(item))
				}
			}
		}

		if response.Body.Data == nil || response.Body.Data.Instances == nil ||
			len(response.Body.Data.Instances.Instance) < int(pageSize) {
			break
		}
		pageNum++
	}

	sortTrafficPackages(result)
	return result, nil
}

func convertTrafficPackage(item *bssopenapi.QueryResourcePackageInstancesResponseBodyDataInstancesInstance) trafficPackage {
	return trafficPackage{
		instanceID:      dara.StringValue(item.InstanceId),
		remark:          dara.StringValue(item.Remark),
		region:          dara.StringValue(item.Region),
		effectiveTime:   dara.StringValue(item.EffectiveTime),
		expiryTime:      dara.StringValue(item.ExpiryTime),
		totalAmount:     parseAmount(dara.StringValue(item.TotalAmount)),
		remainingAmount: parseAmount(dara.StringValue(item.RemainingAmount)),
		totalUnit:       dara.StringValue(item.TotalAmountUnit),
		remainingUnit:   dara.StringValue(item.RemainingAmountUnit),
		packageKind:     classifyTrafficPackage(dara.StringValue(item.Remark), dara.StringValue(item.PackageType)),
		packageRegion:   resolveTrafficRegion(dara.StringValue(item.Region), dara.StringValue(item.Remark), dara.StringValue(item.PackageType)),
	}
}

func classifyTrafficPackage(remark string, packageType string) string {
	text := strings.ToLower(remark + " " + packageType)
	if strings.Contains(text, "闲时") ||
		strings.Contains(text, "offpeak") ||
		strings.Contains(text, "off-peak") ||
		strings.Contains(text, "night") ||
		strings.Contains(text, "idle") {
		return "闲时"
	}
	return "全时"
}

func resolveTrafficRegion(region string, remark string, packageType string) string {
	if region != "" && region != "*" {
		return region
	}

	text := strings.ToLower(remark + " " + packageType)
	if strings.Contains(text, "亚太") || strings.Contains(text, "apac") || strings.Contains(text, "asia-pacific") {
		return "亚太"
	}
	if strings.Contains(text, "欧美") || strings.Contains(text, "europe") || strings.Contains(text, "america") {
		return "欧美"
	}
	if strings.Contains(text, "香港") || strings.Contains(text, "hongkong") {
		return "中国香港"
	}
	if strings.Contains(text, "闲时") || strings.Contains(text, "offpeak") || strings.Contains(text, "night") {
		return "亚太"
	}
	if region == "*" {
		return "多地域"
	}
	return region
}

func sortTrafficPackages(packages []trafficPackage) {
	sort.SliceStable(packages, func(left int, right int) bool {
		leftEmpty := packages[left].remainingAmount <= 0
		rightEmpty := packages[right].remainingAmount <= 0
		return leftEmpty == false && rightEmpty
	})
}

func fetchAllTrafficUsage(client *bssopenapi.Client, instanceID string, month string) ([]trafficUsage, error) {
	startTime, endTime, err := monthTimeRange(month)
	if err != nil {
		return nil, err
	}
	return fetchTrafficUsageRange(client, instanceID, startTime, endTime)
}

func fetchTrafficUsageRange(client *bssopenapi.Client, instanceID string, startTime string, endTime string) ([]trafficUsage, error) {
	var result []trafficUsage
	lastToken := ""
	for {
		request := &bssopenapi.QueryDPUtilizationDetailRequest{
			ProdCode:     dara.String(trafficPackageProductCode),
			StartTime:    dara.String(startTime),
			EndTime:      dara.String(endTime),
			IncludeShare: dara.Bool(false),
			Limit:        dara.Int32(300),
		}
		if instanceID != "" {
			request.InstanceId = dara.String(instanceID)
		}
		if lastToken != "" {
			request.LastToken = dara.String(lastToken)
		}

		response, err := client.QueryDPUtilizationDetail(request)
		if err != nil {
			return nil, fmt.Errorf("QueryDPUtilizationDetail 调用失败: %w", err)
		}
		if response.Body == nil {
			return nil, fmt.Errorf("QueryDPUtilizationDetail 返回空响应")
		}
		if !dara.BoolValue(response.Body.Success) {
			return nil, fmt.Errorf("QueryDPUtilizationDetail 返回错误: code=%s, message=%s",
				dara.StringValue(response.Body.Code), dara.StringValue(response.Body.Message))
		}

		if response.Body.Data != nil && response.Body.Data.DetailList != nil {
			for _, item := range response.Body.Data.DetailList.DetailList {
				if item != nil {
					result = append(result, convertTrafficUsage(item))
				}
			}
		}

		if response.Body.Data == nil {
			break
		}
		lastToken = dara.StringValue(response.Body.Data.NextToken)
		if lastToken == "" {
			break
		}
	}

	return result, nil
}

func convertTrafficUsage(item *bssopenapi.QueryDPUtilizationDetailResponseBodyDataDetailListDetailList) trafficUsage {
	amount := float64(dara.Float32Value(item.DeductQuantity))
	if amount == 0 {
		amount = float64(dara.Float32Value(item.DeductMeasure))
	}

	return trafficUsage{
		deductDate:         dara.StringValue(item.DeductDate),
		packageInstanceID:  dara.StringValue(item.InstanceId),
		deductedInstanceID: dara.StringValue(item.DeductedInstanceId),
		deductedProduct:    dara.StringValue(item.DeductedProductDetail),
		region:             dara.StringValue(item.Region),
		amount:             amount,
	}
}

func monthTimeRange(month string) (string, string, error) {
	start, err := time.ParseInLocation("2006-01", month, currentLocation())
	if err != nil {
		return "", "", fmt.Errorf("月份格式错误: %w", err)
	}
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	return start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"), nil
}

func currentLocation() *time.Location {
	timezone := os.Getenv("TZ")
	if timezone != "" {
		if location, err := time.LoadLocation(timezone); err == nil {
			return location
		}
	}
	return time.FixedZone("HKT", 8*60*60)
}

func parseAmount(value string) float64 {
	amount, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return amount
}

func (m model) renderTrafficTable() string {
	if m.trafficDetailLoading {
		return subtitleStyle.Render(m.statusMsg)
	}
	if m.trafficDetailError != "" {
		return errorStyle.Render("查询失败: " + m.trafficDetailError)
	}
	if m.showTrafficDetails {
		return m.renderTrafficDetails()
	}
	if m.trafficError != "" {
		return errorStyle.Render("流量包查询失败: " + m.trafficError)
	}
	if len(m.trafficPackages) == 0 {
		return subtitleStyle.Render("没有有效的共享流量包")
	}
	selectedIndex := m.packageOffset
	if selectedIndex >= len(m.trafficPackages) {
		selectedIndex = len(m.trafficPackages) - 1
	}
	if selectedIndex < 0 {
		selectedIndex = 0
	}

	const (
		colMark      = 2
		colKind      = 8
		colTotal     = 8
		colRemaining = 8
		colUsed      = 8
		colRegion    = 12
		colExpiry    = 21
		colInstance  = 24
		gap          = 2
	)
	var b strings.Builder
	header := padRight("", colMark) + strings.Repeat(" ", gap) +
		padRight("类型", colKind) + strings.Repeat(" ", gap) +
		padRight("总量", colTotal) + strings.Repeat(" ", gap) +
		padRight("剩余", colRemaining) + strings.Repeat(" ", gap) +
		padRight("已用", colUsed) + strings.Repeat(" ", gap) +
		padRight("区域", colRegion) + strings.Repeat(" ", gap) +
		padRight("到期时间", colExpiry) + strings.Repeat(" ", gap) +
		"实例 ID"
	b.WriteString(header + "\n")
	b.WriteString(strings.Repeat("─", minInt(getTermWidth(), stringWidth(header))) + "\n")

	pageStart := selectedIndex / m.pageSize * m.pageSize
	end := pageStart + m.pageSize
	if end > len(m.trafficPackages) {
		end = len(m.trafficPackages)
	}
	for index := pageStart; index < end; index++ {
		item := m.trafficPackages[index]
		used := item.totalAmount - item.remainingAmount
		if used < 0 {
			used = 0
		}
		unit := item.totalUnit
		if unit == "" {
			unit = item.remainingUnit
		}
		totalAmount := fmt.Sprintf("%.2f%s", item.totalAmount, unit)
		remainingAmount := fmt.Sprintf("%.2f%s", item.remainingAmount, unit)
		usedAmount := fmt.Sprintf("%.2f%s", used, unit)
		marker := " "
		if index == selectedIndex {
			marker = "▶"
		}
		row := padRight(marker, colMark) + strings.Repeat(" ", gap) +
			padRight(item.packageKind, colKind) + strings.Repeat(" ", gap) +
			padLeft(truncateRight(totalAmount, colTotal), colTotal) + strings.Repeat(" ", gap) +
			padLeft(truncateRight(remainingAmount, colRemaining), colRemaining) + strings.Repeat(" ", gap) +
			padLeft(truncateRight(usedAmount, colUsed), colUsed) + strings.Repeat(" ", gap) +
			padRight(truncateRight(item.packageRegion, colRegion), colRegion) + strings.Repeat(" ", gap) +
			padRight(formatPackageTime(item.expiryTime), colExpiry) + strings.Repeat(" ", gap) +
			truncateRight(item.instanceID, colInstance)
		b.WriteString(row + "\n")
	}

	selected := m.trafficPackages[selectedIndex]
	used := selected.totalAmount - selected.remainingAmount
	if used < 0 {
		used = 0
	}
	percent := 0.0
	if selected.totalAmount > 0 {
		percent = used / selected.totalAmount * 100
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("选中: %s  |  已用 %.1f%%  |  %s", selected.instanceID, percent, selected.remark))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("明细查询范围: " + m.month + "（按当前账单月份）"))
	return b.String()
}

func (m model) renderTrafficDetails() string {
	if len(m.trafficDetails) == 0 {
		return subtitleStyle.Render("该流量包在当前月份没有抵扣明细")
	}

	const (
		colDate    = 18
		colProduct = 22
		colRegion  = 14
		colAmount  = 18
		gap        = 2
	)
	var b strings.Builder
	if m.packageOffset < len(m.trafficPackages) {
		selected := m.trafficPackages[m.packageOffset]
		b.WriteString(fmt.Sprintf("流量包: %s  |  %s\n", selected.instanceID, m.month))
	}
	header := padRight("抵扣时间", colDate) + strings.Repeat(" ", gap) +
		padRight("产品", colProduct) + strings.Repeat(" ", gap) +
		padRight("地域", colRegion) + strings.Repeat(" ", gap) +
		padRight("抵扣量", colAmount) + strings.Repeat(" ", gap) +
		"被抵扣实例"
	b.WriteString(header + "\n")
	b.WriteString(strings.Repeat("─", minInt(getTermWidth(), stringWidth(header))) + "\n")

	start := m.trafficDetailOffset
	if start > len(m.trafficDetails) {
		start = len(m.trafficDetails)
	}
	end := start + m.pageSize
	if end > len(m.trafficDetails) {
		end = len(m.trafficDetails)
	}
	for _, item := range m.trafficDetails[start:end] {
		row := padRight(formatPackageTime(item.deductDate), colDate) + strings.Repeat(" ", gap) +
			padRight(truncateRight(item.deductedProduct, colProduct), colProduct) + strings.Repeat(" ", gap) +
			padRight(truncateRight(item.region, colRegion), colRegion) + strings.Repeat(" ", gap) +
			padLeft(formatBytes(item.amount), colAmount) + strings.Repeat(" ", gap) +
			truncateRight(item.deductedInstanceID, 24)
		b.WriteString(row + "\n")
	}
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render(fmt.Sprintf("共 %d 条明细，当前 %d-%d | ↑↓翻页 | Esc 返回流量包列表 | r 刷新",
		len(m.trafficDetails), start+1, end)))
	return b.String()
}

func formatPackageTime(value string) string {
	if len(value) >= 19 {
		return strings.Replace(value[:19], "T", " ", 1)
	}
	return value
}

func formatBytes(value float64) string {
	if value == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	unitIndex := 0
	for math.Abs(value) >= 1024 && unitIndex < len(units)-1 {
		value /= 1024
		unitIndex++
	}
	return fmt.Sprintf("%.2f %s", value, units[unitIndex])
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
