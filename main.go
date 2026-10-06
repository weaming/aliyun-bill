// 阿里云账单查询 TUI 工具
// 支持按月查询每日消费明细和消费项汇总
// 环境变量: ALI_KEY_ID, ALI_KEY_SECRET
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	bssopenapi "github.com/alibabacloud-go/bssopenapi-20171214/v6/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── 样式定义 ───────────────────────────────────────────────

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#5C4BFE")).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF4444"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	inputStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#5C4BFE")).
			Padding(0, 1).
			Width(40)

	tabActiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#5C4BFE")).
			Bold(true).
			Padding(0, 3)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#888888")).
				Padding(0, 3)

	highlightStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500")).
			Bold(true)
)

// ─── 数据模型 ───────────────────────────────────────────────

// billItem 单条账单记录
type billItem struct {
	productName  string
	productCode  string
	pretaxAmount float32
	subType      string
	instanceID   string
	billingDate  string
}

// dayProduct 某一天的某个产品消费
type dayProduct struct {
	date         string
	productName  string
	productCode  string
	pretaxAmount float32
	count        int
}

// ─── 应用状态 ───────────────────────────────────────────────

type model struct {
	client *bssopenapi.Client

	// 页面状态
	view      string // "input", "loading", "data"
	statusMsg string
	errorMsg  string

	// 输入
	inputValue string
	cursorOn   bool

	// 标签页
	activeTab int // 0=每日按产品, 1=消费项明细, 2=流量包

	// 数据
	month                string
	dailyData            []dayProduct // 每日×产品 二维聚合
	itemData             []billItem   // 按实例/消费项数据
	totalAmount          float32
	trafficPackages      []trafficPackage
	trafficDetails       []trafficUsage
	trafficError         string
	trafficDetailError   string
	trafficDetailLoading bool
	showTrafficDetails   bool
	trafficDetailOffset  int

	// 滚动
	itemOffset    int
	packageOffset int
	pageSize      int

	// 调试
	debug bool
}

// ─── 初始化 ────────────────────────────────────────────────

func initialModel() model {
	return model{
		view:       "input",
		inputValue: currentMonth(),
		activeTab:  0,
		pageSize:   20,
	}
}

// currentMonth returns "YYYY-MM" for the current local time.
func currentMonth() string {
	now := time.Now().In(currentLocation())
	return fmt.Sprintf("%04d-%02d", now.Year(), now.Month())
}

// shiftMonth adds n months to a "YYYY-MM" string.
func shiftMonth(s string, n int) string {
	if len(s) != 7 {
		s = currentMonth()
	}
	var y, mo int
	fmt.Sscanf(s, "%d-%d", &y, &mo)
	mo += n
	for mo < 1 {
		mo += 12
		y--
	}
	for mo > 12 {
		mo -= 12
		y++
	}
	return fmt.Sprintf("%04d-%02d", y, mo)
}

func (m model) Init() tea.Cmd {
	return tea.Tick(time.Second/2, func(_ time.Time) tea.Msg {
		return cursorTickMsg{}
	})
}

type cursorTickMsg struct{}
type dataLoadedMsg struct {
	daily           []dayProduct
	items           []billItem
	totalAmount     float32
	trafficPackages []trafficPackage
	trafficErr      error
	err             error
}

