package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"hypefollow/pkg/binance"
	"hypefollow/pkg/config"
	"hypefollow/pkg/logger"
)

// Client handles Hyperliquid WebSocket
type Client struct {
	cfg             *config.Config
	binanceClient   *binance.Client
	ws              *websocket.Conn
	connMutex       sync.RWMutex
	writeMutex      sync.Mutex // Protect WebSocket writes
	orderMap        map[string]int64 // HL oid -> Binance orderId
	mapMutex        sync.RWMutex
	processingMap   map[string]bool  // HL oid -> is processing
	processingMutex sync.RWMutex
	reconnecting    bool
	reconnectMutex  sync.Mutex
	syncing         bool
	syncMutex       sync.Mutex
}

// NewClient creates a new Hyperliquid client
func NewClient(cfg *config.Config, binanceClient *binance.Client) *Client {
	return &Client{
		cfg:           cfg,
		binanceClient: binanceClient,
		orderMap:      make(map[string]int64),
		processingMap: make(map[string]bool),
	}
}

// Connect connects to Hyperliquid WebSocket
func (c *Client) Connect(ctx context.Context) error {
	wsURL := c.cfg.HyperliquidWSURL

	logger.Info("Connecting to Hyperliquid", "url", wsURL)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	ws, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}

	c.connMutex.Lock()
	oldWS := c.ws
	c.ws = ws
	c.connMutex.Unlock()

	// Close old connection if exists
	if oldWS != nil {
		oldWS.Close()
	}

	logger.Info("✅ WebSocket connected")

	// Subscribe to order updates
	if err := c.subscribe(); err != nil {
		c.connMutex.Lock()
		c.ws = nil
		c.connMutex.Unlock()
		ws.Close()
		return err
	}

	// Initial sync
	go c.syncOpenOrders()

	// Read messages
	go c.readMessages(ctx)

	return nil
}

// StartChecksumValidation starts the periodic checksum validation (call once from main)
func (c *Client) StartChecksumValidation(ctx context.Context) {
	c.startChecksumValidation(ctx)
}

func (c *Client) subscribe() error {
	msg := map[string]interface{}{
		"method": "subscribe",
		"subscription": map[string]string{
			"type": "orderUpdates",
			"user": c.cfg.HyperliquidUser,
		},
	}

	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	if err := c.ws.WriteJSON(msg); err != nil {
		return fmt.Errorf("subscribe failed: %w", err)
	}

	logger.Info("Subscribed to order updates", "user", c.cfg.HyperliquidUser)
	return nil
}

func (c *Client) readMessages(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send ping
			c.writeMutex.Lock()
			c.connMutex.RLock()
			ws := c.ws
			c.connMutex.RUnlock()

			if ws != nil {
				ws.WriteJSON(map[string]string{"method": "ping"})
			}
			c.writeMutex.Unlock()
		default:
			c.connMutex.RLock()
			ws := c.ws
			c.connMutex.RUnlock()

			if ws == nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			_, message, err := ws.ReadMessage()
			if err != nil {
				logger.Error("WebSocket read error", err)
				c.reconnect(ctx)
				return
			}

			c.handleMessage(message)
		}
	}
}

func (c *Client) handleMessage(data []byte) {
	var msg struct {
		Channel string          `json:"channel"`
		Data    json.RawMessage `json:"data"`
	}
	
	if err := json.Unmarshal(data, &msg); err != nil {
		logger.Error("Failed to parse message", err)
		return
	}
	
	if msg.Channel != "orderUpdates" {
		return
	}
	
	var orders []HLOrder
	if err := json.Unmarshal(msg.Data, &orders); err != nil {
		logger.Error("Failed to parse orders", err)
		return
	}
	
	for _, order := range orders {
		if order.Coin != "BTC" {
			continue
		}
		
		logger.Info("Order update",
			"oid", order.OID,
			"status", order.Status,
			"side", order.Side,
			"size", order.Size,
			"price", order.LimitPx,
		)
		
		switch order.Status {
		case "open", "triggered":
			c.handleNewOrUpdate(order)
		case "canceled":
			c.handleCancel(order)
		case "filled":
			c.handleFill(order)
		}
	}
}

