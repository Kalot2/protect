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
		// 只取消止盈止损单
		if (order.Type == futures.OrderTypeLimit && order.ReduceOnly) || order.Type == futures.OrderTypeStopMarket {
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
		if err := t.cancelAllTPSL(); err != nil {
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
	if amt == 0 {
		// 没有持仓时，清除记录并撤销所有止盈止损单
		delete(t.maxProfit, position.Symbol)
		if err := t.cancelAllTPSL(); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		log.Printf("没有持仓，已撤销所有止盈止损单")
		return nil
	}

	entryPrice, _ := strconv.ParseFloat(position.EntryPrice, 64)
	unPnl, _ := strconv.ParseFloat(position.UnRealizedProfit, 64)

	// 获取当前订单
	orders, err := t.client.NewListOpenOrdersService().Symbol("SOLUSDC").Do(context.Background())
	if err != nil {
		return fmt.Errorf("获取订单失败: %v", err)
	}

	// 检查是否已有止损和止盈单
	hasValidStopLoss := false
	hasValidTakeProfit := false
	for _, order := range orders {
		qty, _ := strconv.ParseFloat(order.OrigQuantity, 64)
		if math.Abs(qty - math.Abs(amt)) <= 0.0001 {
			if order.Type == futures.OrderTypeStopMarket {
				hasValidStopLoss = true
			} else if order.Type == futures.OrderTypeLimit {
				hasValidTakeProfit = true
			}
		}
	}

	// 如果缺少任何一种订单，重新设置全部订单
	if !hasValidStopLoss || !hasValidTakeProfit {
		log.Printf("缺少止盈止损订单，重新设置")
		if err := t.cancelAllTPSL(); err != nil {
			return fmt.Errorf("取消订单失败: %v", err)
		}
		// 等待两秒，确保订单已经被取消
		time.Sleep(2 * time.Second)

		// 设置止损单
		stopPrice := entryPrice
		side := futures.SideTypeSell
		positionSide := futures.PositionSideTypeLong
		if amt > 0 {
			// 多仓，止损价格在入场价下方100点
			stopPrice = entryPrice - 1.0
			side = futures.SideTypeSell
			positionSide = futures.PositionSideTypeLong
		} else {
			// 空仓，止损价格在入场价上方100点
			stopPrice = entryPrice + 1.0
			side = futures.SideTypeBuy
			positionSide = futures.PositionSideTypeShort
		}

		// 创建止损单
		_, err = t.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeStopMarket).
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			StopPrice(fmt.Sprintf("%.2f", stopPrice)).
			Do(context.Background())
		if err != nil {
			return fmt.Errorf("设置止损单失败: %v", err)
		}
		log.Printf("已设置止损单，价格: %.2f", stopPrice)

		// 设置止盈单
		side = futures.SideTypeSell
		positionSide = futures.PositionSideTypeLong
		var price float64
		if amt > 0 {
			// 多仓，止盈价格在入场价上方200点
			price = entryPrice + 2.0
			side = futures.SideTypeSell
			positionSide = futures.PositionSideTypeLong
		} else {
			// 空仓，止盈价格在入场价下方200点
			price = entryPrice - 2.0
			side = futures.SideTypeBuy
			positionSide = futures.PositionSideTypeShort
		}

		// 创建止盈单
		_, err = t.client.NewCreateOrderService().
			Symbol("SOLUSDC").
			Side(side).
			PositionSide(positionSide).
			Type(futures.OrderTypeLimit).
			TimeInForce(futures.TimeInForceTypeGTC).
			Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
			Price(fmt.Sprintf("%.2f", price)).
			Do(context.Background())
		if err != nil {
			return fmt.Errorf("设置止盈单失败: %v", err)
		}
		log.Printf("已设置止盈单，价格: %.2f", price)
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
		log.Printf("获取持仓信息...")
		// 获取持仓信息
		positions, err := t.client.NewGetPositionRiskService().Do(context.Background())
		if err != nil {
			log.Printf("获取持仓信息失败: %v", err)
			time.Sleep(5 * time.Second)  // 失败后等待5秒
			continue
		}

		log.Printf("获取到 %d 个持仓信息", len(positions))

		// 检查每个持仓
		hasSOLPosition := false
		for _, p := range positions {
			if p.Symbol == "SOLUSDC" {
				hasSOLPosition = true
				amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
				log.Printf("检查 SOLUSDC 持仓，数量: %.4f", amt)
				
				// 检查止盈止损
				if err := t.checkProtectiveStopProfit(p); err != nil {
					log.Printf("检查止盈止损失败: %v", err)
				}
			}
		}

		if !hasSOLPosition {
			log.Printf("未找到 SOLUSDC 持仓信息")
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
