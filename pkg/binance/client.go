package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hypefollow/pkg/config"
	"hypefollow/pkg/logger"
)

const (
	MainnetBaseURL = "https://fapi.binance.com"
	TestnetBaseURL = "https://testnet.binancefuture.com"
)

// Client handles Binance Futures API
type Client struct {
	apiKey    string
	apiSecret string
	baseURL   string
	httpClient *http.Client
}

// NewClient creates a new Binance client
func NewClient(cfg *config.Config) (*Client, error) {
	baseURL := MainnetBaseURL
	if cfg.BinanceUseTestnet {
		baseURL = TestnetBaseURL
	}

	return &Client{
		apiKey:     cfg.BinanceAPIKey,
		apiSecret:  cfg.BinanceAPISecret,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// VerifyAPI checks API connectivity
func (c *Client) VerifyAPI() error {
	_, err := c.GetAccount()
	return err
}

// GetAccount gets account info
func (c *Client) GetAccount() (map[string]interface{}, error) {
	return c.signedRequest("GET", "/fapi/v2/account", nil)
}

// CreateLimitOrder creates a limit order
func (c *Client) CreateLimitOrder(symbol, side string, price, quantity float64) (map[string]interface{}, error) {
	params := map[string]string{
		"symbol":   symbol,
		"side":     side,
		"type":     "LIMIT",
		"price":    strconv.FormatFloat(price, 'f', 1, 64),
		"quantity": strconv.FormatFloat(quantity, 'f', 3, 64),
		"timeInForce": "GTC",
	}
	
	logger.Info("Creating limit order",
		"symbol", symbol,
		"side", side,
		"price", price,
		"quantity", quantity,
	)
	
	return c.signedRequest("POST", "/fapi/v1/order", params)
}

// CancelOrder cancels an order
func (c *Client) CancelOrder(symbol string, orderID int64) error {
	params := map[string]string{
		"symbol":  symbol,
		"orderId": strconv.FormatInt(orderID, 10),
	}
	
	_, err := c.signedRequest("DELETE", "/fapi/v1/order", params)
	return err
}

// GetOpenOrders gets all open orders
func (c *Client) GetOpenOrders(symbol string) ([]map[string]interface{}, error) {
	params := map[string]string{}
	if symbol != "" {
		params["symbol"] = symbol
	}
	
	resp, err := c.signedRequest("GET", "/fapi/v1/openOrders", params)
	if err != nil {
		return nil, err
	}
	
	orders, ok := resp.([]map[string]interface{})
	if !ok {
		return []map[string]interface{}{}, nil
	}
	return orders, nil
}

func (c *Client) signedRequest(method, endpoint string, params map[string]string) (interface{}, error) {
	if params == nil {
		params = make(map[string]string)
	}
	
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	params["timestamp"] = timestamp
	
	// Build query string
	var queryParts []string
	for k, v := range params {
		queryParts = append(queryParts, fmt.Sprintf("%s=%s", k, v))
	}
	queryString := strings.Join(queryParts, "&")
	
	// Sign
	h := hmac.New(sha256.New, []byte(c.apiSecret))
	h.Write([]byte(queryString))
	signature := hex.EncodeToString(h.Sum(nil))
	queryString += "&signature=" + signature
	
	// Build URL
	url := c.baseURL + endpoint + "?" + queryString
	
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}
	
	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	
	return result, nil
}

// GetBinanceSymbol converts coin to Binance symbol
func GetBinanceSymbol(coin string) string {
	return coin + "USDT"
}