// ─── 更新 ───────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			if m.showTrafficDetails {
				m.showTrafficDetails = false
				m.trafficDetailError = ""
				m.trafficDetails = nil
				m.trafficDetailOffset = 0
				return m, nil
			}
			if m.view != "input" {
				m.view = "input"
				m.errorMsg = ""
				m.statusMsg = ""
				return m, nil
			}
			return m, tea.Quit

		case tea.KeyEnter:
			if m.view == "input" {
				return m.startQuery()
			}
			if m.view == "data" && m.activeTab == 2 && !m.showTrafficDetails {
				return m.startTrafficDetail()
			}

		case tea.KeyLeft:
			if m.view == "data" && m.activeTab > 0 {
				m.activeTab--
			}
		case tea.KeyRight:
			if m.view == "data" && m.activeTab < 2 {
				m.activeTab++
			}

		case tea.KeyUp:
			if m.view == "data" && !m.showTrafficDetails {
				if m.activeTab == 1 && m.itemOffset > 0 {
					m.itemOffset--
				}
				if m.activeTab == 2 && m.packageOffset > 0 {
					m.packageOffset--
				}
			}
			if m.view == "data" && m.showTrafficDetails && m.trafficDetailOffset > 0 {
				m.trafficDetailOffset--
			}
		case tea.KeyDown:
			if m.view == "data" && !m.showTrafficDetails && m.activeTab == 1 {
				maxOffset := len(m.itemData) - m.pageSize
				if maxOffset < 0 {
					maxOffset = 0
				}
				if m.itemOffset < maxOffset {
					m.itemOffset++
				}
			}
			if m.view == "data" && !m.showTrafficDetails && m.activeTab == 2 {
				maxOffset := len(m.trafficPackages) - 1
				if maxOffset < 0 {
					maxOffset = 0
				}
				if m.packageOffset < maxOffset {
					m.packageOffset++
				}
			}
			if m.view == "data" && m.showTrafficDetails {
				maxOffset := len(m.trafficDetails) - m.pageSize
				if maxOffset < 0 {
					maxOffset = 0
				}
				if m.trafficDetailOffset < maxOffset {
					m.trafficDetailOffset++
				}
			}
		case tea.KeyPgUp:
			if m.view == "data" && !m.showTrafficDetails && m.activeTab == 1 {
				m.itemOffset -= m.pageSize
				if m.itemOffset < 0 {
					m.itemOffset = 0
				}
			}
			if m.view == "data" && !m.showTrafficDetails && m.activeTab == 2 {
				m.packageOffset -= m.pageSize
				if m.packageOffset < 0 {
					m.packageOffset = 0
				}
			}
			if m.view == "data" && m.showTrafficDetails {
				m.trafficDetailOffset -= m.pageSize
				if m.trafficDetailOffset < 0 {
					m.trafficDetailOffset = 0
				}
			}
		case tea.KeyPgDown:
			if m.view == "data" && !m.showTrafficDetails && m.activeTab == 1 {
				maxOffset := len(m.itemData) - m.pageSize
				if maxOffset < 0 {
					maxOffset = 0
				}
				m.itemOffset += m.pageSize
				if m.itemOffset > maxOffset {
					m.itemOffset = maxOffset
				}
			}
			if m.view == "data" && !m.showTrafficDetails && m.activeTab == 2 {
				maxOffset := len(m.trafficPackages) - 1
				if maxOffset < 0 {
					maxOffset = 0
				}
				m.packageOffset += m.pageSize
				if m.packageOffset > maxOffset {
					m.packageOffset = maxOffset
				}
			}
			if m.view == "data" && m.showTrafficDetails {
				maxOffset := len(m.trafficDetails) - m.pageSize
				if maxOffset < 0 {
					maxOffset = 0
				}
				m.trafficDetailOffset += m.pageSize
				if m.trafficDetailOffset > maxOffset {
					m.trafficDetailOffset = maxOffset
				}
			}
		}

		// 全局快捷键
		switch msg.String() {
		case "q":
			if m.view != "input" {
				return m, tea.Quit
			}
		case "1":
			if m.view == "data" {
				m.activeTab = 0
			}
		case "2":
			if m.view == "data" {
				m.activeTab = 1
			}
		case "3":
			if m.view == "data" {
				m.activeTab = 2
			}
		case "r":
			if m.view == "data" && m.month != "" {
				if m.showTrafficDetails {
					return m.startTrafficDetail()
				}
				m.view = "loading"
				m.statusMsg = "正在刷新..."
				return m, loadDataCmd(m.client, m.month)
			}
		case "d":
			if m.view == "input" || m.view == "data" {
				prev := shiftMonth(m.month, -1)
				m.inputValue = prev
				if m.view == "data" {
					m.view = "loading"
					m.statusMsg = fmt.Sprintf("正在查询 %s 账单数据...", prev)
					return m, loadDataCmd(m.client, prev)
				}
			}
		case "f":
			if m.view == "input" || m.view == "data" {
				next := shiftMonth(m.month, 1)
				m.inputValue = next
				if m.view == "data" {
					m.view = "loading"
					m.statusMsg = fmt.Sprintf("正在查询 %s 账单数据...", next)
					return m, loadDataCmd(m.client, next)
				}
			}
		}

		// 输入模式处理
		if m.view == "input" {
			switch msg.String() {
			case "backspace":
				if len(m.inputValue) > 0 {
					m.inputValue = m.inputValue[:len(m.inputValue)-1]
				}
			case "enter", "esc", "q", "d", "f", "1", "2", "3", "r":
				// handled above
			default:
				if len(msg.String()) == 1 && len(m.inputValue) < 7 {
					m.inputValue += msg.String()
				}
			}
		}

	case cursorTickMsg:
		if m.view == "input" {
			m.cursorOn = !m.cursorOn
			return m, tea.Tick(time.Second/2, func(_ time.Time) tea.Msg {
				return cursorTickMsg{}
			})
		}

	case dataLoadedMsg:
		if msg.err != nil {
			m.errorMsg = fmt.Sprintf("数据加载失败: %v", msg.err)
			m.view = "input"
		} else if len(msg.daily) == 0 && len(msg.items) == 0 && len(msg.trafficPackages) == 0 {
			m.errorMsg = "该月份无账单数据，也没有有效流量包"
			m.view = "input"
		} else {
			m.dailyData = msg.daily
			m.itemData = msg.items
			m.totalAmount = msg.totalAmount
			m.trafficPackages = msg.trafficPackages
			m.trafficError = ""
			if msg.trafficErr != nil {
				m.trafficError = msg.trafficErr.Error()
			}
			m.month = m.inputValue
			m.view = "data"
			m.statusMsg = ""
			m.itemOffset = 0
			m.packageOffset = 0
			m.showTrafficDetails = false
			m.trafficDetails = nil
			m.trafficDetailError = ""
			m.trafficDetailOffset = 0
		}
	case trafficDetailsLoadedMsg:
		m.trafficDetailLoading = false
		if msg.err != nil {
			m.trafficDetailError = msg.err.Error()
			return m, nil
		}
		m.trafficDetails = msg.details
		m.trafficDetailError = ""
	}

	return m, nil
}

