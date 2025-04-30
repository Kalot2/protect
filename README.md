# Binance Demo

这是一个使用go-binance SDK的示例项目，展示了以下功能：
1. 获取实时价格
2. 获取K线数据
3. 使用WebSocket监听价格变化

## 前置要求

1. 安装Go (版本 >= 1.8)
2. 币安API密钥（在main.go中替换apiKey和secretKey）

## 安装

```bash
# 安装依赖
go mod tidy
```

## 运行

```bash
go run main.go
```

## 功能说明

- 程序启动后会先获取BTC/USDT的实时价格
- 然后获取最近10根1小时K线数据
- 最后会启动WebSocket连接，实时监听价格变化
- 按Ctrl+C可以优雅退出程序
