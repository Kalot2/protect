package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/adshao/go-binance/v2/futures"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"image/color"
)

type Config struct {
	APIKey    string `json:"api_key"`
	SecretKey string `json:"secret_key"`
	TakeProfit struct {
		Long  float64 `json:"LONG"`
		Short float64 `json:"SHORT"`
	} `json:"take_profit"`
}

type Kline struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

type TraderUI struct {
	app          fyne.App
	window       fyne.Window
	client       *futures.Client
	currentPriceLabel *widget.Label
	klineChart   *canvas.Image
	analysisLabel *widget.Label
	positionsList *widget.List
	ordersList   *widget.List
	positions    binding.UntypedList
	orders       binding.UntypedList
	klines       []Kline
	currentPrice float64

	// 下单表单
	sideSelect   *widget.Select
	priceEntry   *widget.Entry
	amountEntry  *widget.Entry
	stopLossEntry *widget.Entry

	// 跟踪最高盈利
	maxProfit map[string]float64
}

func (ui *TraderUI) initUI() {
	// 恢复默认颜色主题
	ui.app.Settings().SetTheme(theme.DefaultTheme())

	// 创建价格显示
	priceLabel := widget.NewLabelWithStyle("当前价格", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	ui.currentPriceLabel = widget.NewLabelWithStyle("加载中...", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true, Bold: true})

	priceCard := widget.NewCard("", "", container.NewVBox(
		priceLabel,
		ui.currentPriceLabel,
	))

	// 创建下单表单
	ui.sideSelect = widget.NewSelect([]string{"买入做多", "卖出做空"}, nil)
	ui.sideSelect.SetSelected("买入做多")

	ui.priceEntry = widget.NewEntry()
	ui.priceEntry.SetPlaceHolder("输入价格")
	ui.priceEntry.TextStyle = fyne.TextStyle{Monospace: true}

	ui.amountEntry = widget.NewEntry()
	ui.amountEntry.SetPlaceHolder("输入数量")
	ui.amountEntry.TextStyle = fyne.TextStyle{Monospace: true}

	ui.stopLossEntry = widget.NewEntry()
	ui.stopLossEntry.SetPlaceHolder("输入止损价格")
	ui.stopLossEntry.TextStyle = fyne.TextStyle{Monospace: true}

	submitBtn := widget.NewButton("下单", func() {
		ui.submitOrder()
	})
	submitBtn.Importance = widget.HighImportance  // 高亮显示下单按钮

	orderForm := widget.NewCard("", "", container.NewVBox(  // 使用Card包装下单表单
		widget.NewLabelWithStyle("下单", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewGridWithColumns(2,
			widget.NewLabelWithStyle("方向", fyne.TextAlignTrailing, fyne.TextStyle{}),
			ui.sideSelect,
			widget.NewLabelWithStyle("价格", fyne.TextAlignTrailing, fyne.TextStyle{}),
			ui.priceEntry,
			widget.NewLabelWithStyle("数量", fyne.TextAlignTrailing, fyne.TextStyle{}),
			ui.amountEntry,
			widget.NewLabelWithStyle("止损价格", fyne.TextAlignTrailing, fyne.TextStyle{}),
			ui.stopLossEntry,
		),
		container.NewPadded(submitBtn),  // 添加padding使按钮更突出
	))

	// 创建K线图显示
	ui.klineChart = &canvas.Image{
		FillMode: canvas.ImageFillOriginal,
	}
	ui.klineChart.SetMinSize(fyne.NewSize(180, 120))  // 缩小到原来的60%

	// 创建分析区域
	ui.analysisLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	ui.analysisLabel.Wrapping = fyne.TextWrapBreak
	analysisScroll := container.NewVScroll(ui.analysisLabel)
	analysisScroll.SetMinSize(fyne.NewSize(180, 213))  // 增加三分之一（160 * 1.33 ≈ 213）

	chartContainer := widget.NewCard("价格走势", "", container.NewVBox(
		widget.NewSeparator(),
		container.NewVBox(
			container.NewPadded(ui.klineChart),
			widget.NewCard(
				"技术分析",
				"",
				analysisScroll,
			),
		),
	))

	// 创建持仓列表
	ui.positions = binding.NewUntypedList()
	ui.positionsList = widget.NewListWithData(
		ui.positions,
		func() fyne.CanvasObject {
			return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			if val, err := i.(binding.Untyped).Get(); err == nil {
				o.(*widget.Label).SetText(val.(string))
			}
		},
	)

	// 创建订单列表
	ui.orders = binding.NewUntypedList()
	ui.ordersList = widget.NewListWithData(
		ui.orders,
		func() fyne.CanvasObject {
			return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			if val, err := i.(binding.Untyped).Get(); err == nil {
				o.(*widget.Label).SetText(val.(string))
			}
		},
	)
	ui.ordersList.OnSelected = ui.handleOrderClick

	// 创建持仓和订单列表
	positionsScroll := container.NewVScroll(ui.positionsList)
	positionsScroll.SetMinSize(fyne.NewSize(100, 150))  // 设置滚动区域最小尺寸
	positionsCard := widget.NewCard(
		"持仓", 
		"", 
		positionsScroll,
	)
	positionsCard.Resize(fyne.NewSize(0, 100))  // 设置卡片尺寸

	ordersScroll := container.NewVScroll(ui.ordersList)
	ordersScroll.SetMinSize(fyne.NewSize(100, 100))  // 设置滚动区域最小尺寸
	ordersCard := widget.NewCard(
		"订单", 
		"", 
		ordersScroll,
	)
	ordersCard.Resize(fyne.NewSize(0, 50))  // 设置卡片尺寸

	// 创建右侧面板
	rightPanel := container.NewVBox(
		priceCard,
		orderForm,
		container.NewGridWithRows(2,  // 使用网格布局并排显示持仓和订单
			positionsCard,
			ordersCard,
		),
	)
	rightContainer := container.NewHBox(
		rightPanel,
		widget.NewSeparator(),
		container.NewPadded(widget.NewLabel("")),  // 添加一个空白区域来控制宽度
	)
	rightContainer.Resize(fyne.NewSize(350, 0))  // 限制右侧面板宽度

	// 创建主布局
	content := container.NewHSplit(
		chartContainer,
		rightContainer,
	)
	content.SetOffset(0.65)  // 让右侧面板占35%

	// 设置窗口内容和大小
	ui.window.Resize(fyne.NewSize(800, 700))
	ui.window.SetContent(content)

	// 启动数据更新
	ui.startDataUpdater()
}

func (ui *TraderUI) submitOrder() {
	side := futures.SideTypeBuy
	if ui.sideSelect.Selected == "卖出做空" {
		side = futures.SideTypeSell
	}

	price := ui.priceEntry.Text
	quantity := ui.amountEntry.Text
	stopLoss := ui.stopLossEntry.Text

	// 创建主订单
	order, err := ui.client.NewCreateOrderService().
		Symbol("SOLUSDC").
		Side(side).
		PositionSide("BOTH").  // 双向持仓模式
		Type(futures.OrderTypeLimit).
		TimeInForce(futures.TimeInForceTypeGTC).
		Price(price).
		Quantity(quantity).
		Do(context.Background())

	if err != nil {
		dialog.ShowError(err, ui.window)
		return
	}

	// 如果设置了止损价格，创建止损单
	if stopLoss != "" {
		stopSide := futures.SideTypeSell
		if side == futures.SideTypeSell {
			stopSide = futures.SideTypeBuy
		}

		_, err = ui.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(stopSide).
			PositionSide("BOTH").
			Type(futures.OrderTypeStopMarket).
			TimeInForce(futures.TimeInForceTypeGTC).
			StopPrice(stopLoss).
			Quantity(quantity).
			Do(context.Background())

		if err != nil {
			dialog.ShowError(fmt.Errorf("主订单已成功，但止损单创建失败: %v", err), ui.window)
			return
		}
	}

	dialog.ShowInformation("下单成功", fmt.Sprintf("订单ID: %d", order.OrderID), ui.window)
}

func (ui *TraderUI) updateKlines() error {
	klines, err := ui.client.NewKlinesService().
		Symbol("SOLUSDC").
		Interval("5m").        // 使用5分钟K线
		Limit(50).            // 获取50根K线
		Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取K线数据失败: %v", err)
	}

	// 将K线数据转换为我们的格式
	ui.klines = make([]Kline, len(klines))
	for i, k := range klines {
		open, _ := strconv.ParseFloat(k.Open, 64)
		high, _ := strconv.ParseFloat(k.High, 64)
		low, _ := strconv.ParseFloat(k.Low, 64)
		close, _ := strconv.ParseFloat(k.Close, 64)
		volume, _ := strconv.ParseFloat(k.Volume, 64)
		ui.klines[i] = Kline{
			Time:   time.Unix(k.OpenTime/1000, 0),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
		}
	}

	// 创建一个新的图表
	p := plot.New()

	p.Title.Text = "SOL/USDC K线图"
	p.X.Label.Text = "时间"
	p.Y.Label.Text = "价格"

	// 设置图表边距和样式
	p.X.Padding = 0
	p.Y.Padding = 0
	p.X.Min = -1
	p.X.Max = float64(len(ui.klines))

	// 计算价格范围
	minPrice := ui.klines[0].Low
	maxPrice := ui.klines[0].High
	for _, k := range ui.klines {
		if k.Low < minPrice {
			minPrice = k.Low
		}
		if k.High > maxPrice {
			maxPrice = k.High
		}
	}
	padding := (maxPrice - minPrice) * 0.01
	p.Y.Min = minPrice - padding
	p.Y.Max = maxPrice + padding

	candlePlotter := &CandlePlotter{
		Klines: ui.klines,
		Width:  0.8,
	}

	p.Add(candlePlotter)

	// 设置更多的X轴时间标签
	ticks := make([]plot.Tick, 5)
	for i := 0; i < 5; i++ {
		pos := float64(i) * float64(len(ui.klines)-1) / 4
		idx := int(pos)
		if idx >= len(ui.klines) {
			idx = len(ui.klines) - 1
		}
		ticks[i] = plot.Tick{
			Value: pos,
			Label: ui.klines[idx].Time.Format("15:04"),
		}
	}
	p.X.Tick.Marker = plot.ConstantTicks(ticks)

	// 创建一个临时文件来保存图表
	tmpFile, err := os.CreateTemp("", "kline-*.png")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// 保存图表到临时文件
	if err := p.Save(9.6*vg.Inch, 5.4*vg.Inch, tmpFile.Name()); err != nil {  // 缩小到原来的60%
		return fmt.Errorf("保存K线图失败: %v", err)
	}

	// 读取临时文件内容
	imgData, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("读取K线图失败: %v", err)
	}

	// 在UI线程中更新图表
	fyne.Do(func() {
		ui.klineChart.Resource = fyne.NewStaticResource("kline.png", imgData)
		ui.klineChart.Refresh()
	})

	// 更新技术分析
	analysis := ui.analyzeKlines(ui.klines)
	fyne.Do(func() {
		ui.analysisLabel.SetText(analysis)
	})

	return nil
}