func (m model) startQuery() (tea.Model, tea.Cmd) {
	month := strings.TrimSpace(m.inputValue)
	if !isValidMonth(month) {
		m.errorMsg = "格式错误！请输入 YYYY-MM 格式，例如 2025-03"
		return m, nil
	}

	client, err := createClient()
	if err != nil {
		m.errorMsg = fmt.Sprintf("认证失败: %v", err)
		return m, nil
	}

	m.client = client
	m.month = month
	m.errorMsg = ""
	m.statusMsg = fmt.Sprintf("正在查询 %s 账单数据...", month)
	m.itemOffset = 0
	m.packageOffset = 0
	m.showTrafficDetails = false
	m.trafficDetails = nil
	m.trafficDetailOffset = 0
	m.view = "loading"

	return m, loadDataCmd(m.client, month)
}

func loadDataCmd(client *bssopenapi.Client, month string) tea.Cmd {
	return func() tea.Msg {
		fmt.Fprintf(os.Stderr, "[DEBUG] 开始查询 %s\n", month)
		result := loadDataSync(client, month)
		if result.err != nil {
			fmt.Fprintf(os.Stderr, "[DEBUG] 账单数据查询错误: %v\n", result.err)
			return dataLoadedMsg{err: result.err}
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] 聚合后 %d 条每日记录，%d 条消费项，%d 个流量包\n",
			len(result.daily), len(result.items), len(result.trafficPackages))
		if result.trafficErr != nil {
			fmt.Fprintf(os.Stderr, "[DEBUG] QueryResourcePackageInstances 错误: %v\n", result.trafficErr)
		}

		return dataLoadedMsg{
			daily:           result.daily,
			items:           result.items,
			totalAmount:     result.total,
			trafficPackages: result.trafficPackages,
			trafficErr:      result.trafficErr,
		}
	}
}

