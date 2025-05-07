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
	positions  map[string]float64  // 记录上一次的仓位大小
}

func NewTraderCLI(apiKey, secretKey string) (*TraderCLI, error) {
	client := binance.NewFuturesClient(apiKey, secretKey)
	
	return &TraderCLI{
		client:     client,
		maxProfit:  make(map[string]float64),
		positions:  make(map[string]float64),
	}, nil
}

// 取消所有止盈止损订单
func (t *TraderCLI) cancelAllTPSL() error {
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	for _, order := range orders {
		// 只取消止盈(LIMIT)和止损(STOP_MARKET)订单
		if order.Type == futures.OrderTypeLimit || order.Type == futures.OrderTypeStopMarket {
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
	if amt == 0 {
		return nil
	}

	// 获取当前订单
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 计算当前止损订单的总数量
	var totalStopLossQty float64
	for _, order := range orders {
		if order.Type == futures.OrderTypeStopMarket {
			qty, _ := strconv.ParseFloat(order.OrigQuantity, 64)
			totalStopLossQty += qty
		}
	}

	// 如果止损订单总数量不等于仓位数量，重新设置
	if math.Abs(totalStopLossQty - math.Abs(amt)) > 0.0001 {
		log.Printf("止损订单数量不匹配 [订单: %.4f, 仓位: %.4f]，重新设置止盈止损", totalStopLossQty, math.Abs(amt))
		if err := t.cancelAllTPSL(); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		t.positions["SOLUSDC"] = amt
		// 等待一秒，确保订单已经被取消
		time.Sleep(time.Second)
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

	// 如果已经有止损单且数量正确，不需要重新设置
	if hasStopLoss {
		return nil
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

func (t *TraderCLI) checkAndSetTakeProfit(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		return nil
	}

	// 获取当前订单
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 计算当前止盈订单的总数量
	var totalTakeProfitQty float64
	for _, order := range orders {
		if order.Type == futures.OrderTypeLimit {
			qty, _ := strconv.ParseFloat(order.OrigQuantity, 64)
			totalTakeProfitQty += qty
		}
	}

	// 如果止盈订单总数量不等于仓位数量，重新设置
	if math.Abs(totalTakeProfitQty - math.Abs(amt)) > 0.0001 {
		log.Printf("止盈订单数量不匹配 [订单: %.4f, 仓位: %.4f]，重新设置止盈止损", totalTakeProfitQty, math.Abs(amt))
		if err := t.cancelAllTPSL(); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		// 等待一秒，确保订单已经被取消
		time.Sleep(time.Second)
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

		// 将价格四舍五入到0.01
		price = roundToTickSize(price, 0.01)

		// 创建限价止盈单
		_, err := t.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeLimit).
			TimeInForce(futures.TimeInForceTypeGTC).
			Price(fmt.Sprintf("%.2f", price)).
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Do(context.Background())
		
		if err != nil {
			return fmt.Errorf("创建止盈单失败: %v", err)
		}
		log.Printf("已设置止盈单，价格: %.2f", price)
	}

	return nil
}

func (t *TraderCLI) checkProtectiveStopProfit(position *futures.PositionRisk) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		delete(t.maxProfit, position.Symbol)  // 清除记录
		return nil
	}

	unPnl, _ := strconv.ParseFloat(position.UnRealizedProfit, 64)
	
	// 更新最高盈利
	if _, exists := t.maxProfit[position.Symbol]; !exists {
		t.maxProfit[position.Symbol] = unPnl
	} else if unPnl > t.maxProfit[position.Symbol] {
		t.maxProfit[position.Symbol] = unPnl
	}

	maxProfit := t.maxProfit[position.Symbol]
	
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

func (t *TraderCLI) run() error {
	log.Println("交易系统启动...")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		positions, err := t.client.NewGetPositionRiskService().Do(context.Background())
		if err != nil {
			log.Printf("获取持仓信息失败: %v", err)
			continue
		}

		for _, p := range positions {
			if p.Symbol == "SOLUSDC" {
				amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
				if amt != 0 {
					entryPrice, _ := strconv.ParseFloat(p.EntryPrice, 64)
					unPnl, _ := strconv.ParseFloat(p.UnRealizedProfit, 64)
					
					direction := "多"
					if amt < 0 {
						direction = "空"
					}

					log.Printf("持仓信息 - 方向: %s, 数量: %.4f, 入场价: %.2f, 未实现盈亏: %.2f, 最高盈利: %.2f",
						direction, math.Abs(amt), entryPrice, unPnl, t.maxProfit[p.Symbol])

					// 检查保护止盈
					if err := t.checkProtectiveStopProfit(p); err != nil {
						log.Printf("检查保护止盈失败: %v", err)
					}

					// 检查并设置止盈
					if err := t.checkAndSetTakeProfit(p); err != nil {
						log.Printf("设置止盈失败: %v", err)
					}

					// 检查并设置止损
					if err := t.checkAndSetStopLoss(p); err != nil {
						log.Printf("设置止损失败: %v", err)
					}
				}
			}
		}
	}

	return nil
}

func roundToTickSize(price float64, tickSize float64) float64 {
	return math.Round(price/tickSize) * tickSize
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