func (ui *TraderUI) analyzeKlines(klines []Kline) string {
	if len(klines) < 2 {
		return "数据不足以进行分析"
	}

	var analysis strings.Builder

	// 计算涨跌幅
	lastClose := klines[len(klines)-1].Close
	prevClose := klines[len(klines)-2].Close
	change := (lastClose - prevClose) / prevClose * 100

	// 计算成交量变化
	lastVol := klines[len(klines)-1].Volume
	prevVol := klines[len(klines)-2].Volume
	volChange := (lastVol - prevVol) / prevVol * 100

	// 计算RSI
	rsi := ui.calculateRSI(klines, 14)

	analysis.WriteString(fmt.Sprintf("24h涨跌幅: %.2f%%\n", change))
	analysis.WriteString(fmt.Sprintf("成交量变化: %.2f%%\n", volChange))
	analysis.WriteString(fmt.Sprintf("RSI(14): %.2f\n\n", rsi))

	// 添加简单分析结论
	analysis.WriteString("市场分析:\n")
	if change > 0 {
		analysis.WriteString("- 价格呈上涨趋势\n")
	} else {
		analysis.WriteString("- 价格呈下跌趋势\n")
	}

	if volChange > 0 {
		analysis.WriteString("- 成交量放大，市场活跃度增加\n")
	} else {
		analysis.WriteString("- 成交量萎缩，市场活跃度下降\n")
	}

	if rsi > 70 {
		analysis.WriteString("- RSI超买，可能存在回调风险\n")
	} else if rsi < 30 {
		analysis.WriteString("- RSI超卖，可能存在反弹机会\n")
	} else {
		analysis.WriteString("- RSI处于中性区间\n")
	}

	return analysis.String()
}