func (c *Client) handleNewOrUpdate(order HLOrder) {
	oidStr := strconv.FormatInt(order.OID, 10)

	// Try to acquire processing lock for this order
	c.processingMutex.Lock()
	if c.processingMap[oidStr] {
		c.processingMutex.Unlock()
		logger.Info("Order already being processed, skipping", "oid", order.OID)
		return
	}
	c.processingMap[oidStr] = true
	c.processingMutex.Unlock()

	// Ensure we release the processing lock when done
	defer func() {
		c.processingMutex.Lock()
		delete(c.processingMap, oidStr)
		c.processingMutex.Unlock()
	}()

	// Calculate follower quantity
	qty := order.Size * c.cfg.FollowRatio
	if qty < c.cfg.MinOrderSize {
		logger.Warn("Quantity below minimum", "qty", qty, "min", c.cfg.MinOrderSize)
		return
	}

	// Round to 3 decimals
	qty = float64(int(qty*1000)) / 1000

	symbol := binance.GetBinanceSymbol(order.Coin)
	binanceSide := "BUY"
	if order.Side == "A" {
		binanceSide = "SELL"
	}

	// Check if already mapped
	c.mapMutex.RLock()
	existingID, exists := c.orderMap[oidStr]
	c.mapMutex.RUnlock()

	if exists {
		// Cancel existing and recreate
		logger.Info("Updating existing order", "hl_oid", order.OID, "binance_id", existingID)
		c.binanceClient.CancelOrder(symbol, existingID)
	}

	result, err := c.binanceClient.CreateLimitOrder(symbol, binanceSide, order.LimitPx, qty)
	if err != nil {
		logger.Error("Failed to create order", err, "oid", order.OID)
		return
	}

	// Save mapping
	if result != nil {
		if orderID, ok := result["orderId"].(float64); ok {
			c.mapMutex.Lock()
			c.orderMap[oidStr] = int64(orderID)
			c.mapMutex.Unlock()

			logger.Info("✅ Order created",
				"hl_oid", order.OID,
				"binance_id", int64(orderID),
				"qty", qty,
				"price", order.LimitPx,
			)
		}
	}
}

func (c *Client) handleCancel(order HLOrder) {
	oidStr := strconv.FormatInt(order.OID, 10)

	// Try to acquire processing lock for this order
	c.processingMutex.Lock()
	if c.processingMap[oidStr] {
		c.processingMutex.Unlock()
		logger.Info("Order being processed, queuing cancel", "oid", order.OID)
		// Wait a bit and retry
		go func() {
			time.Sleep(100 * time.Millisecond)
			c.handleCancel(order)
		}()
		return
	}
	c.processingMap[oidStr] = true
	c.processingMutex.Unlock()

	// Ensure we release the processing lock when done
	defer func() {
		c.processingMutex.Lock()
		delete(c.processingMap, oidStr)
		c.processingMutex.Unlock()
	}()

	c.mapMutex.RLock()
	binanceID, exists := c.orderMap[oidStr]
	c.mapMutex.RUnlock()

	if !exists {
		logger.Warn("Cancel: order not mapped", "oid", order.OID)
		return
	}

	symbol := binance.GetBinanceSymbol(order.Coin)
	if err := c.binanceClient.CancelOrder(symbol, binanceID); err != nil {
		logger.Error("Failed to cancel order", err, "oid", order.OID)
		return
	}

	c.mapMutex.Lock()
	delete(c.orderMap, oidStr)
	c.mapMutex.Unlock()

	logger.Info("✅ Order cancelled", "hl_oid", order.OID, "binance_id", binanceID)
}