// ─── 数据聚合 ───────────────────────────────────────────────

func aggregateDaily(items []*bssopenapi.QueryBillResponseBodyDataItemsItem) []dayProduct {
	// 按 日期+产品 聚合
	type key struct {
		date string
		code string
	}
	groups := make(map[key]*dayProduct)

	for _, item := range items {
		date := pickDate(item)

		code := dara.StringValue(item.ProductCode)
		if code == "" {
			code = "其他"
		}
		name := dara.StringValue(item.ProductName)
		if name == "" {
			name = code
		}

		k := key{date, code}
		if g, ok := groups[k]; ok {
			g.pretaxAmount += dara.Float32Value(item.PretaxAmount)
			g.count++
		} else {
			groups[k] = &dayProduct{
				date:         date,
				productName:  name,
				productCode:  code,
				pretaxAmount: dara.Float32Value(item.PretaxAmount),
				count:        1,
			}
		}
	}

	var result []dayProduct
	for _, g := range groups {
		result = append(result, *g)
	}

	// 先按日期排序，再按金额降序
	sort.Slice(result, func(i, j int) bool {
		if result[i].date == result[j].date {
			return result[i].pretaxAmount > result[j].pretaxAmount
		}
		return result[i].date < result[j].date
	})

	return result
}

// pickDate 从账单项中提取日期，依次尝试 UsageStartTime → UsageEndTime → PaymentTime
func pickDate(item *bssopenapi.QueryBillResponseBodyDataItemsItem) string {
	for _, f := range []*string{item.UsageStartTime, item.UsageEndTime, item.PaymentTime} {
		if f != nil && len(*f) >= 10 {
			return (*f)[:10]
		}
	}
	return "未知"
}

func convertItems(items []*bssopenapi.DescribeInstanceBillResponseBodyDataItems) []billItem {
	var result []billItem
	for _, item := range items {
		result = append(result, billItem{
			productName:  dara.StringValue(item.ProductName),
			productCode:  dara.StringValue(item.PipCode),
			instanceID:   dara.StringValue(item.InstanceID),
			subType:      dara.StringValue(item.SubscriptionType),
			pretaxAmount: dara.Float32Value(item.PretaxAmount),
		})
	}

	// 按金额降序
	sort.Slice(result, func(i, j int) bool {
		return result[i].pretaxAmount > result[j].pretaxAmount
	})

	return result
}

// ─── 视图渲染 ───────────────────────────────────────────────

func (m model) View() string {
	var b strings.Builder

	switch m.view {
	case "input":
		b.WriteString(m.viewInput())
	case "loading":
		b.WriteString(m.viewLoading())
	case "data":
		b.WriteString(m.viewData())
	}

	b.WriteString("\n")
	return b.String()
}

func (m model) viewInput() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("阿里云账单查询"))
	b.WriteString("\n\n")
	b.WriteString(subtitleStyle.Render("请输入查询月份 (YYYY-MM 格式):"))
	b.WriteString("\n\n")

	cursor := "█"
	if !m.cursorOn {
		cursor = " "
	}
	inputBox := inputStyle.Render("月份 > " + m.inputValue + cursor)
	b.WriteString(inputBox)
	b.WriteString("\n\n")

	if m.errorMsg != "" {
		b.WriteString(errorStyle.Render("错误: " + m.errorMsg))
		b.WriteString("\n\n")
	}

	b.WriteString(helpStyle.Render("按 Enter 查询 | Ctrl+C/q 退出"))
	return b.String()
}