func (ui *TraderUI) calculateRSI(klines []Kline, period int) float64 {
	if len(klines) < period+1 {
		return 50 // 数据不足时返回中性值
	}

	var gains, losses float64
	for i := len(klines) - period; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses -= change
		}
	}

	if losses == 0 {
		return 100
	}

	rs := gains / losses
	return 100 - (100 / (1 + rs))
}

func (ui *TraderUI) loadConfig() (*Config, error) {
	data, err := os.ReadFile("config.json")
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}

	if config.APIKey == "" || config.SecretKey == "" {
		return nil, fmt.Errorf("请在config.json中填写API密钥")
	}

	return &config, nil
}

func (ui *TraderUI) NewTraderUI() (*TraderUI, error) {
	config, err := ui.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %v", err)
	}

	// 创建期货客户端，使用期货的API接口
	futuresClient := futures.NewClient(config.APIKey, config.SecretKey)

	a := app.New()
	w := a.NewWindow("币安期货交易")

	ui.app = a
	ui.window = w
	ui.client = futuresClient
	ui.positions = binding.NewUntypedList()
	ui.orders = binding.NewUntypedList()
	ui.maxProfit = make(map[string]float64)

	// 初始化UI组件
	ui.initUI()

	return ui, nil
}

func (ui *TraderUI) getCurrentPrice() (float64, error) {
	ticker, err := ui.client.NewPremiumIndexService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return 0, fmt.Errorf("获取价格失败: %v", err)
	}
	if len(ticker) == 0 {
		return 0, fmt.Errorf("未找到SOLUSDC的价格")
	}
	price, err := strconv.ParseFloat(ticker[0].MarkPrice, 64)
	if err != nil {
		return 0, fmt.Errorf("解析价格失败: %v", err)
	}
	return price, nil
}

