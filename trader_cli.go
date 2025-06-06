package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
)

type TraderCLI struct {
	client     *futures.Client
	maxProfit  map[string]float64
	positions  map[string]float64
	lastPosition map[string]*futures.PositionRisk
	lastUpdate   map[string]time.Time
}

func NewTraderCLI(apiKey, secretKey string) (*TraderCLI, error) {
	client := binance.NewFuturesClient(apiKey, secretKey)
	
	return &TraderCLI{
		client:     client,
		maxProfit:  make(map[string]float64),
		positions:  make(map[string]float64),
		lastPosition: make(map[string]*futures.PositionRisk),
		lastUpdate:   make(map[string]time.Time),
	}, nil
}

// 取消所有止盈止损单
func (t *TraderCLI) cancelAllTPSL(currentAmt float64) error {
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	for _, order := range orders {
		// 检查是否是止盈止损单
		if (order.Type == futures.OrderTypeLimit && order.ReduceOnly) || order.Type == futures.OrderTypeStopMarket {
			// 如果指定了当前仓位，检查订单数量是否匹配
			if currentAmt != 0 {
				qty, _ := strconv.ParseFloat(order.OrigQuantity, 64)
				// 如果订单数量与当前仓位相同，跳过
				if math.Abs(qty - math.Abs(currentAmt)) <= 0.0001 {
					continue
				}
			}

			_, err := t.client.NewCancelOrderService().
				Symbol("SOLUSDC").
				OrderID(order.OrderID).
				Do(context.Background())
			
			if err != nil {
				log.Printf("取消订单失败 [OrderID: %d]: %v", order.OrderID, err)
				continue
			}
			log.Printf("已取消订单 [OrderID: %d, Type: %s]", order.OrderID, order.Type)
		}
	}
	return nil
}

func (t *TraderCLI) checkAndSetStopLoss(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	entryPrice, _ := strconv.ParseFloat(position.EntryPrice, 64)
	if amt == 0 {
		return nil
	}

	// 获取当前订单
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 检查是否已有止损单
	hasValidStopLoss := false
	for _, order := range orders {
		if order.Type == futures.OrderTypeStopMarket {
			qty, _ := strconv.ParseFloat(order.OrigQuantity, 64)
			// 检查数量是否匹配
			if math.Abs(qty - math.Abs(amt)) <= 0.0001 {
				hasValidStopLoss = true
				break
			}
		}
	}

	// 如果没有有效的止损单，重新设置
	if !hasValidStopLoss {
		log.Printf("没有有效的止损单，重新设置止盈止损")
		if err := t.cancelAllTPSL(amt); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		// 等待两秒，确保订单已经被取消
		time.Sleep(2 * time.Second)
	}

	// 如果没有有效的止损单，创建一个
	if !hasValidStopLoss {
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
		_, err := t.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeStopMarket).
			StopPrice(fmt.Sprintf("%.2f", stopPrice)).
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Do(context.Background())
		
		if err != nil {
			return fmt.Errorf("创建止损单失败: %v", err)
		}
		log.Printf("已设置止损单，价格: %.2f", stopPrice)
	}

	return nil
}