func (m model) viewLoading() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("阿里云账单查询"))
	b.WriteString("\n\n")
	b.WriteString("加载中... " + m.statusMsg)
	return b.String()
}

func (m model) viewData() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("阿里云账单查询 - " + m.month))
	b.WriteString("\n")

	// 标签页
	tabs := []string{"每日消费", "消费项明细", "流量包"}
	for i, tab := range tabs {
		if i == m.activeTab {
			b.WriteString(tabActiveStyle.Render(tab))
		} else {
			b.WriteString(tabInactiveStyle.Render(tab))
		}
		if i < len(tabs)-1 {
			b.WriteString(" ")
		}
	}
	b.WriteString("\n\n")

	if m.errorMsg != "" {
		b.WriteString(errorStyle.Render(m.errorMsg))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("按 Esc 重新输入"))
		return b.String()
	}

	// 渲染当前标签页内容
	if m.activeTab == 0 {
		b.WriteString(m.renderDailyTable())
	} else if m.activeTab == 1 {
		b.WriteString(m.renderItemTable())
	} else {
		b.WriteString(m.renderTrafficTable())
	}

	b.WriteString("\n\n")
	help := "←→切换 | d/f上月/下月 | r刷新 | q退出"
	if m.activeTab == 2 && !m.showTrafficDetails {
		help = "←→切换 | ↑↓选择流量包 | Enter查看明细 | d/f切换月份 | r刷新 | q退出"
	}
	if m.showTrafficDetails {
		help = "Esc返回流量包列表 | r刷新明细 | q退出"
	}
	b.WriteString(helpStyle.Render(help))
	return b.String()
}