func (ui *TraderUI) updatePrice() error {
	price, err := ui.getCurrentPrice()
	if err != nil {
		return err
	}

	ui.currentPrice = price
	fyne.Do(func() {
		ui.currentPriceLabel.SetText(fmt.Sprintf("%.4f USDC", price))
	})
	return nil
}

func (ui *TraderUI) checkAndSetTakeProfit(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		return nil
	}

	// 获取当前订单
	orders, err := ui.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 检查是否已有止盈单
	hasTakeProfit := false
	entryPrice, _ := strconv.ParseFloat(position.EntryPrice, 64)
	for _, order := range orders {
		if (amt > 0 && order.Side == futures.SideTypeSell && order.Type == futures.OrderTypeLimit) ||
			(amt < 0 && order.Side == futures.SideTypeBuy && order.Type == futures.OrderTypeLimit) {
			hasTakeProfit = true
			break
		}
	}

	// 如果没有止盈单，创建一个
	if !hasTakeProfit {
		side := futures.SideTypeSell
		positionSide := futures.PositionSideTypeLong
		var price float64

		if amt > 0 {
			// 多仓，止盈价格在入场价上方200点
			price = entryPrice + 2.0  // 2.0 = 200点/100
			side = futures.SideTypeSell
			positionSide = futures.PositionSideTypeLong
		} else {
			// 空仓，止盈价格在入场价下方200点
			price = entryPrice - 2.0  // 2.0 = 200点/100
			side = futures.SideTypeBuy
			positionSide = futures.PositionSideTypeShort
		}

		// 将价格四舍五入到0.01（SOL的最小价格单位）
		price = roundToTickSize(price, 0.01)

		// 创建限价止盈单
		_, err := ui.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeLimit).
			TimeInForce(futures.TimeInForceTypeGTC).  // GTC: Good Till Cancel
			Price(fmt.Sprintf("%.2f", price)).  // 使用2位小数
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Do(context.Background())
		
		if err != nil {
			return fmt.Errorf("创建止盈单失败: %v", err)
		}
	}

	return nil
}

