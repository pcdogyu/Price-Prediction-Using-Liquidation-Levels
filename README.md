# 清算墙概率预测系统

这是一个以 Binance USDⓈ-M 永续公开数据为核心的 Go 单体服务。它为 BTCUSDT、ETHUSDT 估算清算压力地图，并预测未来 60 分钟最先发生的事件：触及上方墙、触及下方墙或均未触及；独立期权页展示 Deribit BTC / ETH Gamma。

核心模型默认只使用 Binance 公开数据，不会下单。可选的人工触发功能通过服务器上的持久化浏览器读取已登录 CoinGlass 页面收到的清算图响应；Cookie 始终留在浏览器配置目录，不会写入抓取文件。模型自身的地图仍是基于 OI 增量和杠杆先验的估计，并非交易所真实仓位。

## 已实现

- Binance USDⓈ-M 公开行情与强平 WebSocket；统一 UTC、USD 名义价值和被强平仓位方向，并在 K 线上以金额缩放圆圈实时显示逐笔爆仓。
- 复用 Binance `aggTrade` WebSocket，每 500ms 合并推送最新成交价，实时更新顶部价格、现价线和正在形成的 K 线；分钟 REST 数据继续负责完整行情校准和断线恢复。
- Binance 1 分钟 K 线、OI、资金费率及多空比回填，行情保留180天。
- Binance 聚合成交实时流、REST缺口补偿及官方日归档校验；按北京时间08:00和纽约09:30重置当前成交量分布，使用24个价格档并标记成交额前三档。
- SQLite WAL 存储、幂等强平事件、聚合成交游标、采集健康状态、指数退避重连及24小时主动重连。
- 10×/25×/50×/100× 清算层、时间衰减、价格穿越清除、ATR 自适应分桶、平滑和上下墙选择。
- 32 个不依赖未来数据的特征（包含 VAL/VAH 距离、价值区宽度及缺失标志）、60 分钟三分类标签、Go/Gonum 多项逻辑回归、L2 正则和温度校准。
- 4 组扩展窗口回测、60 分钟隔离区、类别先验基线、Log Loss、Brier Score 和 ECE。
- JSON API、SSE、Prometheus 文本指标、健康检查和内嵌网页仪表盘。

历史交易所 API 不提供完整的逐笔强平回放，因此回测中的真实强平特征为缺失/零值；实时强平只从服务启动后积累。成交量价值区使用 Binance 官方 USD-M `aggTrades` 日归档重建。连续30天归档完成并重新训练前，预测严格返回 `data_insufficient`。

## 本地运行

要求 Go 1.21 或更高版本。

```bash
go test ./...
go run ./cmd/server
```

打开 `http://localhost:9090`。仪表盘显示 Binance 永续价格、多周期交互式K线、当前时段70%成交量价值区、垂直清算强度和上下墙触发状态。首次启动会在后台增量回填180天行情和30天逐笔成交特征；在完成建图和训练前，API 会明确返回 `data_insufficient`。未配置 `APP_AUTH_USERNAME` 与 `APP_AUTH_PASSWORD_HASH` 时，本地开发默认不启用登录。

常用配置见 [.env.example](.env.example)。服务本身不会读取 `.env` 文件；可由 systemd、Docker 或 shell 注入环境变量。

## API

登录后默认进入“气泡图”，导航提供“清算历史”“对冲墙”“市场信息”“期权”。顶部“程序日志”左侧可切换浅色和深色主题，首次默认深色，选择保存在当前浏览器并在刷新、切换页面后恢复。

清算历史保存 Binance 全部 USDⓈ-M 币对的公开采样事件，筛选栏支持搜索并选择单个币对，默认全部币对；支持方向、数量和金额过滤及稳定游标分页。页面不提供时间输入，API 的时间参数仍兼容历史查询与内部分页；原气泡图仍仅显示 BTC/ETH。

清算历史顶部的四周期统计和组合描述固定使用 ETHUSDT 全量双侧数据，按 1H→4H→12H→24H 排列，不受逐笔明细的币对、方向或阈值筛选影响。金额较大的一侧标记为多头或空头清算占优，相等时显示均衡，零金额显示暂无清算；覆盖全部 16 种多空组合，缺失周期单独标识。多／空指被清算仓位类型，重叠窗口间的差异不代表已经发生价格趋势反转。统计与明细共用查询截止时间，历史分页保持该时间，分析更新失败时标记上次成功数据。