func (t *TraderCLI) checkProtectiveStopProfit(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	
	// 确定仓位方向
	var direction string
	if amt > 0 {
		direction = "多"
	} else if amt < 0 {
		direction = "空"
	} else {
		direction = "无"
		// 没有持仓时，清除记录并撤销所有止盈止损单
		delete(t.maxProfit, position.Symbol)
		if err := t.cancelAllTPSL(0); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		log.Printf("没有持仓，已撤销所有止盈止损单")
		return nil
	}

	log.Printf("当前%s仓，数量: %.4f", direction, math.Abs(amt))

	entryPrice, _ := strconv.ParseFloat(position.EntryPrice, 64)
	unPnl, _ := strconv.ParseFloat(position.UnRealizedProfit, 64)

	// 获取当前订单
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 检查上次的仓位和入场价
	lastAmt := 0.0
	lastEntryPrice := 0.0
	if lastPos, ok := t.lastPosition["SOLUSDC"]; ok {
		lastAmt, _ = strconv.ParseFloat(lastPos.PositionAmt, 64)
		lastEntryPrice, _ = strconv.ParseFloat(lastPos.EntryPrice, 64)
	}

	// 如果仓位或入场价变化，取消所有订单
	if math.Abs(lastAmt-amt) > 0.0001 || math.Abs(lastEntryPrice-entryPrice) > 0.01 {
		log.Printf("仓位或入场价变化，准备重新设置订单")
		log.Printf("旧仓位: %.4f, 新仓位: %.4f", lastAmt, amt)
		log.Printf("旧入场价: %.2f, 新入场价: %.2f", lastEntryPrice, entryPrice)
		if err := t.cancelAllTPSL(amt); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		time.Sleep(1 * time.Second)
		// 重新获取订单
		orders, err = t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
		if err != nil {
			return fmt.Errorf("获取订单失败: %v", err)
		}
	}

	// 检查是否已有止损和止盈单
	hasValidStopLoss := false
	hasValidTakeProfit := false
	for _, order := range orders {
		qty, _ := strconv.ParseFloat(order.OrigQuantity, 64)
		if math.Abs(qty - math.Abs(amt)) <= 0.0001 {
			if order.Type == futures.OrderTypeStopMarket {
				hasValidStopLoss = true
				log.Printf("发现有效止损单: 数量=%.4f, 价格=%.2f", qty, order.StopPrice)
			} else if order.Type == futures.OrderTypeLimit {
				hasValidTakeProfit = true
				log.Printf("发现有效止盈单: 数量=%.4f, 价格=%.2f", qty, order.Price)
			}
		}
	}

	// 如果没有持仓，不需要设置止盈止损单
	if amt == 0 {
		if len(orders) > 0 {
			log.Printf("没有持仓，但发现%d个订单，准备清除", len(orders))
			if err := t.cancelAllTPSL(amt); err != nil {
				return fmt.Errorf("取消订单失败: %v", err)
			}
		}
		return nil
	}

	// 如果缺少任何一种订单，只设置缺少的订单
	if !hasValidStopLoss || !hasValidTakeProfit {
		if !hasValidStopLoss {
			log.Printf("缺少止损订单，准备设置")
		}
		if !hasValidTakeProfit {
			log.Printf("缺少止盈订单，准备设置")
		}

		// 设置止损单
		if !hasValidStopLoss {
			stopPrice := entryPrice
			side := futures.SideTypeSell
			positionSide := futures.PositionSideTypeLong
			if amt > 0 {
				// 多仓，止损价格在入场价下方100点
				stopPrice = entryPrice - 1.0
				side = futures.SideTypeSell
				positionSide = futures.PositionSideTypeLong
				log.Printf("设置多仓止损单，入场价: %.2f，止损价: %.2f", entryPrice, stopPrice)
			} else {
				// 空仓，止损价格在入场价上方100点
				stopPrice = entryPrice + 1.0
				side = futures.SideTypeBuy
				positionSide = futures.PositionSideTypeShort
				log.Printf("设置空仓止损单，入场价: %.2f，止损价: %.2f", entryPrice, stopPrice)
			}

			// 创建止损单
			stopOrder := t.client.NewCreateOrderService().
				Symbol("SOLUSDC").
				Side(side).
				PositionSide(positionSide).
				Type(futures.OrderTypeStopMarket).
				Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
				StopPrice(fmt.Sprintf("%.2f", stopPrice)).
				WorkingType("CONTRACT_PRICE")

			_, err = stopOrder.Do(context.Background())
			if err != nil {
				return fmt.Errorf("设置止损单失败: %v", err)
			}
			log.Printf("已设置止损单，价格: %.2f", stopPrice)
		}

		// 设置止盈单
		if !hasValidTakeProfit {
			var takeProfitPrice float64
			side := futures.SideTypeSell
			positionSide := futures.PositionSideTypeLong
			if amt > 0 {
				// 多仓，止盈价格在入场价上方100点
				takeProfitPrice = entryPrice + 2.0
				side = futures.SideTypeSell
				positionSide = futures.PositionSideTypeLong
				log.Printf("设置多仓止盈单，入场价: %.2f，止盈价: %.2f", entryPrice, takeProfitPrice)
			} else {
				// 空仓，止盈价格在入场价下方100点
				takeProfitPrice = entryPrice - 2.0
				side = futures.SideTypeBuy
				positionSide = futures.PositionSideTypeShort
				log.Printf("设置空仓止盈单，入场价: %.2f，止盈价: %.2f", entryPrice, takeProfitPrice)
			}

			// 创建止盈单
			profitOrder := t.client.NewCreateOrderService().
				Symbol("SOLUSDC").
				Side(side).
				PositionSide(positionSide).
				Type(futures.OrderTypeLimit).
				TimeInForce(futures.TimeInForceTypeGTC).
				Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
				Price(fmt.Sprintf("%.2f", takeProfitPrice)).
				WorkingType("CONTRACT_PRICE")

			_, err = profitOrder.Do(context.Background())
			if err != nil {
				return fmt.Errorf("设置止盈单失败: %v", err)
			}
			log.Printf("已设置止盈单，价格: %.2f", takeProfitPrice)
		}
	}

	// 更新最高盈利
	maxProfit := t.maxProfit[position.Symbol]
	if maxProfit == 0 || unPnl > maxProfit {
		t.maxProfit[position.Symbol] = unPnl
		maxProfit = unPnl
	}

	// 打印持仓信息
	positionType := "多"
	if amt < 0 {
		positionType = "空"
	}
	log.Printf("持仓信息 - 方向: %s, 数量: %.4f, 入场价: %.2f, 未实现盈亏: %.2f, 最高盈利: %.2f",
		positionType, math.Abs(amt), entryPrice, unPnl, maxProfit)

	// 如果曾经盈利超过200U，且当前回撤超过50%，执行市价平仓
	if maxProfit >= 200 && unPnl <= maxProfit*0.5 {
		side := futures.SideTypeSell
		positionSide := futures.PositionSideTypeLong
		if amt < 0 {
			side = futures.SideTypeBuy
			positionSide = futures.PositionSideTypeShort
		}

		// 市价平仓
		_, err := t.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeMarket).
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Do(context.Background())

		if err != nil {
			return fmt.Errorf("保护止盈平仓失败: %v", err)
		}

		log.Printf("触发保护止盈，最高盈利: %.2f，当前盈利: %.2f", maxProfit, unPnl)
		delete(t.maxProfit, position.Symbol)
	}

	return nil

}