func (ui *TraderUI) checkAndSetStopLoss(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		return nil
	}

	// 获取当前止损订单
	orders, err := ui.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 检查是否已有止损单
	hasStopLoss := false
	entryPrice, _ := strconv.ParseFloat(position.EntryPrice, 64)
	for _, order := range orders {
		if (amt > 0 && order.Side == futures.SideTypeSell && order.Type == futures.OrderTypeStopMarket) ||
			(amt < 0 && order.Side == futures.SideTypeBuy && order.Type == futures.OrderTypeStopMarket) {
			hasStopLoss = true
			break
		}
	}

	// 如果没有止损单，创建一个
	if !hasStopLoss {
		stopPrice := entryPrice
		side := futures.SideTypeSell
		positionSide := futures.PositionSideTypeLong
		if amt > 0 {
			// 多仓，止损价格在入场价下方100点
			stopPrice = entryPrice - 1.0  // 1.0 = 100点/100
			side = futures.SideTypeSell
			positionSide = futures.PositionSideTypeLong
		} else {
			// 空仓，止损价格在入场价上方100点
			stopPrice = entryPrice + 1.0  // 1.0 = 100点/100
			side = futures.SideTypeBuy
			positionSide = futures.PositionSideTypeShort
		}

		// 将价格四舍五入到0.01
		stopPrice = roundToTickSize(stopPrice, 0.01)

		// 创建止损市价单
		_, err := ui.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).  // 设置持仓方向
			Type(futures.OrderTypeStopMarket).
			StopPrice(fmt.Sprintf("%.2f", stopPrice)).  // 使用2位小数
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Do(context.Background())
		
		if err != nil {
			return fmt.Errorf("创建止损单失败: %v", err)
		}
	}

	return nil
}

func (ui *TraderUI) checkProtectiveStopProfit(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		delete(ui.maxProfit, position.Symbol)  // 清除记录
		return nil
	}

	unPnl, _ := strconv.ParseFloat(position.UnRealizedProfit, 64)
	
	// 更新最高盈利
	if _, exists := ui.maxProfit[position.Symbol]; !exists {
		ui.maxProfit[position.Symbol] = unPnl
	} else if unPnl > ui.maxProfit[position.Symbol] {
		ui.maxProfit[position.Symbol] = unPnl
	}

	maxProfit := ui.maxProfit[position.Symbol]
	
	// 如果曾经盈利超过200U，且当前回撤超过50%，执行市价平仓
	if maxProfit >= 200 && unPnl <= maxProfit*0.5 {
		side := futures.SideTypeSell
		positionSide := futures.PositionSideTypeLong
		if amt < 0 {
			side = futures.SideTypeBuy
			positionSide = futures.PositionSideTypeShort
		}

		// 市价平仓
		_, err := ui.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeMarket).
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Do(context.Background())

		if err != nil {
			return fmt.Errorf("保护止盈平仓失败: %v", err)
		}

		// 平仓后清除记录
		delete(ui.maxProfit, position.Symbol)
	}

	return nil
}

