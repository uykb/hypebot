package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

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

	// Create Hyperliquid WebSocket client
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