func (c *Client) handleFill(order HLOrder) {
	oidStr := strconv.FormatInt(order.OID, 10)
	logger.Info("Order filled", "oid", order.OID)

	// Cleanup mapping after delay
	go func() {
		time.Sleep(5 * time.Second)
		// Check if currently processing
		c.processingMutex.RLock()
		isProcessing := c.processingMap[oidStr]
		c.processingMutex.RUnlock()

		if isProcessing {
			// Wait for processing to complete
			time.Sleep(100 * time.Millisecond)
		}

		c.mapMutex.Lock()
		delete(c.orderMap, oidStr)
		c.mapMutex.Unlock()
	}()
}

func (c *Client) syncOpenOrders() {
	c.syncMutex.Lock()
	if c.syncing {
		c.syncMutex.Unlock()
		logger.Info("Sync already in progress, skipping")
		return
	}
	c.syncing = true
	c.syncMutex.Unlock()

	defer func() {
		c.syncMutex.Lock()
		c.syncing = false
		c.syncMutex.Unlock()
	}()

	logger.Info("Starting bidirectional order sync...")

	// Fetch HL orders
	hlOrders, err := c.fetchHLOrders()
	if err != nil {
		logger.Error("Failed to fetch HL orders", err)
		return
	}

	// Filter BTC orders and build map
	hlOrderMap := make(map[int64]HLOrder)
	for _, order := range hlOrders {
		if order.Coin == "BTC" {
			hlOrderMap[order.OID] = order
		}
	}

	// Fetch Binance open orders
	binanceOrders, err := c.binanceClient.GetOpenOrders("BTCUSDT")
	if err != nil {
		logger.Error("Failed to fetch Binance orders", err)
		return
	}

	logger.Info("Orders to sync",
		"hl_btc_count", len(hlOrderMap),
		"binance_count", len(binanceOrders),
	)

	// Track Binance orders that should be cancelled (zombie orders)
	var zombieOrderIDs []int64
	activeBinanceIDs := make(map[int64]bool)

	for _, bOrder := range binanceOrders {
		orderType, _ := bOrder["type"].(string)
		if orderType != "LIMIT" {
			continue
		}

		orderID, ok := bOrder["orderId"].(float64)
		if !ok {
			continue
		}

		binanceID := int64(orderID)
		activeBinanceIDs[binanceID] = true

		// Check if this Binance order maps to any HL order
		found := false
		c.mapMutex.RLock()
		for hlOID, mappedBinanceID := range c.orderMap {
			if mappedBinanceID == binanceID {
				// Check if HL order still exists
				hlOIDInt, _ := strconv.ParseInt(hlOID, 10, 64)
				if _, exists := hlOrderMap[hlOIDInt]; exists {
					found = true
				}
				break
			}
		}
		c.mapMutex.RUnlock()

		if !found {
			// This is a zombie order (Binance has it but HL doesn't)
			zombieOrderIDs = append(zombieOrderIDs, binanceID)
		}
	}

	// Cancel zombie orders
	cancelled := 0
	for _, orderID := range zombieOrderIDs {
		logger.Info("Cancelling zombie order", "orderId", orderID)
		if err := c.binanceClient.CancelOrder("BTCUSDT", orderID); err != nil {
			logger.Error("Failed to cancel zombie order", err, "orderId", orderID)
		} else {
			cancelled++
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Sync HL orders to Binance
	created := 0
	for _, hlOrder := range hlOrderMap {
		// Check if already mapped to an active Binance order
		oidStr := strconv.FormatInt(hlOrder.OID, 10)
		c.mapMutex.RLock()
		existingBinanceID, exists := c.orderMap[oidStr]
		c.mapMutex.RUnlock()

		if exists && activeBinanceIDs[existingBinanceID] {
			// Order already synced and active, skip
			continue
		}

		// Need to create this order
		logger.Info("Syncing order",
			"oid", hlOrder.OID,
			"side", hlOrder.Side,
			"size", hlOrder.Size,
			"price", hlOrder.LimitPx,
		)

		c.handleNewOrUpdate(hlOrder)
		created++
		time.Sleep(100 * time.Millisecond)
	}

	logger.Info("Order sync complete",
		"zombies_found", len(zombieOrderIDs),
		"zombies_cancelled", cancelled,
		"hl_orders_created", created,
	)
}

func (c *Client) reconnect(ctx context.Context) {
	c.reconnectMutex.Lock()
	if c.reconnecting {
		c.reconnectMutex.Unlock()
		logger.Info("Reconnect already in progress, skipping")
		return
	}
	c.reconnecting = true
	c.reconnectMutex.Unlock()

	defer func() {
		c.reconnectMutex.Lock()
		c.reconnecting = false
		c.reconnectMutex.Unlock()
	}()

	logger.Info("Reconnecting...")

	// Close old connection
	c.connMutex.Lock()
	if c.ws != nil {
		c.ws.Close()
		c.ws = nil
	}
	c.connMutex.Unlock()

	// Exponential backoff
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if err := c.Connect(ctx); err != nil {
				logger.Error("Reconnect failed", err, "backoff", backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			logger.Info("✅ Reconnected successfully")
			return
		}
	}
}

// Close closes the connection
func (c *Client) Close() {
	c.connMutex.Lock()
	if c.ws != nil {
		c.ws.Close()
	}
	c.connMutex.Unlock()
}

// HLOrder represents a Hyperliquid order
type HLOrder struct {
	Coin    string  `json:"coin"`
	Side    string  `json:"side"`
	LimitPx float64 `json:"limitPx,string"`
	Size    float64 `json:"sz,string"`
	OID     int64   `json:"oid"`
	Status  string  `json:"status"`
}

// startChecksumValidation runs periodic checksum validation every 5 minutes (internal)
func (c *Client) startChecksumValidation(ctx context.Context) {
	// Wait 30 seconds before first check to allow initial sync to complete
	time.Sleep(30 * time.Second)

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run first check
	c.validateChecksum()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.validateChecksum()
		}
	}
}