func (ui *TraderUI) updatePositions() error {
	positions, err := ui.client.NewGetPositionRiskService().Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取持仓信息失败: %v", err)
	}

	var positionTexts []interface{}
	for _, p := range positions {
		if p.Symbol == "SOLUSDC" {
			// 检查保护止盈
			if err := ui.checkProtectiveStopProfit(p); err != nil {
				fmt.Printf("检查保护止盈失败: %v\n", err)
			}

			// 检查并设置止盈
			if err := ui.checkAndSetTakeProfit(p); err != nil {
				fmt.Printf("设置止盈失败: %v\n", err)
			}
			// 检查并设置止损
			if err := ui.checkAndSetStopLoss(p); err != nil {
				fmt.Printf("设置止损失败: %v\n", err)
			}

			amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
			if amt != 0 {
				entryPrice, _ := strconv.ParseFloat(p.EntryPrice, 64)
				unPnl, _ := strconv.ParseFloat(p.UnRealizedProfit, 64)
				
				// 获取止盈止损订单
				orders, err := ui.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
				if err != nil {
					fmt.Printf("获取订单失败: %v\n", err)
					continue
				}

				var tpPrice, slPrice float64
				for _, order := range orders {
					price, _ := strconv.ParseFloat(order.Price, 64)
					// 确定方向
					isLong := amt > 0
					
					// 多仓：
					// - 止盈应该是卖单(SELL)且价格高于入场价
					// - 止损应该是卖单(SELL)且价格低于入场价
					// 空仓：
					// - 止盈应该是买单(BUY)且价格低于入场价
					// - 止损应该是买单(BUY)且价格高于入场价
					if isLong {
						if order.Side == futures.SideTypeSell {
							if price > entryPrice {
								tpPrice = price
							} else {
								slPrice = price
							}
						}
					} else {
						if order.Side == futures.SideTypeBuy {
							if price < entryPrice {
								tpPrice = price
							} else {
								slPrice = price
							}
						}
					}
				}

				// 确定方向
				direction := "多"
				if amt < 0 {
					direction = "空"
				}

				// 格式化持仓信息
				text := fmt.Sprintf(
					"方向: %s\n数量: %.4f\n入场价: %.4f\n未实现盈亏: %.4f\n最高盈利: %.4f\n",
					direction, math.Abs(amt), entryPrice, unPnl, ui.maxProfit[p.Symbol],
				)
				
				// 添加止盈止损信息
				if tpPrice > 0 {
					text += fmt.Sprintf("止盈价: %.4f (%.1f点)\n", 
						tpPrice, math.Abs(tpPrice-entryPrice)*100)
				}
				if slPrice > 0 {
					text += fmt.Sprintf("止损价: %.4f (%.1f点)", 
						slPrice, math.Abs(slPrice-entryPrice)*100)
				}
				
				positionTexts = append(positionTexts, text)
			}
		}
	}

	if len(positionTexts) == 0 {
		positionTexts = append(positionTexts, "无持仓")
	}

	return ui.positions.Set(positionTexts)
}

func (ui *TraderUI) updateOrders() error {
	orders, err := ui.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	var orderTexts []interface{}
	for _, order := range orders {
		price := order.Price
		qty := order.OrigQuantity

		// 根据订单类型显示不同信息
		var priceInfo string
		if order.Type == futures.OrderTypeLimit {
			priceInfo = fmt.Sprintf("价格: %s", price)
		} else if order.Type == futures.OrderTypeStopMarket {
			priceInfo = fmt.Sprintf("触发价: %s", order.StopPrice)
		}

		// 添加取消按钮
		text := fmt.Sprintf("[x] %s %s@%s (%s)\n    订单号: %d",
			order.Side, qty, priceInfo, order.Type, order.OrderID)
		
		orderTexts = append(orderTexts, text)
	}

	if len(orderTexts) == 0 {
		orderTexts = append(orderTexts, "无挂单")
	}

	return ui.orders.Set(orderTexts)
}