func roundToTickSize(price float64, tickSize float64) float64 {
	return math.Round(price/tickSize) * tickSize
}

func (t *TraderCLI) run() error {
	log.Printf("交易系统启动...")

	for {
		// 检查缓存的持仓信息是否仍然有效（5秒内）
		var currentPosition *futures.PositionRisk
		if lastPos, ok := t.lastPosition["SOLUSDC"]; ok {
			if lastUpdate, ok := t.lastUpdate["SOLUSDC"]; ok {
				if time.Since(lastUpdate) < 5*time.Second {
					currentPosition = lastPos
				}
			}
		}

		// 如果缓存无效，获取新的持仓信息
		if currentPosition == nil {
			log.Printf("获取持仓信息...")
			positions, err := t.client.NewGetPositionRiskService().Do(context.Background())
			if err != nil {
				log.Printf("获取持仓信息失败: %v", err)
				time.Sleep(5 * time.Second)  // 失败后等待5秒
				continue
			}

			log.Printf("获取到 %d 个持仓信息", len(positions))

			// 查找SOLUSDC持仓
			log.Printf("开始查找SOLUSDC持仓信息...")
			// 打印所有非零持仓
			for _, p := range positions {
				amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
				if amt != 0 {
					log.Printf("发现持仓: Symbol=%s, PositionAmt=%s, EntryPrice=%s", p.Symbol, p.PositionAmt, p.EntryPrice)
					// 如果是SOLUSDC，直接使用这个持仓信息
					if p.Symbol == "SOLUSDC" {
						log.Printf("找到SOLUSDC有效持仓 - Symbol: %s, PositionAmt: %s, EntryPrice: %s, MarkPrice: %s, UnRealizedProfit: %s, LiquidationPrice: %s, Leverage: %s, MarginType: %s",
							p.Symbol, p.PositionAmt, p.EntryPrice, p.MarkPrice,
							p.UnRealizedProfit, p.LiquidationPrice, p.Leverage, p.MarginType)
						currentPosition = p
						// 更新缓存
						t.lastPosition["SOLUSDC"] = p
						t.lastUpdate["SOLUSDC"] = time.Now()
						break
					}
				}
			}

			// 如果没有找到持仓，创建一个空持仓
			if currentPosition == nil {
				currentPosition = &futures.PositionRisk{Symbol: "SOLUSDC", PositionAmt: "0"}
				t.lastPosition["SOLUSDC"] = currentPosition
				t.lastUpdate["SOLUSDC"] = time.Now()
			}
		}

		// 处理持仓信息
		amt, _ := strconv.ParseFloat(currentPosition.PositionAmt, 64)
		log.Printf("检查 SOLUSDC 持仓，数量: %.4f", amt)
		
		// 检查止盈止损
		if err := t.checkProtectiveStopProfit(currentPosition); err != nil {
			log.Printf("检查止盈止损失败: %v", err)
		}

		// 等待一秒
		time.Sleep(time.Second)
	}
}

func main() {
	// 从环境变量获取API密钥
	apiKey := os.Getenv("BINANCE_API_KEY")
	secretKey := os.Getenv("BINANCE_SECRET_KEY")

	if apiKey == "" || secretKey == "" {
		log.Fatal("请设置BINANCE_API_KEY和BINANCE_SECRET_KEY环境变量")
	}

	trader, err := NewTraderCLI(apiKey, secretKey)
	if err != nil {
		log.Fatalf("创建交易系统失败: %v", err)
	}

	if err := trader.run(); err != nil {
		log.Fatalf("交易系统运行失败: %v", err)
	}
}