// validateChecksum compares HL and Binance order counts and triggers sync if needed
func (c *Client) validateChecksum() {
	// Skip if sync is already in progress
	c.syncMutex.Lock()
	if c.syncing {
		c.syncMutex.Unlock()
		logger.Info("Checksum: sync in progress, skipping validation")
		return
	}
	c.syncMutex.Unlock()

	// Get HL orders
	hlOrders, err := c.fetchHLOrders()
	if err != nil {
		logger.Error("Checksum: failed to fetch HL orders", err)
		return
	}

	// Count BTC orders
	hlCount := 0
	for _, order := range hlOrders {
		if order.Coin == "BTC" {
			hlCount++
		}
	}

	// Get Binance open orders
	binanceOrders, err := c.binanceClient.GetOpenOrders("BTCUSDT")
	if err != nil {
		logger.Error("Checksum: failed to fetch Binance orders", err)
		return
	}

	// Count LIMIT orders
	binanceCount := 0
	for _, order := range binanceOrders {
		if orderType, _ := order["type"].(string); orderType == "LIMIT" {
			binanceCount++
		}
	}

	// Compare counts
	if hlCount != binanceCount {
		logger.Warn("Order count mismatch detected, triggering sync",
			"hl_count", hlCount,
			"binance_count", binanceCount,
		)
		c.syncOpenOrders()
	} else {
		logger.Info("Order count validated",
			"count", hlCount,
		)
	}
}

// fetchHLOrders fetches open orders from Hyperliquid
func (c *Client) fetchHLOrders() ([]HLOrder, error) {
	url := "https://api.hyperliquid.xyz/info"
	payload := map[string]interface{}{
		"type": "openOrders",
		"user": c.cfg.HyperliquidUser,
	}

	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var orders []HLOrder
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, err
	}

	return orders, nil
}