func (m model) renderDailyTable() string {
	if len(m.dailyData) == 0 {
		return subtitleStyle.Render("本月无消费记录")
	}

	type dayGroup struct {
		date     string
		products []dayProduct
		dayTotal float32
	}
	var days []dayGroup
	dayMap := make(map[string]int)
	for _, dp := range m.dailyData {
		idx, ok := dayMap[dp.date]
		if !ok {
			idx = len(days)
			dayMap[dp.date] = idx
			days = append(days, dayGroup{date: dp.date})
		}
		days[idx].products = append(days[idx].products, dp)
		days[idx].dayTotal += dp.pretaxAmount
	}

	const colDate = 10
	const colName = 28
	const colAmt = 10

	var b strings.Builder

	for _, day := range days {
		for i, p := range day.products {
			dateStr := ""
			if i == 0 {
				dateStr = p.date
			}
			nameStr := truncateRight(p.productName, colName)
			b.WriteString(padRight(dateStr, colDate))
			b.WriteString("  ")
			b.WriteString(padRight(nameStr, colName))
			b.WriteString("  ")
			b.WriteString(fmt.Sprintf("¥%.2f", p.pretaxAmount))
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat(" ", colDate))
		b.WriteString("  ")
		b.WriteString(padRight("小计", colName))
		b.WriteString("  ")
		b.WriteString(fmt.Sprintf("¥%.2f", day.dayTotal))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(padRight("月合计", colDate))
	b.WriteString("  ")
	b.WriteString(padRight(fmt.Sprintf("%d天", len(days)), colName))
	b.WriteString("  ")
	b.WriteString(fmt.Sprintf("¥%.2f", m.totalAmount))
	b.WriteString("\n")

	return b.String()
}

func (m model) renderItemTable() string {
	if len(m.itemData) == 0 {
		return subtitleStyle.Render("本月无消费项记录")
	}

	type productSummary struct {
		productName string
		productCode string
		totalAmount float32
		count       int
		subType     string
	}
	products := make(map[string]*productSummary)

	for _, item := range m.itemData {
		code := item.productCode
		if code == "" {
			code = "其他"
		}
		name := item.productName
		if name == "" {
			name = code
		}
		subType := item.subType
		if subType == "" {
			subType = "-"
		}

		if p, ok := products[code]; ok {
			p.totalAmount += item.pretaxAmount
			p.count++
		} else {
			products[code] = &productSummary{
				productName: name,
				productCode: code,
				totalAmount: item.pretaxAmount,
				count:       1,
				subType:     subType,
			}
		}
	}

	type kv struct {
		key   string
		value *productSummary
	}
	sorted := make([]kv, 0, len(products))
	for k, v := range products {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].value.totalAmount > sorted[j].value.totalAmount
	})

	tw := getTermWidth()
	if tw < 42 {
		tw = 42
	}

	const colAmt = 12
	const colCnt = 5
	const gap = 2
	colCode := 10
	colType := 10
	colName := tw - colCode - colType - colAmt - colCnt - gap*4
	if colName < 8 {
		colName = 8
	}

	var b strings.Builder

	// 分页
	end := m.itemOffset + m.pageSize
	if end > len(sorted) {
		end = len(sorted)
	}
	pageItems := sorted[m.itemOffset:end]

	var pageTotal float32
	for _, s := range pageItems {
		v := s.value
		row := padRight(truncateRight(v.productName, colName), colName)
		row += strings.Repeat(" ", gap)
		row += padRight(truncateRight(v.productCode, colCode), colCode)
		row += strings.Repeat(" ", gap)
		row += padRight(v.subType, colType)
		row += strings.Repeat(" ", gap)
		row += padLeft(fmt.Sprintf("¥%.2f", v.totalAmount), colAmt)
		row += strings.Repeat(" ", gap)
		row += padLeft(fmt.Sprintf("%d", v.count), colCnt)
		b.WriteString(row + "\n")
		pageTotal += v.totalAmount
	}

	b.WriteString(strings.Repeat("─", tw))
	b.WriteString("\n")
	sumRow := padRight("本页", colName)
	sumRow += strings.Repeat(" ", gap)
	sumRow += padRight("", colCode)
	sumRow += strings.Repeat(" ", gap)
	sumRow += padRight("", colType)
	sumRow += strings.Repeat(" ", gap)
	sumRow += padLeft(fmt.Sprintf("¥%.2f", pageTotal), colAmt)
	sumRow += strings.Repeat(" ", gap)
	sumRow += padLeft(fmt.Sprintf("%d/%d", m.itemOffset+1, len(sorted)), colCnt)
	b.WriteString(sumRow + "\n")

	return b.String()
}

// ─── 数据获取函数 ───────────────────────────────────────────

func fetchAllBills(client *bssopenapi.Client, billingCycle string) ([]*bssopenapi.QueryBillResponseBodyDataItemsItem, error) {
	var allItems []*bssopenapi.QueryBillResponseBodyDataItemsItem
	pageNum := int32(1)
	pageSize := int32(300)

	for {
		req := &bssopenapi.QueryBillRequest{
			BillingCycle: dara.String(billingCycle),
			PageNum:      dara.Int32(pageNum),
			PageSize:     dara.Int32(pageSize),
		}

		resp, err := client.QueryBill(req)
		if err != nil {
			return nil, fmt.Errorf("QueryBill 调用失败: %w", err)
		}

		if resp.Body == nil || !dara.BoolValue(resp.Body.Success) {
			code := dara.StringValue(resp.Body.Code)
			msg := dara.StringValue(resp.Body.Message)
			return nil, fmt.Errorf("QueryBill 返回错误: code=%s, message=%s", code, msg)
		}

		if resp.Body.Data != nil && resp.Body.Data.Items != nil {
			allItems = append(allItems, resp.Body.Data.Items.Item...)
		}

		if resp.Body.Data == nil || len(resp.Body.Data.Items.Item) < int(pageSize) {
			break
		}
		pageNum++
	}

	return allItems, nil
}

