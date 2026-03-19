package config

import (
	"os"
	"strconv"
)

// Config holds all configuration
type Config struct {
	HyperliquidUser    string
	HyperliquidWSURL   string
	
	BinanceAPIKey      string
	BinanceAPISecret   string
	BinanceUseTestnet  bool
	
	FollowRatio        float64
	MinOrderSize       float64
	
	LogLevel           string
}

// Load loads configuration from environment
func Load() *Config {
	return &Config{
		HyperliquidUser:   getEnv("HYPERLIQUID_USER", "0xdae4df7207feb3b350e4284c8efe5f7dac37f637"),
		HyperliquidWSURL:  getEnv("HYPERLIQUID_WS_URL", "wss://api.hyperliquid.xyz/ws"),
		
		BinanceAPIKey:     getEnv("BINANCE_API_KEY", ""),
		BinanceAPISecret:  getEnv("BINANCE_API_SECRET", ""),
		BinanceUseTestnet: getEnvBool("BINANCE_TESTNET", false),
		
		FollowRatio:       getEnvFloat("FOLLOW_RATIO", 0.1),
		MinOrderSize:      getEnvFloat("MIN_ORDER_SIZE", 0.002),
		
		LogLevel:          getEnv("LOG_LEVEL", "info"),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val == "true" || val == "1"
}

func getEnvFloat(key string, defaultVal float64) float64 {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultVal
	}
	return f
}
