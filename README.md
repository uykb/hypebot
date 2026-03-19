# HypeFollow Go MVP

Simple BTC limit order sync from Hyperliquid to Binance Futures.

## Environment Variables

```bash
# Required
BINANCE_API_KEY=your_binance_api_key
BINANCE_API_SECRET=your_binance_api_secret

# Optional
HYPERLIQUID_USER=0xdae4df7207feb3b350e4284c8efe5f7dac37f637
FOLLOW_RATIO=0.1                    # Follow 10% of HL order size
MIN_ORDER_SIZE=0.002                # Minimum order size
BINANCE_TESTNET=false               # Use testnet
LOG_LEVEL=info                      # Log level
```

## Build

```bash
cd /root/HypeFollow-go
docker build -t hypefollow-go .
```

## Run

```bash
docker run -e BINANCE_API_KEY=xxx -e BINANCE_API_SECRET=xxx hypefollow-go
```

## Features

- WebSocket connection to Hyperliquid for real-time order updates
- Initial sync of all open orders on startup
- Create/update/cancel limit orders on Binance
- Only BTC/USDT trading pair
- Fixed ratio following (e.g., 10% of HL order size)

## Architecture

```
Hyperliquid WebSocket → Order Parser → Binance API
     ↑
Initial Sync (REST API)
```
