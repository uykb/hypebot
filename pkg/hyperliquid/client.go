package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	cfg           *config.Config
	binanceClient *binance.Client
	ws            *websocket.Conn
	connMutex     sync.RWMutex
	orderMap      map[string]int64 // HL oid -> Binance orderId
	mapMutex      sync.RWMutex
}

// NewClient creates a new Hyperliquid client
func NewClient(cfg *config.Config, binanceClient *binance.Client) *Client {
	return &Client{
		cfg:           cfg,
		binanceClient: binanceClient,
		orderMap:      make(map[string]int64),
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
	c.ws = ws
	c.connMutex.Unlock()
	
	logger.Info("✅ WebSocket connected")
	
	// Subscribe to order updates
	if err := c.subscribe(); err != nil {
		return err
	}
	
	// Initial sync
	go c.syncOpenOrders()
	
	// Read messages
	go c.readMessages(ctx)
	
	return nil
}

func (c *Client) subscribe() error {
	msg := map[string]interface{}{
		"method": "subscribe",
		"subscription": map[string]string{
			"type": "orderUpdates",
			"user": c.cfg.HyperliquidUser,
		},
	}
	
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
			c.connMutex.RLock()
			ws := c.ws
			c.connMutex.RUnlock()
			
			if ws != nil {
				ws.WriteJSON(map[string]string{"method": "ping"})
			}
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
				c.reconnect()
				continue
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
	existingID, exists := c.orderMap[strconv.FormatInt(order.OID, 10)]
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
			c.orderMap[strconv.FormatInt(order.OID, 10)] = int64(orderID)
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
	c.mapMutex.RLock()
	binanceID, exists := c.orderMap[strconv.FormatInt(order.OID, 10)]
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
	delete(c.orderMap, strconv.FormatInt(order.OID, 10))
	c.mapMutex.Unlock()
	
	logger.Info("✅ Order cancelled", "hl_oid", order.OID, "binance_id", binanceID)
}

func (c *Client) handleFill(order HLOrder) {
	logger.Info("Order filled", "oid", order.OID)
	
	// Cleanup mapping after delay
	go func() {
		time.Sleep(5 * time.Second)
		c.mapMutex.Lock()
		delete(c.orderMap, strconv.FormatInt(order.OID, 10))
		c.mapMutex.Unlock()
	}()
}

func (c *Client) syncOpenOrders() {
	logger.Info("Syncing open orders from Hyperliquid...")
	
	url := "https://api.hyperliquid.xyz/info"
	payload := map[string]interface{}{
		"type": "openOrders",
		"user": c.cfg.HyperliquidUser,
	}
	
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error("Failed to fetch open orders", err)
		return
	}
	defer resp.Body.Close()
	
	var orders []HLOrder
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		logger.Error("Failed to decode orders", err)
		return
	}
	
	logger.Info("Found open orders", "count", len(orders))
	
	for _, order := range orders {
		if order.Coin != "BTC" {
			continue
		}
		
		logger.Info("Syncing order",
			"oid", order.OID,
			"side", order.Side,
			"size", order.Size,
			"price", order.LimitPx,
		)
		
		c.handleNewOrUpdate(order)
	}
	
	logger.Info("Order sync complete")
}

func (c *Client) reconnect() {
	logger.Info("Reconnecting...")
	time.Sleep(5 * time.Second)
	ctx := context.Background()
	c.Connect(ctx)
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
