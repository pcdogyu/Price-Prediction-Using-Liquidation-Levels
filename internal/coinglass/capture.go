package coinglass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/engine"
)

const mapURL = "https://www.coinglass.com/pro/futures/LiquidationMap"

var (
	ErrBusy       = errors.New("a CoinGlass capture is already running")
	requestTimout = 45 * time.Second
)

type Capturer struct {
	debugURL string
	dir      string
	client   *http.Client
	mu       sync.Mutex
}

type Response struct {
	URL    string          `json:"url"`
	Status int             `json:"status"`
	Bytes  int             `json:"bytes"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type Result struct {
	CapturedAt time.Time        `json:"captured_at"`
	PageURL    string           `json:"page_url"`
	File       string           `json:"file"`
	Responses  []Response       `json:"responses,omitempty"`
	Parsed     []ParsedResponse `json:"parsed"`
}

type ParsedResponse struct {
	Symbol  string          `json:"symbol"`
	Scope   string          `json:"scope"`
	Code    string          `json:"code"`
	Success bool            `json:"success"`
	Bytes   int             `json:"bytes"`
	Data    json.RawMessage `json:"data"`
}

type target struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type cdpMessage struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func New(debugURL, dir string) *Capturer {
	return &Capturer{
		debugURL: strings.TrimRight(debugURL, "/"),
		dir:      dir,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Capturer) Capture(ctx context.Context) (Result, error) {
	if !c.mu.TryLock() {
		return Result{}, ErrBusy
	}
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, requestTimout)
	defer cancel()
	page, err := c.page(ctx)
	if err != nil {
		return Result{}, err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, page.WebSocketDebuggerURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("connect to Chrome page: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(64 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(requestTimout))

	result := Result{CapturedAt: time.Now().UTC(), PageURL: page.URL}
	result.Parsed, err = decodeMapResponses(conn, 1)
	if err != nil {
		return Result{}, err
	}
	result.File, err = c.save(result)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (c *Capturer) Latest() (Result, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, "latest.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, errors.New("no CoinGlass capture is available yet")
		}
		return Result{}, fmt.Errorf("read latest CoinGlass capture: %w", err)
	}
	var result Result
	if err = json.Unmarshal(data, &result); err != nil {
		return Result{}, fmt.Errorf("decode latest CoinGlass capture: %w", err)
	}
	return result, nil
}

func (c *Capturer) LiquidationMap(symbol string, maxAge time.Duration) (engine.MapResult, error) {
	latest, err := c.Latest()
	if err != nil {
		return engine.MapResult{}, err
	}
	if maxAge > 0 && time.Since(latest.CapturedAt) > maxAge {
		return engine.MapResult{}, fmt.Errorf("latest CoinGlass capture is stale: %s", latest.CapturedAt.Format(time.RFC3339))
	}
	symbol = strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(symbol)), "USDT")
	var selected *ParsedResponse
	for index := range latest.Parsed {
		item := &latest.Parsed[index]
		if item.Symbol == symbol && item.Scope == "binance" && item.Code == "0" && item.Success {
			selected = item
			break
		}
	}
	if selected == nil {
		return engine.MapResult{}, fmt.Errorf("CoinGlass Binance map for %s is unavailable", symbol)
	}
	var data struct {
		LastPrice float64            `json:"lastPrice"`
		LiqMapV2  map[string][][]any `json:"liqMapV2"`
	}
	if err = json.Unmarshal(selected.Data, &data); err != nil {
		return engine.MapResult{}, fmt.Errorf("decode CoinGlass %s liquidation map: %w", symbol, err)
	}
	if data.LastPrice <= 0 || len(data.LiqMapV2) == 0 {
		return engine.MapResult{}, fmt.Errorf("CoinGlass %s liquidation map is empty", symbol)
	}
	allowed := map[int]bool{10: true, 25: true, 50: true, 100: true}
	bins := make([]domain.MapBin, 0, len(data.LiqMapV2))
	for priceText, rows := range data.LiqMapV2 {
		price, parseErr := strconv.ParseFloat(priceText, 64)
		if parseErr != nil || price <= 0 {
			continue
		}
		bin := domain.MapBin{Price: price, LeverageUSD: make(map[string]float64)}
		for _, row := range rows {
			if len(row) < 3 {
				continue
			}
			amount, amountOK := row[1].(float64)
			leverageValue, leverageOK := row[2].(float64)
			leverage := int(leverageValue)
			if !amountOK || !leverageOK || amount <= 0 || !allowed[leverage] {
				continue
			}
			bin.TotalUSD += amount
			bin.LeverageUSD[strconv.Itoa(leverage)] += amount
		}
		if bin.TotalUSD <= 0 {
			continue
		}
		if price < data.LastPrice {
			bin.LongUSD = bin.TotalUSD
		} else {
			bin.ShortUSD = bin.TotalUSD
		}
		bins = append(bins, bin)
	}
	if len(bins) == 0 {
		return engine.MapResult{}, fmt.Errorf("CoinGlass %s liquidation map has no supported leverage rows", symbol)
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].Price < bins[j].Price })
	widths := make([]float64, 0, len(bins)-1)
	for index := 1; index < len(bins); index++ {
		if difference := bins[index].Price - bins[index-1].Price; difference > 0 {
			widths = append(widths, difference)
		}
	}
	binWidth := data.LastPrice * .0005
	if len(widths) > 0 {
		sort.Float64s(widths)
		binWidth = widths[len(widths)/2]
	}
	capturedAt := latest.CapturedAt
	return engine.MapResult{
		DataSource: "coinglass_binance_liqmap",
		Leverages:  []float64{10, 25, 50, 100},
		Bins:       bins,
		MarkPrice:  data.LastPrice,
		BinWidth:   binWidth,
		CapturedAt: &capturedAt,
		TopLong:    strongestLiquidations(bins, true, 3),
		TopShort:   strongestLiquidations(bins, false, 3),
	}, nil
}

func strongestLiquidations(bins []domain.MapBin, long bool, limit int) []domain.LiquidationPeak {
	peaks := make([]domain.LiquidationPeak, 0, len(bins))
	for _, bin := range bins {
		amount := bin.ShortUSD
		if long {
			amount = bin.LongUSD
		}
		if amount > 0 {
			peaks = append(peaks, domain.LiquidationPeak{Price: bin.Price, AmountUSD: amount})
		}
	}
	sort.Slice(peaks, func(i, j int) bool {
		if peaks[i].AmountUSD == peaks[j].AmountUSD {
			return peaks[i].Price < peaks[j].Price
		}
		return peaks[i].AmountUSD > peaks[j].AmountUSD
	})
	if len(peaks) > limit {
		peaks = peaks[:limit]
	}
	return peaks
}

func decodeMapResponses(conn *websocket.Conn, id int) ([]ParsedResponse, error) {
	const expression = `(async()=>{
		let requireModule;
		window.webpackChunk_N_E.push([[Date.now()],{},value=>{requireModule=value}]);
		if(!requireModule) throw new Error('CoinGlass webpack runtime is unavailable');
		const api=requireModule(89390), common={merge:true,interval:1,limit:1500};
		const jobs=[
			['BTC','binance',api.QSv({...common,symbol:'Binance_BTCUSDT'})],
			['BTC','aggregate',api.bvP({...common,symbol:'BTC'})],
			['ETH','binance',api.QSv({...common,symbol:'Binance_ETHUSDT'})],
			['ETH','aggregate',api.bvP({...common,symbol:'ETH'})]
		];
		const values=await Promise.all(jobs.map(async([symbol,scope,promise])=>{
			const value=await promise;
			return {symbol,scope,code:String(value?.code??''),success:Boolean(value?.success),data:value?.data??null};
		}));
		return JSON.stringify(values);
	})()`
	if err := writeCommand(conn, id, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true, "timeout": 40000}); err != nil {
		return nil, err
	}
	for {
		var message cdpMessage
		if err := conn.ReadJSON(&message); err != nil {
			return nil, fmt.Errorf("wait for decoded CoinGlass data: %w", err)
		}
		if message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("decode CoinGlass data in browser: %s", message.Error.Message)
		}
		var evaluation struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
			Exception *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		}
		if err := json.Unmarshal(message.Result, &evaluation); err != nil {
			return nil, fmt.Errorf("decode Chrome evaluation result: %w", err)
		}
		if evaluation.Exception != nil {
			return nil, fmt.Errorf("CoinGlass parser failed: %s", evaluation.Exception.Text)
		}
		var parsed []ParsedResponse
		if err := json.Unmarshal([]byte(evaluation.Result.Value), &parsed); err != nil {
			return nil, fmt.Errorf("decode structured CoinGlass JSON: %w", err)
		}
		if len(parsed) != 4 {
			return nil, fmt.Errorf("expected 4 parsed CoinGlass maps, got %d", len(parsed))
		}
		for index := range parsed {
			parsed[index].Bytes = len(parsed[index].Data)
			if !json.Valid(parsed[index].Data) {
				return nil, fmt.Errorf("parsed CoinGlass %s/%s data is not JSON", parsed[index].Symbol, parsed[index].Scope)
			}
		}
		return parsed, nil
	}
}

func (c *Capturer) page(ctx context.Context) (target, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.debugURL+"/json/list", nil)
	if err != nil {
		return target{}, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return target{}, fmt.Errorf("query Chrome pages: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return target{}, fmt.Errorf("query Chrome pages: HTTP %d", response.StatusCode)
	}
	var pages []target
	if err = json.NewDecoder(response.Body).Decode(&pages); err != nil {
		return target{}, fmt.Errorf("decode Chrome pages: %w", err)
	}
	for _, page := range pages {
		if page.Type == "page" && strings.Contains(page.URL, "coinglass.com") && page.WebSocketDebuggerURL != "" {
			return page, nil
		}
	}
	return target{}, errors.New("no CoinGlass page is open in the persistent browser")
}

func (c *Capturer) save(result Result) (string, error) {
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return "", fmt.Errorf("create capture directory: %w", err)
	}
	name := "coinglass-" + result.CapturedAt.Format("20060102T150405.000000000Z") + ".json"
	path := filepath.Join(c.dir, name)
	result.File = path
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode capture: %w", err)
	}
	if err = atomicWrite(path, data); err != nil {
		return "", fmt.Errorf("write capture: %w", err)
	}
	if err = atomicWrite(filepath.Join(c.dir, "latest.json"), data); err != nil {
		return "", fmt.Errorf("write latest capture: %w", err)
	}
	return path, nil
}

func atomicWrite(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func writeCommand(conn *websocket.Conn, id int, method string, params any) error {
	if err := conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("send Chrome command %s: %w", method, err)
	}
	return nil
}