func fetchAllInstanceBills(client *bssopenapi.Client, billingCycle string) ([]*bssopenapi.DescribeInstanceBillResponseBodyDataItems, error) {
	var allItems []*bssopenapi.DescribeInstanceBillResponseBodyDataItems
	nextToken := ""
	maxResults := int32(300)

	for {
		req := &bssopenapi.DescribeInstanceBillRequest{
			BillingCycle: dara.String(billingCycle),
			Granularity:  dara.String("MONTHLY"),
			MaxResults:   dara.Int32(maxResults),
		}
		if nextToken != "" {
			req.NextToken = dara.String(nextToken)
		}

		resp, err := client.DescribeInstanceBill(req)
		if err != nil {
			return nil, fmt.Errorf("DescribeInstanceBill 调用失败: %w", err)
		}

		if resp.Body == nil || !dara.BoolValue(resp.Body.Success) {
			code := dara.StringValue(resp.Body.Code)
			msg := dara.StringValue(resp.Body.Message)
			return nil, fmt.Errorf("DescribeInstanceBill 返回错误: code=%s, message=%s", code, msg)
		}

		if resp.Body.Data != nil && resp.Body.Data.Items != nil {
			allItems = append(allItems, resp.Body.Data.Items...)
		}

		nextToken = dara.StringValue(resp.Body.Data.NextToken)
		if nextToken == "" || resp.Body.Data == nil || len(resp.Body.Data.Items) < int(maxResults) {
			break
		}
	}

	return allItems, nil
}

// ─── 辅助函数 ───────────────────────────────────────────────

// padRight pads s with spaces on the right to reach width n.
func padRight(s string, n int) string {
	w := stringWidth(s)
	if w >= n {
		return truncateRunes(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// padLeft pads s with spaces on the left to reach width n.
func padLeft(s string, n int) string {
	w := stringWidth(s)
	if w >= n {
		return truncateRunes(s, n)
	}
	return strings.Repeat(" ", n-w) + s
}

// truncateRunes truncates s to at most n display columns.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	w := 0
	end := 0
	for i, r := range runes {
		cw := runeWidth(r)
		if w+cw > n {
			break
		}
		w += cw
		end = i + 1
	}
	return string(runes[:end])
}

// truncateRight truncates s to at most n display columns, adding "…" if cut.
func truncateRight(s string, n int) string {
	w := stringWidth(s)
	if w <= n {
		return s
	}
	runes := []rune(s)
	acc := 0
	end := 0
	for i, r := range runes {
		cw := runeWidth(r)
		if acc+cw > n-1 {
			break
		}
		acc += cw
		end = i + 1
	}
	return string(runes[:end]) + "…"
}

// runeWidth returns the display width of a single rune.
func runeWidth(r rune) int {
	if r > 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A ||
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE19) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x20000 && r <= 0x2FFFD) ||
		(r >= 0x30000 && r <= 0x3FFFD)) {
		return 2
	}
	return 1
}

// stringWidth returns the display width of a string (CJK chars count as 2).
func stringWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// getTermWidth returns the terminal width, or 80 as default.
func getTermWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