对冲墙由后台独立维护 BTC/ETH 本地盘口，与 CoinGlass 清算墙属于不同数据。盘口按交易对最小价格单位的十倍分桶；墙阈值为 25 万 USD 与同侧展示档位金额第 85 百分位的较大值，持续至少 3 秒并出现相邻档位确认才形成事件。无人访问网页时也会继续采集。SQLite 中每 5 秒保存压缩盘口快照，保留 30 天；墙事件保留 180 天，连接中断与服务重启单独标记。

市场信息的主动买卖及 CVD 使用 USD 金额，CVD 从所选窗口开始累计，缺口不会被记成零。净仓估算使用 OI 与顶级交易员持仓比，不代表真实全市场净仓。Gamma/GEX 基于 Binance Options 的公开 Gamma、OI、合约单位与指数价格计算，CALL 计正、PUT 计负，不代表做市商真实净仓；Gamma Wall 取绝对 GEX 最大的行权价。永续指标每分钟更新，期权每 5 分钟更新，接口返回缺失、部分可用或过期状态。

“期权”页固定展示 BTCUSDT / ETHUSDT 的 Deribit 归一化 Gamma 曲线、零轴、最新值、合约覆盖和更新时间。后台独立调用 Deribit 公开 API，按 OI 选取各币种前 80 个未到期且有持仓的期权，读取原始 Greeks Gamma，计算 `(ΣCALL Gamma − ΣPUT Gamma) / (ΣCALL Gamma + ΣPUT Gamma)`；OI 仅用于选样，不乘以 OI，与市场信息中的 Binance 美元 GEX 是不同口径。每分钟采集和刷新；历史从本服务采集起累计，按滚动 72 小时保存 3 天，每分钟自动清理过期记录，默认回看 24 小时，可选 1–72 小时窗口。合约缺失标记部分覆盖，失败保留最后成功值，超过 3 分钟标记过期；缺失不记为零，超过 150 秒的采样间隔断开曲线。无需 Deribit API 密钥，也不依赖其他项目的运行状态。

市场信息的 Gamma/GEX 柱状图高度为 338px（原 260px 增加 30%），并显示 Gamma Flip 价格卡片与虚线。Flip 使用 Binance `markIV`、利率、OI、合约单位和剩余到期时间，固定 IV 按 Black–Scholes 重估标的现价 50%～150% 范围内的净 GEX；多处零交叉时选取距离现价最近的一处。它与每个行权价柱子的净 GEX 变号不同。API 增加 `gamma_flip`、`gamma_flips`、`flip_state`、搜索范围及 IV 覆盖字段；无符号翻转或数据不足时价格为 `null`，不显示零价格。Flip 和期权链共用原来的 5 分钟刷新与持久化路径。

新增接口（均要求有效登录会话）：

```text
GET /api/v1/liquidations?symbol=ALL&side=all&field=notional_usd&minimum=0&limit=50&cursor=<cursor>&from=<RFC3339>&to=<RFC3339>
GET /api/v1/hedge-wall?symbol=ETHUSDT&half_life=120&window=5
GET /api/v1/hedge-wall/history?symbol=ETHUSDT&kind=events&limit=50&cursor=<cursor>&from=<RFC3339>&to=<RFC3339>
GET /api/v1/market-info?symbol=ETHUSDT&range=1h
GET /api/v1/options?hours=24
```

`field` 支持 `notional_usd` 或 `quantity`；`side` 指被清算仓位的 `long`/`short`，默认 `all`；`kind` 支持 `events` 或 `snapshots`。清算历史和气泡图均默认不设置金额门槛。历史自服务接收事件时开始积累，公开强平流无法回填部署前的完整逐笔历史。所有时间按 UTC 存储，页面使用北京时间显示。

```text
GET /api/v1/signals/latest?symbol=BTCUSDT
GET /api/v1/map?symbol=BTCUSDT
GET /api/v1/market?symbol=BTCUSDT&interval=15m&limit=120&before=<RFC3339>
GET /api/v1/volume-profile?symbol=BTCUSDT
GET /api/v1/backtest?symbol=BTCUSDT
GET /api/v1/logs?limit=200&level=INFO
GET /api/v1/stream
POST /api/v1/coinglass/capture
GET /api/v1/coinglass/latest
POST /auth/login
POST /auth/logout
GET /healthz
GET /readyz
GET /metrics
```

`state=ok` 才表示数据、双侧清算墙和模型均可用。`experimental=true` 表示模型尚未满足晋级门槛，不能解释为已证明有交易优势。

