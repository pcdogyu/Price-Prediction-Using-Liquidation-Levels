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
	Symbol     string          `json:"symbol"`
	Scope      string          `json:"scope"`
	Code       string          `json:"code"`
	Success    bool            `json:"success"`
	Bytes      int             `json:"bytes"`
	CapturedAt time.Time       `json:"captured_at,omitempty"`
	Data       json.RawMessage `json:"data"`
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
	return c.capture(ctx, "BTCUSDT", "ETHUSDT")
}

// CaptureSymbol refreshes one Binance perpetual pair on demand. Scheduled
// captures keep the common BTC/ETH data warm, while arbitrary dashboard pairs
// such as SOLUSDT are fetched only when a user opens them.
func (c *Capturer) CaptureSymbol(ctx context.Context, symbol string) (Result, error) {
	return c.capture(ctx, symbol)
}

func (c *Capturer) capture(ctx context.Context, rawSymbols ...string) (Result, error) {
	if !c.mu.TryLock() {
		return Result{}, ErrBusy
	}
	defer c.mu.Unlock()
	symbols, err := normalizeCaptureSymbols(rawSymbols...)
	if err != nil {
		return Result{}, err
	}

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
	result.Parsed, err = decodeMapResponses(conn, 1, symbols)
	if err != nil {
		return Result{}, err
	}
	for index := range result.Parsed {
		result.Parsed[index].CapturedAt = result.CapturedAt
	}
	result.File, err = c.save(result)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

const capturePrefix = "coinglass-"

// PruneCaptures deletes the timestamped capture files written before now minus
// keep and reports how many went away. latest.json is the live pointer the map
// page reads, so it is never touched, and a missing directory is not an error.
func PruneCaptures(dir string, now time.Time, keep time.Duration) (int, error) {
	if strings.TrimSpace(dir) == "" {
		return 0, nil
	}
	if keep <= 0 {
		return 0, errors.New("coinglass capture retention must be positive")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read coinglass capture directory: %w", err)
	}
	cutoff := now.Add(-keep)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !isCaptureName(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.ModTime().After(cutoff) {
			continue
		}
		if removeErr := os.Remove(filepath.Join(dir, entry.Name())); removeErr != nil {
			if errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("delete coinglass capture %s: %w", entry.Name(), removeErr)
		}
		removed++
	}
	return removed, nil
}

// Prune applies PruneCaptures to the directory this capturer writes into.
func (c *Capturer) Prune(now time.Time, keep time.Duration) (int, error) {
	return PruneCaptures(c.dir, now, keep)
}

// isCaptureName matches only the timestamped captures, never latest.json.
func isCaptureName(name string) bool {
	return len(name) > len(capturePrefix)+len(".json") && strings.HasPrefix(name, capturePrefix) && strings.HasSuffix(name, ".json")
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
	capturedAt := selected.CapturedAt
	if capturedAt.IsZero() {
		// Captures written before per-symbol timestamps were introduced remain
		// readable and use the capture-level timestamp as a compatibility path.
		capturedAt = latest.CapturedAt
	}
	if maxAge > 0 && time.Since(capturedAt) > maxAge {
		return engine.MapResult{}, fmt.Errorf("latest CoinGlass capture is stale: %s", capturedAt.Format(time.RFC3339))
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
	aboveUSD, belowUSD, direction := liquidationDirection(bins, data.LastPrice)
	return engine.MapResult{
		DataSource:           "coinglass_binance_liqmap",
		Leverages:            []float64{10, 25, 50, 100},
		Bins:                 bins,
		MarkPrice:            data.LastPrice,
		BinWidth:             binWidth,
		CapturedAt:           &capturedAt,
		TopLong:              strongestLiquidations(bins, true, 3),
		TopShort:             strongestLiquidations(bins, false, 3),
		LiquidationAboveUSD:  aboveUSD,
		LiquidationBelowUSD:  belowUSD,
		LiquidationDirection: direction,
	}, nil
}

func liquidationDirection(bins []domain.MapBin, markPrice float64) (aboveUSD, belowUSD float64, direction string) {
	for _, bin := range bins {
		if bin.Price > markPrice {
			aboveUSD += bin.TotalUSD
		} else if bin.Price < markPrice {
			belowUSD += bin.TotalUSD
		}
	}
	switch {
	case belowUSD > aboveUSD:
		direction = "up"
	case aboveUSD > belowUSD:
		direction = "down"
	default:
		direction = "balanced"
	}
	return
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

func normalizeCaptureSymbols(rawSymbols ...string) ([]string, error) {
	if len(rawSymbols) == 0 {
		return nil, errors.New("at least one CoinGlass symbol is required")
	}
	seen := make(map[string]struct{}, len(rawSymbols))
	symbols := make([]string, 0, len(rawSymbols))
	for _, raw := range rawSymbols {
		raw = strings.ToUpper(strings.TrimSpace(raw))
		raw = strings.TrimSuffix(raw, "USDT")
		if raw == "" {
			return nil, errors.New("CoinGlass symbol is empty")
		}
		for _, r := range raw {
			if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				return nil, fmt.Errorf("invalid CoinGlass symbol %q", raw)
			}
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		symbols = append(symbols, raw)
	}
	return symbols, nil
}

func mergeParsedResponses(previous, current []ParsedResponse, capturedAt time.Time) []ParsedResponse {
	merged := make([]ParsedResponse, 0, len(previous)+len(current))
	positions := make(map[string]int, len(previous)+len(current))
	for _, item := range previous {
		if item.CapturedAt.IsZero() {
			item.CapturedAt = capturedAt
		}
		key := strings.ToUpper(item.Symbol) + "|" + strings.ToLower(item.Scope)
		positions[key] = len(merged)
		merged = append(merged, item)
	}
	for _, item := range current {
		key := strings.ToUpper(item.Symbol) + "|" + strings.ToLower(item.Scope)
		if index, ok := positions[key]; ok {
			merged[index] = item
			continue
		}
		positions[key] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func decodeMapResponses(conn *websocket.Conn, id int, symbols []string) ([]ParsedResponse, error) {
	symbolJSON, err := json.Marshal(symbols)
	if err != nil {
		return nil, fmt.Errorf("encode CoinGlass symbols: %w", err)
	}
	expression := fmt.Sprintf(`(async()=>{
		let requireModule;
		window.webpackChunk_N_E.push([[Date.now()],{},value=>{requireModule=value}]);
		if(!requireModule) throw new Error('CoinGlass webpack runtime is unavailable');
		const api=requireModule(89390), common={merge:true,interval:1,limit:1500};
		const symbols=%s;
		const jobs=symbols.flatMap(symbol=>[
			[symbol,'binance',api.QSv({...common,symbol:'Binance_'+symbol+'USDT'})],
			[symbol,'aggregate',api.bvP({...common,symbol})]
		]);
		const values=await Promise.all(jobs.map(async([symbol,scope,promise])=>{
			const value=await promise;
			return {symbol,scope,code:String(value?.code??''),success:Boolean(value?.success),data:value?.data??null};
		}));
		return JSON.stringify(values);
	})()`, string(symbolJSON))
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
		expected := len(symbols) * 2
		if len(parsed) != expected {
			return nil, fmt.Errorf("expected %d parsed CoinGlass maps, got %d", expected, len(parsed))
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
	if previous, err := c.Latest(); err == nil {
		result.Parsed = mergeParsedResponses(previous.Parsed, result.Parsed, previous.CapturedAt)
	}
	name := capturePrefix + result.CapturedAt.Format("20060102T150405.000000000Z") + ".json"
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
