package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/notify"
)

const alertChartCandleLimit = 240

var alertChartLocation = time.FixedZone("UTC+8", 8*60*60)

// alertChartTask builds the 1m time slice around the alert event, then renders it
// when the queued photo reaches the Telegram worker. Keeping the provider call in
// the task preserves text-then-photo ordering without delaying liquidation
// ingestion on an upstream K-line request.
func (rtm *alertRuntime) alertChartTask(symbol, side string, at time.Time, referencePrice float64) notify.Task {
	until := at.UTC().Add(time.Minute)
	if at.IsZero() {
		until = time.Time{}
	}
	return rtm.alertChartTaskWindow(symbol, side, at, referencePrice, until, "报警时间")
}

// currentAlertChartTask is used only by the manual "触发报价" action. The
// alert may be hours old when the operator clicks it, so the chart must be
// fetched as the newest Binance window instead of replaying the alert-time
// candles. A zero until keeps Binance's currently forming candle in the view.
func (rtm *alertRuntime) currentAlertChartTask(symbol, side string) notify.Task {
	return rtm.alertChartTaskWindow(symbol, side, time.Now().UTC(), 0, time.Time{}, "触发报价时间")
}

func (rtm *alertRuntime) alertChartTaskWindow(symbol, side string, markerAt time.Time, referencePrice float64, until time.Time, captionLabel string) notify.Task {
	return notify.Task{
		Key:          "kline-photo|" + symbol + "|" + side,
		PhotoName:    "kline.png",
		PhotoCaption: fmt.Sprintf("%s 1m K线 · %s %s", symbol, captionLabel, markerAt.In(alertChartLocation).Format("2006-01-02 15:04:05 UTC+8")),
		Photo: func(ctx context.Context) ([]byte, error) {
			if rtm.service == nil {
				return nil, errors.New("alert service unavailable")
			}
			view, err := rtm.service.AlertKlines(ctx, symbol, "1m", alertChartCandleLimit, until)
			if err != nil {
				return nil, err
			}
			var timeline []domain.AlertTimelineItem
			if rtm.store != nil && len(view.Candles) > 0 {
				page, timelineErr := rtm.store.AlertTimeline(ctx, symbol, "", 200, "")
				if timelineErr != nil {
					rtm.log.Warn("alert chart history unavailable", "symbol", symbol, "error", timelineErr)
				} else {
					timeline = page.Rows
				}
			}
			return renderAlertChartPNG(view.Candles, markerAt, referencePrice, side, symbol, timeline)
		},
	}
}