func isValidMonth(s string) bool {
	if len(s) != 7 || s[4] != '-' {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return false
	}
	year := parts[0]
	month := parts[1]
	if len(year) != 4 || len(month) != 2 {
		return false
	}
	for _, c := range year + month {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func createClient() (*bssopenapi.Client, error) {
	accessKeyID := os.Getenv("ALI_KEY_ID")
	accessKeySecret := os.Getenv("ALI_KEY_SECRET")

	if accessKeyID == "" || accessKeySecret == "" {
		return nil, fmt.Errorf("请设置环境变量 ALI_KEY_ID 和 ALI_KEY_SECRET")
	}

	config := &openapi.Config{
		AccessKeyId:     dara.String(accessKeyID),
		AccessKeySecret: dara.String(accessKeySecret),
		Endpoint:        dara.String("business.aliyuncs.com"),
	}

	return bssopenapi.NewClient(config)
}

// ─── 主函数 ────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 && os.Args[1] == "xbar" {
		if err := runXbar(); err != nil {
			fmt.Printf("阿里云查询失败 | color=red\n---\n%s\n", err)
			os.Exit(1)
		}
		return
	}

	m := initialModel()

	// If a month argument is given, auto-query
	if len(os.Args) > 1 {
		month := os.Args[1]
		if isValidMonth(month) {
			m.inputValue = month
			client, err := createClient()
			if err != nil {
				fmt.Fprintf(os.Stderr, "认证失败: %v\n", err)
				os.Exit(1)
			}
			m.client = client
			m.view = "loading"
			m.month = month
			m.statusMsg = fmt.Sprintf("正在查询 %s 账单数据...", month)

			// Sync load in main
			res := loadDataSync(client, month)
			if res.err != nil {
				fmt.Fprintf(os.Stderr, "查询失败: %v\n", res.err)
				os.Exit(1)
			}
			if len(res.daily) == 0 && len(res.items) == 0 && len(res.trafficPackages) == 0 {
				fmt.Fprintln(os.Stderr, "该月份无账单数据")
			}
			m.dailyData = res.daily
			m.itemData = res.items
			m.totalAmount = res.total
			m.trafficPackages = res.trafficPackages
			if res.trafficErr != nil {
				m.trafficError = res.trafficErr.Error()
			}
			m.view = "data"
		} else {
			fmt.Fprintf(os.Stderr, "月份格式错误: %s (应为 YYYY-MM)\n", month)
			os.Exit(1)
		}
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "程序异常退出: %v\n", err)
		os.Exit(1)
	}
}

type loadResult struct {
	daily           []dayProduct
	items           []billItem
	total           float32
	trafficPackages []trafficPackage
	trafficErr      error
	err             error
}

func loadDataSync(client *bssopenapi.Client, month string) loadResult {
	type billsResult struct {
		items []*bssopenapi.QueryBillResponseBodyDataItemsItem
		err   error
	}
	type instanceBillsResult struct {
		items []*bssopenapi.DescribeInstanceBillResponseBodyDataItems
		err   error
	}
	type trafficPackagesResult struct {
		items []trafficPackage
		err   error
	}

	billsCh := make(chan billsResult, 1)
	instanceBillsCh := make(chan instanceBillsResult, 1)
	trafficPackagesCh := make(chan trafficPackagesResult, 1)
	go func() {
		items, err := fetchAllBills(client, month)
		billsCh <- billsResult{items: items, err: err}
	}()
	go func() {
		items, err := fetchAllInstanceBills(client, month)
		instanceBillsCh <- instanceBillsResult{items: items, err: err}
	}()
	go func() {
		items, err := fetchAllTrafficPackages(client)
		trafficPackagesCh <- trafficPackagesResult{items: items, err: err}
	}()

	bills := <-billsCh
	if bills.err != nil {
		return loadResult{err: bills.err}
	}
	instanceBills := <-instanceBillsCh
	if instanceBills.err != nil {
		return loadResult{err: instanceBills.err}
	}
	trafficResult := <-trafficPackagesCh

	daily := aggregateDaily(bills.items)
	items := convertItems(instanceBills.items)

	var total float32
	for _, d := range daily {
		total += d.pretaxAmount
	}

	return loadResult{
		daily:           daily,
		items:           items,
		total:           total,
		trafficPackages: trafficResult.items,
		trafficErr:      trafficResult.err,
	}
}
