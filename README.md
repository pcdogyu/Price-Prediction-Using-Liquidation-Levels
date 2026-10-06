# 清算墙概率预测系统

这是一个只使用公开交易所数据的 Go 单体服务。它为 BTCUSDT、ETHUSDT 估算清算压力地图，并预测未来 60 分钟最先发生的事件：触及上方墙、触及下方墙或均未触及。

项目不会抓取 CoinGlass，不读取浏览器 Cookie，也不会下单。地图是基于 OI 增量和杠杆先验的模型估计，并非交易所真实仓位。

## 已实现

- Binance、Bybit、OKX 的公开行情与强平 WebSocket；统一 UTC、USD 名义价值和被强平仓位方向。
- Binance、Bybit、OKX 1 分钟 K 线回填；Binance 与 Bybit 5 分钟 OI 历史回填。
- SQLite WAL 存储、幂等强平事件、采集健康状态、指数退避重连及 Binance 24 小时主动重连。
- 5×/10×/20×/50×/100× 清算层、时间衰减、价格穿越清除、ATR 自适应分桶、平滑和上下墙选择。
- 27 个不依赖未来数据的特征（包含缺失标志）、60 分钟三分类标签、Go/Gonum 多项逻辑回归、L2 正则和温度校准。
- 4 组扩展窗口回测、60 分钟隔离区、类别先验基线、Log Loss、Brier Score 和 ECE。
- JSON API、SSE、Prometheus 文本指标、健康检查和内嵌网页仪表盘。

历史交易所 API 不提供完整的逐笔强平回放。因此首次 30 天回测中的真实强平特征为缺失/零值；实时强平只从服务启动后积累。OKX 不提供与 Binance/Bybit 等价的逐合约历史 OI，本系统仍保存其实时 OI，但不会伪造历史值。

## 本地运行

要求 Go 1.21 或更高版本。

```bash
go test ./...
go run ./cmd/server
```

打开 `http://localhost:9090`。仪表盘显示三所中位合成价、最近24小时的15分钟K线、K线形态、垂直价格清算强度和上下墙触发状态。首次启动会在后台回填数据；在完成建图和训练前，API 会明确返回 `data_insufficient`。

常用配置见 [.env.example](.env.example)。服务本身不会读取 `.env` 文件；可由 systemd、Docker 或 shell 注入环境变量。

## API

```text
GET /api/v1/signals/latest?symbol=BTCUSDT
GET /api/v1/map?symbol=BTCUSDT
GET /api/v1/market?symbol=BTCUSDT
GET /api/v1/backtest?symbol=BTCUSDT
GET /api/v1/stream
GET /healthz
GET /readyz
GET /metrics
```

`state=ok` 才表示数据、双侧清算墙和模型均可用。`experimental=true` 表示模型尚未满足晋级门槛，不能解释为已证明有交易优势。

## 模型规则

- 每分钟更新公开行情，每 5 分钟固定一次墙并生成预测。
- 地图覆盖现价上下 5%，桶宽为 `max(5bp, 0.1×ATR(14))`。
- 双侧候选墙必须距离现价 0.25–2.5 ATR；按 `强度/(距离ATR+0.25)` 选择。
- 同一根 1 分钟 K 线同时触及上下墙的历史样本会剔除；不足完整 60 分钟观察期的尾部样本不会错误标记为 `neither`。
- 完整模型相对类别先验的样本外改善达到 3%，且 ECE 不高于 0.08，才允许取消模型层面的实验标志。产品规则仍要求累计至少 60 天实时数据并连续两次周评估通过后，才能在对外界面移除“实验模型”提示；目前界面始终保留该提示。

## Linux 部署

构建：

```bash
CGO_ENABLED=0 go build -trimpath -o liquidation-predictor ./cmd/server
```

代码和二进制部署到 `/opt/Price-Prediction-Using-Liquidation-Levels`，数据和模型目录配置到 `/var/lib/liquidation-predictor`，然后参考 [systemd 单元](deploy/liquidation-predictor.service) 和 [nginx 子路径配置](deploy/nginx-liquidation-location.conf)。生产环境让服务只监听 `127.0.0.1:9090`，通过 `/liquidation/` 反向代理对外访问。

SQLite 在线备份示例：

```bash
sqlite3 /var/lib/liquidation-predictor/liquidation.db ".backup '/var/backups/liquidation-$(date +%F).db'"
```

## 数据与风险说明

Binance 全市场强平流对每个交易对每 1000ms 只推送最近一笔，覆盖率在 API 中按采样源标记；Bybit 流按官方定义将 `Buy` 归一为多仓被强平。不同来源的原始笔数不可直接横向比较。

系统用于研究和可视化，不是投资建议。清算墙可能吸引价格，也可能成为加速穿越区；必须通过严格的时间序列样本外验证判断其是否提供增量信息。