func renderAlertChartPNG(candles []domain.MarketCandle, at time.Time, referencePrice float64, side, symbol string, history ...[]domain.AlertTimelineItem) ([]byte, error) {
	if len(candles) == 0 {
		return nil, errors.New("no candles available for alert chart")
	}
	const (
		width      = 1280
		height     = 720
		left       = 96
		right      = 96
		top        = 42
		bottom     = 78
		volumeHigh = 112
	)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 248, G: 250, B: 252, A: 255}}, image.Point{}, draw.Src)
	plot := image.Rect(left, top, width-right, height-bottom)
	pricePlot := image.Rect(plot.Min.X, plot.Min.Y, plot.Max.X, plot.Max.Y-volumeHigh)
	volumePlot := image.Rect(plot.Min.X, pricePlot.Max.Y+12, plot.Max.X, plot.Max.Y)

	low, high, volumePeak := candleRange(candles)
	pricePadding := (high - low) * 0.06
	if pricePadding <= 0 {
		pricePadding = math.Max(high*0.002, 1)
	}
	low -= pricePadding
	high += pricePadding
	if referencePrice > 0 && (referencePrice < low || referencePrice > high) {
		low = math.Min(low, referencePrice-pricePadding)
		high = math.Max(high, referencePrice+pricePadding)
	}

	grid := color.RGBA{R: 213, G: 223, B: 232, A: 255}
	for row := 0; row <= 5; row++ {
		y := pricePlot.Min.Y + row*(pricePlot.Dy()-1)/5
		drawHorizontal(img, pricePlot.Min.X, pricePlot.Max.X-1, y, grid)
	}
	for col := 0; col <= 8; col++ {
		x := pricePlot.Min.X + col*(pricePlot.Dx()-1)/8
		drawVertical(img, x, pricePlot.Min.Y, volumePlot.Max.Y, grid)
	}
	drawChartAxes(img, symbol, pricePlot, volumePlot, candles, low, high)

	step := float64(pricePlot.Dx()) / float64(len(candles))
	bodyWidth := int(math.Max(2, math.Min(10, step*0.68)))
	for index, candle := range candles {
		x := pricePlot.Min.X + int((float64(index)+0.5)*step)
		openY := priceY(candle.Open, low, high, pricePlot)
		closeY := priceY(candle.Close, low, high, pricePlot)
		highY := priceY(candle.High, low, high, pricePlot)
		lowY := priceY(candle.Low, low, high, pricePlot)
		up := candle.Close >= candle.Open
		candleColor := color.RGBA{R: 190, G: 38, B: 72, A: 255}
		if up {
			candleColor = color.RGBA{R: 14, G: 132, B: 92, A: 255}
		}
		drawVertical(img, x, highY, lowY, candleColor)
		bodyTop, bodyBottom := openY, closeY
		if bodyBottom < bodyTop {
			bodyTop, bodyBottom = bodyBottom, bodyTop
		}
		if bodyBottom == bodyTop {
			bodyBottom++
		}
		draw.Draw(img, image.Rect(x-bodyWidth/2, bodyTop, x-bodyWidth/2+bodyWidth, bodyBottom+1), &image.Uniform{C: candleColor}, image.Point{}, draw.Src)
		volumeHeight := int(float64(volumePlot.Dy()-1) * candle.VolumeUSD / volumePeak)
		if volumeHeight > 0 {
			draw.Draw(img, image.Rect(x-bodyWidth/2, volumePlot.Max.Y-volumeHeight, x-bodyWidth/2+bodyWidth, volumePlot.Max.Y), &image.Uniform{C: color.RGBA{R: 128, G: 166, B: 235, A: 150}}, image.Point{}, draw.Over)
		}
	}

	if len(history) > 0 {
		drawHistoricalSignals(img, history[0], candles, pricePlot, low, high, step)
	}

	markerAt := nearestCandleIndex(candles, at)
	if markerAt >= 0 {
		x := pricePlot.Min.X + int((float64(markerAt)+0.5)*step)
		drawDashedVertical(img, x, pricePlot.Min.Y, volumePlot.Max.Y, color.RGBA{R: 197, G: 133, B: 10, A: 255})
		markerPrice := referencePrice
		if markerPrice <= 0 {
			markerPrice = candles[markerAt].Close
		}
		markerY := priceY(markerPrice, low, high, pricePlot)
		markerColor := color.RGBA{R: 197, G: 133, B: 10, A: 255}
		if side == "short" {
			markerColor = color.RGBA{R: 210, G: 27, B: 72, A: 255}
		}
		drawDiamond(img, x, markerY, 8, markerColor)
	}
	if referencePrice > 0 {
		y := priceY(referencePrice, low, high, pricePlot)
		drawDashedHorizontal(img, pricePlot.Min.X, pricePlot.Max.X, y, color.RGBA{R: 76, G: 117, B: 145, A: 210})
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func drawChartAxes(img *image.RGBA, symbol string, pricePlot, volumePlot image.Rectangle, candles []domain.MarketCandle, low, high float64) {
	ink := color.RGBA{R: 73, G: 88, B: 103, A: 255}
	muted := color.RGBA{R: 92, G: 108, B: 123, A: 255}
	for row := 0; row <= 5; row++ {
		value := high - (high-low)*float64(row)/5
		y := pricePlot.Min.Y + row*(pricePlot.Dy()-1)/5
		label := chartPriceLabel(value)
		drawText(img, pricePlot.Min.X-len(label)*12-8, y-7, label, 2, ink)
		drawText(img, pricePlot.Max.X+10, y-7, label, 2, ink)
	}
	for col := 0; col <= 8; col++ {
		index := col * (len(candles) - 1) / 8
		x := pricePlot.Min.X + col*(pricePlot.Dx()-1)/8
		label := candles[index].Time.In(alertChartLocation).Format("15:04")
		textWidth := chartTextWidth(label, 2)
		labelX := x - textWidth/2
		labelX = maxInt(4, minInt(labelX, img.Bounds().Max.X-textWidth-4))
		drawText(img, labelX, volumePlot.Max.Y+10, label, 2, muted)
	}
	if symbol != "" {
		title := strings.ToUpper(symbol) + " 1M KLINE UTC+8"
		drawText(img, pricePlot.Min.X, 12, title, 2, ink)
	}
	drawChartLegend(img, pricePlot.Max.X-295, 12)
}

func drawChartLegend(img *image.RGBA, x, y int) {
	muted := color.RGBA{R: 92, G: 108, B: 123, A: 255}
	warning := color.RGBA{R: 225, G: 151, B: 35, A: 255}

	drawDiamond(img, x+4, y+4, 4, warning)
	drawText(img, x+11, y, "! WARN", 1, muted)
	x += 50
	drawTriangle(img, x+4, y+4, 4, signalColor("long"), false, true)
	drawText(img, x+11, y, "AGGR", 1, muted)
	x += 49
	drawTriangle(img, x+4, y+4, 4, signalColor("long"), true, true)
	drawText(img, x+11, y, "STEADY", 1, muted)
	x += 57
	drawTriangle(img, x+4, y+4, 4, signalColor("long"), true, true)
	drawText(img, x+11, y, "LONG", 1, muted)
	x += 43
	drawTriangle(img, x+4, y+4, 4, signalColor("short"), true, false)
	drawText(img, x+11, y, "SHORT", 1, muted)
}

func drawHistoricalSignals(img *image.RGBA, items []domain.AlertTimelineItem, candles []domain.MarketCandle, pricePlot image.Rectangle, low, high, step float64) {
	if len(candles) == 0 {
		return
	}
	from := candles[0].Time.UTC().Add(-time.Minute)
	to := candles[len(candles)-1].Time.UTC().Add(time.Minute)
	for _, item := range items {
		if item.EventAt.Before(from) || item.EventAt.After(to) {
			continue
		}
		index := nearestCandleIndex(candles, item.EventAt)
		if index < 0 {
			continue
		}
		x := pricePlot.Min.X + int((float64(index)+0.5)*step)
		price := candles[index].Close
		side := ""
		kind := item.Type
		if item.Alert != nil {
			price = item.Alert.ReferencePrice
			side = item.Alert.Side
		}
		if item.Signal != nil {
			price = item.Signal.ReferencePrice
			side = item.Signal.Side
		}
		if price <= 0 {
			price = candles[index].Close
		}
		y := priceY(price, low, high, pricePlot)
		markerColor := signalColor(side)
		switch kind {
		case "alert":
			drawDiamond(img, x, y, 5, color.RGBA{R: 225, G: 151, B: 35, A: 255})
		case domain.SignalAggressive:
			drawTriangle(img, x, y, 7, markerColor, false, side != "short")
		case domain.SignalSteady:
			drawTriangle(img, x, y, 7, markerColor, true, side != "short")
		}
	}
}

func signalColor(side string) color.RGBA {
	if side == "short" {
		return color.RGBA{R: 210, G: 27, B: 72, A: 255}
	}
	return color.RGBA{R: 14, G: 132, B: 92, A: 255}
}

func drawTriangle(img *image.RGBA, x, y, radius int, c color.Color, filled, up bool) {
	for row := 0; row <= radius; row++ {
		span := row
		if !up {
			span = radius - row
		}
		left, right := x-span, x+span
		lineY := y - radius + row
		if filled || row == 0 || row == radius {
			drawHorizontal(img, left, right, lineY, c)
		} else {
			drawVertical(img, left, lineY, lineY, c)
			drawVertical(img, right, lineY, lineY, c)
		}
	}
}

func chartPriceLabel(value float64) string {
	abs := math.Abs(value)
	switch {
	case abs >= 10000:
		return fmt.Sprintf("%.0f", value)
	case abs >= 100:
		return fmt.Sprintf("%.2f", value)
	case abs >= 1:
		return fmt.Sprintf("%.4f", value)
	case abs >= 0.01:
		return fmt.Sprintf("%.5f", value)
	default:
		return fmt.Sprintf("%.7f", value)
	}
}

func chartTextWidth(text string, scale int) int {
	if text == "" {
		return 0
	}
	return len([]rune(text))*(5*scale+scale) - scale
}

func drawText(img *image.RGBA, x, y int, text string, scale int, c color.Color) {
	for _, char := range strings.ToUpper(text) {
		glyph, ok := chartGlyphs[char]
		if !ok {
			glyph = chartGlyphs['?']
		}
		for row, bits := range glyph {
			for col, bit := range bits {
				if bit != '1' {
					continue
				}
				draw.Draw(img, image.Rect(x+col*scale, y+row*scale, x+(col+1)*scale, y+(row+1)*scale), &image.Uniform{C: c}, image.Point{}, draw.Over)
			}
		}
		x += 6 * scale
	}
}

var chartGlyphs = map[rune][]string{
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	'+': {"00000", "00100", "00100", "11111", "00100", "00100", "00000"},
	'.': {"00000", "00000", "00000", "00000", "00000", "00110", "00110"},
	'/': {"00001", "00010", "00100", "01000", "10000", "00000", "00000"},
	':': {"00000", "00110", "00110", "00000", "00110", "00110", "00000"},
	'?': {"01110", "10001", "00001", "00010", "00100", "00000", "00100"},
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
	'!': {"00100", "00100", "00100", "00100", "00100", "00000", "00100"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01110"},
	'I': {"11111", "00100", "00100", "00100", "00100", "00100", "11111"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}

func candleRange(candles []domain.MarketCandle) (low, high, volumePeak float64) {
	low = math.Inf(1)
	for _, candle := range candles {
		low = math.Min(low, candle.Low)
		high = math.Max(high, candle.High)
		volumePeak = math.Max(volumePeak, candle.VolumeUSD)
	}
	if !math.IsInf(low, 1) && high > low {
		return low, high, math.Max(volumePeak, 1)
	}
	return 0, 1, math.Max(volumePeak, 1)
}

func priceY(price, low, high float64, plot image.Rectangle) int {
	if high <= low {
		return plot.Min.Y + plot.Dy()/2
	}
	ratio := (high - price) / (high - low)
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	return plot.Min.Y + int(ratio*float64(plot.Dy()-1))
}

func nearestCandleIndex(candles []domain.MarketCandle, at time.Time) int {
	if at.IsZero() {
		return len(candles) - 1
	}
	best, distance := -1, time.Duration(1<<63-1)
	for index, candle := range candles {
		candidate := candle.Time.Sub(at)
		if candidate < 0 {
			candidate = -candidate
		}
		if candidate < distance {
			best, distance = index, candidate
		}
	}
	return best
}

func drawHorizontal(img *image.RGBA, x1, x2, y int, c color.Color) { drawLine(img, x1, y, x2, y, c) }
func drawVertical(img *image.RGBA, x, y1, y2 int, c color.Color)   { drawLine(img, x, y1, x, y2, c) }

func drawDashedHorizontal(img *image.RGBA, x1, x2, y int, c color.Color) {
	for x := x1; x < x2; x += 10 {
		drawHorizontal(img, x, minInt(x+5, x2), y, c)
	}
}

func drawDashedVertical(img *image.RGBA, x, y1, y2 int, c color.Color) {
	for y := y1; y < y2; y += 10 {
		drawVertical(img, x, y, minInt(y+5, y2), c)
	}
}

func drawDiamond(img *image.RGBA, x, y, radius int, c color.Color) {
	for offset := -radius; offset <= radius; offset++ {
		span := radius - absInt(offset)
		drawHorizontal(img, x-span, x+span, y+offset, c)
	}
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	dx, sx := absInt(x1-x0), 1
	if x0 > x1 {
		sx = -1
	}
	dy, sy := -absInt(y1-y0), 1
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		if image.Pt(x0, y0).In(img.Bounds()) {
			img.Set(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
