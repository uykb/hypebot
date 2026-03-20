package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hypefollow/pkg/binance"
	"hypefollow/pkg/config"
	"hypefollow/pkg/hyperliquid"
	"hypefollow/pkg/logger"
)

func main() {
	logger.Info("Starting HypeFollow (Go MVP - Limit Orders Only)...")

	cfg := config.Load()
	
	logger.Info("Config loaded",
		"hyperliquid_user", cfg.HyperliquidUser,
		"follow_ratio", cfg.FollowRatio,
		"binance_testnet", cfg.BinanceUseTestnet,
	)

	// Create Binance client
	binanceClient, err := binance.NewClient(cfg)
	if err != nil {
		logger.Fatal("Failed to create Binance client", err)
	}

	// Verify API permissions
	if err := binanceClient.VerifyAPI(); err != nil {
		logger.Fatal("Binance API verification failed", err)
	}

	logger.Info("✅ Binance API verified")

	// Step 1: Cleanup existing BTCUSDT limit orders on Binance
	logger.Info("Cleaning up existing BTCUSDT orders on Binance...")
	if err := cleanupBinanceOrders(binanceClient); err != nil {
		logger.Error("Failed to cleanup existing orders", err)
		// Continue anyway, non-fatal
	}

	// Step 2: Create Hyperliquid WebSocket client
	hlClient := hyperliquid.NewClient(cfg, binanceClient)

	// Connect and sync
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := hlClient.Connect(ctx); err != nil {
			logger.Error("Hyperliquid connection error", err)
		}
	}()

	// Wait for interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	
	logger.Info("HypeFollow is running. Press Ctrl+C to stop.")
	<-sigChan
	
	logger.Info("Shutting down...")
	cancel()
	hlClient.Close()
}

// cleanupBinanceOrders cancels all existing open limit orders for BTCUSDT
func cleanupBinanceOrders(client *binance.Client) error {
	symbol := "BTCUSDT"

	// Get all open orders
	orders, err := client.GetOpenOrders(symbol)
	if err != nil {
		return fmt.Errorf("failed to get open orders: %w", err)
	}

	if len(orders) == 0 {
		return nil
	}

	logger.Info("Cleaning up existing orders", "count", len(orders))

	// Cancel each order
	cancelled := 0
	for _, order := range orders {
		orderID, ok := order["orderId"].(float64)
		if !ok {
			logger.Warn("Invalid order ID format", "order", order)
			continue
		}

		orderType, _ := order["type"].(string)
		if orderType != "LIMIT" {
			logger.Info("Skipping non-limit order", "orderId", int64(orderID), "type", orderType)
			continue
		}

		if err := client.CancelOrder(symbol, int64(orderID)); err != nil {
			logger.Error("Failed to cancel order", err, "orderId", int64(orderID))
			continue
		}

		cancelled++

		// Rate limit to avoid hitting API limits
		time.Sleep(50 * time.Millisecond)
	}

	logger.Info("Cleanup complete", "cancelled", cancelled, "total", len(orders))
	return nil
}