`/api/v1/market` 的 `liquidations` 字段包含当前 K 线窗口内最多最近 5,000 笔 Binance 强平事件，`liquidation_minimum_usd` 默认值为 0；超限时气泡图通过现有 `/api/v1/liquidations` 以零门槛、500 笔游标分页补齐可视时间窗口，固定查询截止时间并按事件 ID 去重。页面显示窗口加载笔数、屏内气泡数、价格视野外笔数与补齐进度，失败可重试；已完成的窗口缓存复用，SSE 与每 10 秒行情刷新校准新数据。切换币对或周期取消旧请求。

`position_side=long` 表示被强平的多仓，`position_side=short` 表示被强平的空仓。原始事件完整保存到 SQLite；“全部”仅指本服务实际采集的 Binance 强平采样流（每交易对每秒最近一笔），公开流无法提供完整市场逐笔覆盖。事件按实际时间定位，无需对应 K 线桶；以实际成交价为锚点，多单向下、空单向上偏移 6 像素，图边圈体保持完整，重叠事件可悬停滚动查看。

自动纵轴仅依据当前可见 K 线高低价，两侧各留跨度的 10% 余量；平价 K 线采用现价 0.1% 的最小跨度，无有效 K 线时等待。CoinGlass 墙、成交量分布、VAH/VAL 和预测价格不扩大纵轴，覆盖图层按共同价格轴裁剪；清算墙提示上下越界档数，Top3 保留完整价格并标记视野外。自动刷新保留手动缩放与历史位置，首次打开、切换币对/周期、返回最新、重置缩放恢复 K 线基准范围。K 线区域支持水平和垂直拖动，滚轮按光标价格缩放纵轴。

生产环境可用 `liquidation-predictor hash-password` 从标准输入生成 Argon2id PHC 哈希，并通过 `APP_AUTH_PASSWORD_HASH` 注入主账号。也可以将密码从标准输入传给 `liquidation-predictor add-user <用户名>`，在 SQLite 中新增或更新额外账号；数据库仅保存 Argon2id 哈希。新增账号后重启服务即可加载，主账号及已有会话不受影响。登录会话有效期为 7 天，Cookie 使用 Secure、HttpOnly 和 SameSite=Strict；程序日志以 JSON Lines 写入 `APP_LOG_PATH`，日志接口只允许已登录会话访问。

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

可选的 CoinGlass 人工登录窗口由服务器上的持久化 Chrome、Xvfb、x11vnc 和 noVNC 组成。相关 systemd 单元位于 `deploy/coinglass-*.service`；VNC、noVNC 和 Chrome CDP 分别只监听 `127.0.0.1:5900`、`127.0.0.1:6080` 和 `127.0.0.1:9222`。Nginx 的 `/liquidation/coinglass-login/` 必须通过应用的 `/api/v1/auth/check` 子请求鉴权，不能将这些端口直接暴露到公网。Chrome 登录状态保存在 `/var/lib/liquidation-predictor/coinglass-profile`，其权限应限制为服务用户可读写。登录后可由仪表盘的“抓取 CoinGlass”按钮调用 `POST /api/v1/coinglass/capture`；服务也会按照 `APP_COINGLASS_CAPTURE_INTERVAL`（默认 10 分钟）自动抓取。每次结果保存由 CoinGlass 页面自身运行时解码得到的 BTC/ETH、Binance/全交易所四组结构化 JSON；浏览器 Cookie 不会写入结果。仪表盘选择 BTC 或 ETH 后，`/api/v1/map` 会自动使用对应的 `Binance_BTCUSDT` 或 `Binance_ETHUSDT` 数据，并在最右侧清算墙按 10x/25x/50x/100x 分色显示，同时列出多头与空头清算强度 Top 3 价格。每次抓取还会以该快照现价分别汇总上下方清算金额：下方更大时显示绿色向上箭头，上方更大时显示红色向下箭头；金额相等时不显示方向箭头，方向仅随下一次 CoinGlass 抓取重新计算。该接口只提供两小时内的 CoinGlass 数据，过期或解析不可用时返回 503，不再回退本地估算地图。

SQLite 在线备份示例：

```bash
sqlite3 /var/lib/liquidation-predictor/liquidation.db ".backup '/var/backups/liquidation-$(date +%F).db'"
```

## 数据与风险说明

清算与行情模型读取 Binance USDⓈ-M 永续合约，期权观察页独立读取 Deribit 公开期权数据。Binance 全市场强平流对每个交易对每1000ms只推送最近一笔，因此覆盖率在 API 中按采样源标记。VAL/VAH 根据聚合成交名义额计算，POC只用于内部确定连续70%价值区域，不由API返回。

系统用于研究和可视化，不是投资建议。清算墙可能吸引价格，也可能成为加速穿越区；必须通过严格的时间序列样本外验证判断其是否提供增量信息。