func (ui *TraderUI) handleOrderClick(id widget.ListItemID) {
	if id < 0 {
		return
	}

	// 获取点击的订单文本
	val, err := ui.orders.GetValue(id)
	if err != nil {
		return
	}

	orderText := val.(string)
	if !strings.HasPrefix(orderText, "[x]") {
		return
	}

	// 提取订单号
	var orderId int64
	if _, err := fmt.Sscanf(orderText, "%*s %*s %*s %*s %*s %d", &orderId); err != nil {
		fmt.Printf("解析订单号失败: %v\n", err)
		return
	}

	// 确认取消
	dialog.ShowConfirm("取消订单", "确定要取消这个订单吗？", func(ok bool) {
		if !ok {
			return
		}
		// 取消订单
		_, err := ui.client.NewCancelOrderService().
			Symbol("SOLUSDC").
			OrderID(orderId).
			Do(context.Background())

		if err != nil {
			dialog.ShowError(fmt.Errorf("取消订单失败: %v", err), ui.window)
		}
	}, ui.window)
}

func (ui *TraderUI) startDataUpdater() {
	// 更新K线数据
	go func() {
		for {
			if err := ui.updateKlines(); err != nil {
				fmt.Printf("更新K线失败: %v\n", err)
			}
			time.Sleep(5 * time.Second)
		}
	}()

	// 更新价格和订单数据
	go func() {
		for {
			// 更新价格
			if err := ui.updatePrice(); err != nil {
				fmt.Printf("获取价格失败: %v\n", err)
			}

			// 更新持仓
			if err := ui.updatePositions(); err != nil {
				fmt.Printf("获取持仓失败: %v\n", err)
			}

			// 更新订单
			if err := ui.updateOrders(); err != nil {
				fmt.Printf("获取订单失败: %v\n", err)
			}

			time.Sleep(2 * time.Second)
		}
	}()
}

func (ui *TraderUI) Show() {
	ui.window.ShowAndRun()
}

func main() {
	ui, err := NewTraderUI()
	if err != nil {
		fmt.Println(err)
		return
	}
	ui.Show()
}

type CandlePlotter struct {
	Klines []Kline
	Width  float64
}

func (cp *CandlePlotter) Plot(c draw.Canvas, p *plot.Plot) {
	trX, trY := p.Transforms(&c)

	for i, k := range cp.Klines {
		x := float64(i)

		// 转换坐标
		xmin := trX(x - cp.Width/2)
		xmax := trX(x + cp.Width/2)
		yopen := trY(k.Open)
		yclose := trY(k.Close)
		yhigh := trY(k.High)
		ylow := trY(k.Low)

		var path vg.Path

		// 画影线
		var vs []vg.Point
		vs = append(vs, vg.Point{X: trX(x), Y: yhigh})
		vs = append(vs, vg.Point{X: trX(x), Y: ylow})
		c.StrokeLines(draw.LineStyle{
			Color: color.Black,
			Width: vg.Points(0.5),
		}, vs)

		// 画蜡烛体
		path.Move(vg.Point{X: xmin, Y: yopen})
		path.Line(vg.Point{X: xmax, Y: yopen})
		path.Line(vg.Point{X: xmax, Y: yclose})
		path.Line(vg.Point{X: xmin, Y: yclose})
		path.Close()

		// 根据涨跌设置填充颜色
		if k.Close >= k.Open {
			// 阳线：白色填充
			c.SetColor(color.White)
		} else {
			// 阴线：黑色填充
			c.SetColor(color.Black)
		}
		c.Fill(path)

		// 统一使用黑色边框
		c.SetColor(color.Black)
		c.Stroke(path)
	}
}

func (cp *CandlePlotter) DataRange() (xmin, xmax, ymin, ymax float64) {
	xmin = -1
	xmax = float64(len(cp.Klines))

	ymin = cp.Klines[0].Low
	ymax = cp.Klines[0].High

	for _, k := range cp.Klines {
		if k.Low < ymin {
			ymin = k.Low
		}
		if k.High > ymax {
			ymax = k.High
		}
	}

	// 添加5%的边距
	padding := (ymax - ymin) * 0.05
	ymin -= padding
	ymax += padding

	return
}

func NewTraderUI() (*TraderUI, error) {
	ui := &TraderUI{}
	return ui.NewTraderUI()
}

// 四舍五入到指定精度
func roundToTickSize(price float64, tickSize float64) float64 {
	return math.Round(price/tickSize) * tickSize
}
