package trading

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// Trader 交易器
type Trader struct {
	client     *futures.Client
	maxProfit  map[string]float64
	positions  map[string]float64
	lastPosition map[string]*futures.PositionRisk
	lastUpdate   map[string]time.Time
}

// NewTrader 创建新的交易器
func NewTrader(client *futures.Client) *Trader {
	return &Trader{
		client:     client,
		maxProfit:  make(map[string]float64),
		positions:  make(map[string]float64),
		lastPosition: make(map[string]*futures.PositionRisk),
		lastUpdate:   make(map[string]time.Time),
	}
}

// PlaceOrder 下单
func (t *Trader) PlaceOrder(symbol string, side futures.SideType, orderType futures.OrderType, quantity float64, price float64) error {
	orderService := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		Type(orderType).
		Quantity(fmt.Sprintf("%.4f", quantity))

	if orderType == futures.OrderTypeLimit {
		orderService.TimeInForce(futures.TimeInForceTypeGTC).
			Price(fmt.Sprintf("%.2f", price))
	}

	_, err := orderService.Do(context.Background())
	return err
}

// SetStopLoss 设置止损
func (t *Trader) SetStopLoss(symbol string, position *futures.PositionRisk, stopPrice float64) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		return nil
	}

	side := futures.SideTypeSell
	positionSide := futures.PositionSideTypeLong
	if amt < 0 {
		side = futures.SideTypeBuy
		positionSide = futures.PositionSideTypeShort
	}

	_, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(positionSide).
		Type(futures.OrderTypeStopMarket).
		Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
		StopPrice(fmt.Sprintf("%.2f", stopPrice)).
		Do(context.Background())

	return err
}

// SetTakeProfit 设置止盈
func (t *Trader) SetTakeProfit(symbol string, position *futures.PositionRisk, price float64) error {
	amt, _ := strconv.ParseFloat(position.PositionAmt, 64)
	if amt == 0 {
		return nil
	}

	side := futures.SideTypeSell
	positionSide := futures.PositionSideTypeLong
	if amt < 0 {
		side = futures.SideTypeBuy
		positionSide = futures.PositionSideTypeShort
	}

	_, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(positionSide).
		Type(futures.OrderTypeLimit).
		TimeInForce(futures.TimeInForceTypeGTC).
		Quantity(fmt.Sprintf("%.4f", math.Abs(amt))).
		Price(fmt.Sprintf("%.2f", price)).
		Do(context.Background())

	return err
}

// CancelAllOrders 取消所有订单
func (t *Trader) CancelAllOrders(symbol string) error {
	_, err := t.client.NewCancelAllOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())
	return err
}

// GetPosition 获取持仓
func (t *Trader) GetPosition(symbol string) (*futures.PositionRisk, error) {
	// 检查缓存的持仓信息是否仍然有效（5秒内）
	if lastPos, ok := t.lastPosition[symbol]; ok {
		if lastUpdate, ok := t.lastUpdate[symbol]; ok {
			if time.Since(lastUpdate) < 5*time.Second {
				return lastPos, nil
			}
		}
	}

	positions, err := t.client.NewGetPositionRiskService().Do(context.Background())
	if err != nil {
		return nil, err
	}

	for _, p := range positions {
		if p.Symbol == symbol {
			// 更新缓存
			t.lastPosition[symbol] = p
			t.lastUpdate[symbol] = time.Now()
			return p, nil
		}
	}

	// 如果没有持仓，也更新缓存
	t.lastPosition[symbol] = &futures.PositionRisk{Symbol: symbol, PositionAmt: "0"}
	t.lastUpdate[symbol] = time.Now()

	return nil, fmt.Errorf("position not found")
}
