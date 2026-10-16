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
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/notify"
)

const alertChartCandleLimit = 240

// alertChartTask builds the same 1m time slice that the K-line page uses, then
// renders it when the queued photo reaches the Telegram worker. Keeping the
// provider call in the task preserves text-then-photo ordering without delaying
// liquidation ingestion on an upstream K-line request.
func (rtm *alertRuntime) alertChartTask(symbol, side string, at time.Time, referencePrice float64) notify.Task {
	return notify.Task{
		Key:          "kline-photo|" + symbol + "|" + side,
		PhotoName:    "kline.png",
		PhotoCaption: fmt.Sprintf("%s 1m K线 · 报警时间 %s", symbol, at.UTC().Format("2006-01-02 15:04:05 UTC")),
		Photo: func(ctx context.Context) ([]byte, error) {
			if rtm.service == nil {
				return nil, errors.New("alert service unavailable")
			}
			until := at.UTC().Add(time.Minute)
			if at.IsZero() {
				until = time.Time{}
			}
			view, err := rtm.service.AlertKlines(ctx, symbol, "1m", alertChartCandleLimit, until)
			if err != nil {
				return nil, err
			}
			return renderAlertChartPNG(view.Candles, at, referencePrice, side)
		},
	}
}

func renderAlertChartPNG(candles []domain.MarketCandle, at time.Time, referencePrice float64, side string) ([]byte, error) {
	if len(candles) == 0 {
		return nil, errors.New("no candles available for alert chart")
	}
	const (
		width      = 1280
		height     = 720
		left       = 72
		right      = 24
		top        = 28
		bottom     = 64
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
