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
- 报警页：成交额前 100 币对的 3 分钟爆仓窗口监控、激进/稳健下单信号、1 年预警与信号留存及 Telegram 推送；预警与信号历史页仅展示最近 30 天，固定每页 20 条；支持按当前登录租户参数回放过去 2 周，比较预警、激进、稳健与无过滤/VWAP/Donchian/VWAP + Donchian 四种开仓区组合，按 15 分钟持有输出交易数、加仓次数、胜率、盈利因子、最大回撤和最终权益。
- 自动下单 webhook：预警、激进信号和稳健信号落库后异步发送 TradingView 风格 JSON 到 `APP_STEADY_WEBHOOK_URL`；默认目标为 `https://tvbot.lmitis.com/tvorder`，默认使用 Binance demo 市场，API ID 留空时由 tvbot 使用当前激活的 Binance API。三类实时信号都可在报警页分别配置平仓和开仓开关；开启的动作才会自动发送，关闭动作不影响历史记录、Telegram 通知或手动触发下单。只配置 `APP_STEADY_WEBHOOK_TOKEN_NONCE` 与 `APP_STEADY_WEBHOOK_TOKEN` 后启用，凭据不写入数据库或日志；每个币对和信号类型保留 5 分钟发送抑制。
- 币对 K 线页：报警页币对名点击后在新标签页打开该币对 K 线，九档周期、拖动平移、滚轮缩放并叠加下单信号标记；持仓详情中同一根 K 线、同一方向的多个开仓或平仓会合并为一个标记，合并的平仓标记显示次数。

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

登录后默认进入“气泡图”，导航提供“清算历史”“对冲墙”“市场信息”“期权”“报警”。顶部“程序日志”左侧可切换浅色和深色主题，首次默认深色；登录用户的主题保存在认证 SQLite 数据库中，可在不同电脑间同步，浏览器本地存储仅用于首屏缓存和服务异常时降级。未启用登录时仍只使用当前浏览器主题。

清算历史保存 Binance 全部 USDⓈ-M 币对的公开采样事件，筛选栏支持搜索并选择单个币对，默认全部币对；支持方向、数量和金额过滤及稳定游标分页。页面不提供时间输入，API 的时间参数仍兼容历史查询与内部分页；气泡图支持 Binance 全部可交易 USDT 永续，未配置后台持续采集的币对按需读取行情和 CoinGlass 清算图。

清算历史顶部的六周期统计和组合描述跟随当前选择的币对，按 5M→15M→1H→4H→12H→24H 排列，不受逐笔明细的方向或阈值筛选影响；选择“全部币对”时按全部币对聚合。金额较大的一侧标记为多头或空头清算占优，相等时显示均衡，零金额显示暂无清算；支持全部 64 种多空组合，解析表仅展示当前状态；均衡、暂无清算与缺失周期单独标识。多／空表示强平对应的方向：空头被清算为多，多头被清算为空，重叠窗口间的差异不代表已经发生价格趋势反转。统计与明细共用查询截止时间，历史分页保持该时间，分析更新失败时标记上次成功数据。

对冲墙由后台独立维护 BTC/ETH 本地盘口，与 CoinGlass 清算墙属于不同数据。盘口按交易对最小价格单位的十倍分桶；墙阈值为 25 万 USD 与同侧展示档位金额第 85 百分位的较大值，持续至少 3 秒并出现相邻档位确认才形成事件。无人访问网页时也会继续采集。SQLite 中每 5 秒保存压缩盘口快照，保留 30 天；墙事件保留 180 天，连接中断与服务重启单独标记。

市场信息的主动买卖及 CVD 使用 USD 金额，CVD 从所选窗口开始累计，缺口不会被记成零。净仓估算使用 OI 与顶级交易员持仓比，不代表真实全市场净仓。Gamma/GEX 基于 Binance Options 的公开 Gamma、OI、合约单位与指数价格计算，CALL 计正、PUT 计负，不代表做市商真实净仓；Gamma Wall 取绝对 GEX 最大的行权价。永续指标每分钟更新，期权每 5 分钟更新，接口返回缺失、部分可用或过期状态。

“期权”页固定展示 BTCUSDT / ETHUSDT 的 Deribit 归一化 Gamma 曲线、零轴、最新值、合约覆盖和更新时间。后台独立调用 Deribit 公开 API，按 OI 选取各币种前 80 个未到期且有持仓的期权，读取原始 Greeks Gamma，计算 `(ΣCALL Gamma − ΣPUT Gamma) / (ΣCALL Gamma + ΣPUT Gamma)`；OI 仅用于选样，不乘以 OI，与市场信息中的 Binance 美元 GEX 是不同口径。报警防守也使用该归一化 Gamma：BTCUSDT 使用 BTC Gamma，ETHUSDT 及其他币对使用 ETH Gamma；对应值小于 0 时只发送本租户已开启的平仓，不发送自动开仓。采集和页面刷新对齐为每 5 分钟一次；最近 24 小时保留完整采样，更早的数据按半小时槽选择最接近整点或半点的一条，每小时两条，最长保留 90 天。北京时间每天 03:30 随数据库维护分批降采样并清理过期记录，查询层也会即时过滤尚未清理的冗余点；默认回看 24 小时，可选 1–2160 小时窗口。合约缺失标记部分覆盖，失败保留最后成功值，超过 12 分钟没有新采样标记过期；缺失不记为零。无需 Deribit API 密钥，也不依赖其他项目的运行状态。

市场信息的 Gamma/GEX 柱状图高度为 440px（在 338px 基础上再增加约 30%），显示自适应密度的行权价刻度、等间隔净 GEX USD 刻度及横纵网格线，并显示 Gamma Flip 价格卡片与虚线。Flip 使用 Binance `markIV`、利率、OI、合约单位和剩余到期时间，固定 IV 按 Black–Scholes 重估标的现价 50%～150% 范围内的净 GEX；多处零交叉时选取距离现价最近的一处。它与每个行权价柱子的净 GEX 变号不同。API 增加 `gamma_flip`、`gamma_flips`、`flip_state`、搜索范围及 IV 覆盖字段；无符号翻转或数据不足时价格为 `null`，不显示零价格。Flip 和期权链共用持久化路径。

- “报警”页监控 Binance USDT 本位永续成交额前 100 个币对（BTCUSDT、ETHUSDT 始终保留），每 5 秒刷新可调周期的滑动窗口。某侧爆仓金额超过该币对阈值（默认 BTC 30 万、ETH 20 万、其他 10 万 USD）只记录 `ARMED` 预警并追踪本轮爆仓速率峰值；只有最新 15–30 秒速率回落到峰值 40% 以下，且当前 1m K 线不再创出新低/新高或出现对应下影线/上影线吸收，才进入 `FIRED` 并按 VWAP、深度配额、ATR、网格和最小获利间距决定开仓或仅平仓。预警后该侧继续爆仓只按最新事件顺延观察截止，不重复发送同类信号；反向爆仓在 30 分钟确认窗口内可产生稳健信号。三类信号的历史记录和阈值配置均按登录租户隔离，不同租户互不读取或覆盖；历史记录中的“触发下单”仍保持手动完整开仓行为。反向确认与顺延的最小爆仓额默认 0（任意金额都计入），可用 `APP_ALERT_OPPOSITE_MIN_USD`、`APP_ALERT_FOLLOW_UP_MIN_USD` 提高门槛，用于后续调参。报警页可直接保存 BTC、ETH、其他币对阈值和周期（1–60 分钟），配置持久化到 SQLite，实时引擎立即使用；“信号重算”会按当前雷达参数回放过去 7 天；“报警回测（过去 2 周）”将回测本金、杠杆、止盈止损、每次下单、最大加仓次数和独立的 VWAP/Donchian 参数集中在回测区域，只有点击按钮才按当前表单值 POST JSON 并在后台执行，结果继续由页面轮询；固定回测 14 天、持有 15 分钟，采用收盘触发、下一根 K 线收盘成交，四种过滤组合仍分别比较无过滤、VWAP、Donchian 和 VWAP+Donchian。预警与信号均落库并保留 1 年，但历史页面只展示最近 30 天；页面通过 `GET /api/v1/alerts/timeline` 将预警、激进信号和稳健信号按发生时间统一混排，每页固定 20 条并可使用上一页/下一页翻页，且可按币对和记录类型筛选；原有 `GET /api/v1/alerts/history` 与 `GET /api/v1/alerts/signals` 继续用于独立导出和回测。程序重启会按当前周期回读事件与进行中的周期，重启回放不重复推送。SQLite 按所有保留期字段建立清理索引，北京时间每天 03:30 统一执行一次分批清理，每批最多 2000 行，避免长事务阻塞实时采集；服务启动后也会先执行一次。Telegram 通知在页面填写 Bot Token 与 Chat ID 后写入 SQLite（Token 只回显掩码，留空保存原值），也可用 `APP_TELEGRAM_BOT_TOKEN`、`APP_TELEGRAM_CHAT_ID` 提供默认值；消息含币对、方向、窗口爆仓金额、参考价与北京时间，推送走非阻塞队列并带同类冷却，队列积压时丢弃而不影响清算采集。页面区块顺序为概要指标、预警与信号历史、报警回测、报警参数、Telegram 通知、策略与数据口径，100 币对的实时监控表固定放在页面最下方，刷新后仍在文末。

报警页两个表格（实时监控、预警与信号历史）里的币对名现在是链接，点击后在浏览器新标签页打开该币对 K 线页 `/alerts/chart?symbol=<币对>&interval=<周期>`，报警页自身的滚动位置与筛选不受影响。K 线页提供 1m/2m/5m/15m/30m/1h/4h/8h/12h 九个周期，全部是 Binance 原生周期，浏览器与后端都不做聚合；周期选择和信号图层开关保存在当前浏览器。VWAP 分区开关只负责图中的分区显示，右侧独立的 Donchian 过滤开关负责开启或关闭屏幕上的信号过滤。K 线由本服务代理调用 Binance USDⓈ-M `klines` 接口取得：浏览器 CSP 不允许直连交易所，而被监控的 100 个币对中只有 `APP_SYMBOLS` 里的币对本地存了 K 线，代理后任意被监控币对都能画图。同一“币对+周期+条数+游标”的结果按 5 秒缓存，单页最多 1000 根，时间窗口按周期边界对齐，保证正在形成的最新一根一定在结果里（该根 `complete=false`）；`has_more` 与 `next_before` 供前端左拉时继续读取更早数据。图形支持按住拖动平移（横向移时间、纵向移价格并转为手动纵轴）、滚轮按光标时间缩放、Ctrl/Shift+滚轮或光标落在右侧价格刻度上时缩放价格、双击或“重置缩放”恢复默认视野；停在最新时每 5 秒跟随最新 K 线，拖离后停止跟随并提示“有新数据”，点“回到最新”恢复并补齐数据。下单信号图层默认打开，可分别隐藏全部信号、激进信号或稳健信号：做多用正三角、做空用倒三角，稳健实心、激进空心，同一根 K 线上同类型同方向的信号合并为一个标记并标注条数，纵向位置取信号参考价，悬停显示类型、方向、时间与条数，页面下方同时列出该币对的信号明细。`GET /api/v1/alerts/klines` 中 `symbol` 必填且只接受 2–32 位字母数字，`interval` 只接受上述九项（默认 15m），`limit` 为 1–1000（默认 300），`before` 为不早于 2010 年的 RFC3339 时间；非法参数直接返回 400，不转发给交易所，交易所返回错误时以 502 并把原文显示在页面提示里。

新增接口（均要求有效登录会话）：

```text
GET /api/v1/liquidations?symbol=ALL&side=all&field=notional_usd&minimum=0&limit=50&cursor=<cursor>&from=<RFC3339>&to=<RFC3339>
GET /api/v1/hedge-wall?symbol=ETHUSDT&half_life=120&window=5
GET /api/v1/hedge-wall/history?symbol=ETHUSDT&kind=events&limit=50&cursor=<cursor>&from=<RFC3339>&to=<RFC3339>
GET /api/v1/market-info?symbol=ETHUSDT&range=1h
GET /api/v1/options?hours=24
GET /api/v1/alerts
GET /api/v1/alerts/history?symbol=BTCUSDT&side=long&limit=50&cursor=<cursor>
GET /api/v1/alerts/signals?symbol=BTCUSDT&kind=steady&limit=50&cursor=<cursor>
GET /api/v1/alerts/timeline?symbol=BTCUSDT&kind=all&cursor=<cursor>
GET /api/v1/alerts/chart-signals?symbol=BTCUSDT
POST /api/v1/alerts/backtest
GET /api/v1/alerts/backtest?job_id=<job_id>
GET /api/v1/alerts/klines?symbol=DOGEUSDT&interval=15m&limit=300&before=<RFC3339>
GET|POST /api/v1/alerts/settings
GET|POST /api/v1/alerts/order-settings
GET|POST /api/v1/alerts/strategy-settings
POST /api/v1/alerts/settings/test
```

`POST /api/v1/alerts/backtest` 可发送一次性 JSON 回测参数；空请求使用页面默认值（本金 10000、1x、止盈 1.2%、止损 1.5%、每次下单 1000、最多加仓 1 次、VWAP Donchian 24 小时/2%/35%），不会写入实时报警参数。

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

`/api/v1/market` 的 `liquidations` 字段包含当前 K 线窗口内最多最近 5,000 笔 Binance 强平事件，`liquidation_minimum_usd` 默认值为 0；气泡图默认零门槛，也可在 24 小时周期按钮右侧输入金额阈值，通过现有 `/api/v1/liquidations` 以该门槛和 500 笔游标分页补齐可视时间窗口，固定查询截止时间并按事件 ID 去重。页面显示窗口加载笔数、屏内气泡数、价格视野外笔数与补齐进度，失败可重试；已完成的窗口缓存复用，SSE 与每 10 秒行情刷新校准新数据。切换币对或周期取消旧请求。

`position_side=long` 表示被强平的多仓，`position_side=short` 表示被强平的空仓。原始事件完整保存到 SQLite；“全部”仅指本服务实际采集的 Binance 强平采样流（每交易对每秒最近一笔），公开流无法提供完整市场逐笔覆盖。事件按实际时间定位，无需对应 K 线桶；以实际成交价为锚点，页面可分别调整多单下移和空单上移的像素偏移，默认均为 20 像素，图边圈体保持完整，重叠事件可悬停滚动查看。

自动纵轴仅依据当前可见 K 线高低价，两侧各留跨度的 10% 余量；平价 K 线采用现价 0.1% 的最小跨度，无有效 K 线时等待。CoinGlass 墙、成交量分布、VAH/VAL 和预测价格不扩大纵轴，覆盖图层按共同价格轴裁剪；清算墙提示上下越界档数，Top3 保留完整价格并标记视野外。自动刷新保留手动缩放与历史位置，首次打开、切换币对/周期、返回最新、重置缩放恢复 K 线基准范围。K 线区域支持水平和垂直拖动；滚轮按光标所在区域分工：落在 K 线上按光标时间缩放可见 K 线根数（24–500 根，上限即 `/api/v1/market` 单页最多 500 根，接近已加载边界时和左拉一样自动读取更早 K 线），落在“当前时段成交量”列只缩放成交量柱长度，落在清算墙列只缩放墙柱长度，两列倍率均为 0.25–12 倍且不请求额外数据，超出图形的柱体在各自列内裁断而不侵入相邻面板；Ctrl/Shift+滚轮或光标落在坐标轴与留白处仍按光标价格缩放纵轴。悬停提示、面板标题和图表上下文都会显示当前 K 线根数与两列倍率，“重置缩放”同时恢复默认 120 根 K 线、两列 1 倍与自动纵轴并保留当前浏览位置。

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
BUILD_TIME="$(TZ=Asia/Shanghai date --iso-8601=seconds)"
BUILD_REVISION="$(git rev-parse --short HEAD)"
BUILD_BRANCH="$(git branch --show-current)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags="-s -w -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildTime=${BUILD_TIME} -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildRevision=${BUILD_REVISION} -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildBranch=${BUILD_BRANCH}" -o liquidation-predictor-linux ./cmd/server
```

代码和二进制部署到 `/opt/Price-Prediction-Using-Liquidation-Levels`，数据和模型目录配置到 `/var/lib/liquidation-predictor`，然后参考 [systemd 单元](deploy/liquidation-predictor.service) 和 [nginx 子路径配置](deploy/nginx-liquidation-location.conf)。生产部署目标仅为 `tvbot.lmitis.com`，服务只监听 `127.0.0.1:9090`，通过 `/liquidation/` 反向代理对外访问；`mm.lmitis.com` 不部署本项目，也不应安装本项目的 systemd、CoinGlass 浏览器或 Nginx 路由。

生产发布使用 `deploy/publish-linux.ps1`：发布机按服务器架构执行测试、`go vet` 和 Linux 交叉编译，再使用 `pem/server.pem` 上传预构建二进制；服务器端 `deploy/unified-upgrade.sh` 只同步目标提交、校验二进制、备份上一版程序/配置并原子替换重启，不在部署前备份或复制 SQLite 数据库。已存在的一次性迁移快照只保留并使用，不由发布流程重新生成。

可选的 CoinGlass 人工登录窗口由服务器上的持久化 Chrome、Xvfb、x11vnc 和 noVNC 组成。相关 systemd 单元位于 `deploy/coinglass-*.service`；VNC、noVNC 和 Chrome CDP 分别只监听 `127.0.0.1:5900`、`127.0.0.1:6080` 和 `127.0.0.1:9222`。Nginx 的 `/liquidation/coinglass-login/` 必须通过应用的 `/api/v1/auth/check` 子请求鉴权，不能将这些端口直接暴露到公网。Chrome 登录状态保存在 `/var/lib/liquidation-predictor/coinglass-profile`，其权限应限制为服务用户可读写。登录后可由仪表盘的“抓取 CoinGlass”按钮调用 `POST /api/v1/coinglass/capture`；服务也会按照 `APP_COINGLASS_CAPTURE_INTERVAL`（默认 10 分钟）自动抓取 BTC/ETH。选择其它 Binance USDT 永续时，`/api/v1/map` 会按需请求对应的 CoinGlass Binance 图并合并到缓存；每个币对独立计算两小时有效期。每次结果保存由 CoinGlass 页面自身运行时解码得到 Binance/全交易所结构化 JSON；浏览器 Cookie 不会写入结果。清算墙按 10x/25x/50x/100x 分色显示，同时列出多头与空头清算强度 Top 3 价格。每次抓取还会以该快照现价分别汇总上下方清算金额：下方更大时显示绿色向上箭头，上方更大时显示红色向下箭头；金额相等时不显示方向箭头，方向仅随下一次 CoinGlass 抓取重新计算。该接口只提供两小时内的 CoinGlass 数据，过期或解析不可用时返回 503，不回退本地估算地图。原始抓取文件只保留最近 3 天，由每日维护窗口删除，`latest.json` 作为实时指针不参与清理，避免抓取目录无限增长。

每天北京时间 03:30（服务启动后立即先执行一次）自动完成一次数据库与磁盘清理：按各自保留期分批删除 K 线、清算事件、盘口快照、墙事件、市场指标、报警与信号；Deribit 期权 Gamma 会先删除 90 天外数据，近 24 小时按 5 分钟时间槽去重，再把 24 小时外的数据按整点/半点时间槽降为每小时两条。随后删除超过 3 天的 CoinGlass 原始抓取文件，最后把 WAL 检查点回主库并更新查询计划器统计。只有当删除释放的空闲页累计达到 512 MB 时才执行一次 `VACUUM` 真正回收磁盘空间，避免每天重写整库。清理按每批最多 2000 行分批提交，不会长时间阻塞实时采集；任何一步失败只记录 WARN 日志，不影响服务运行。

SQLite 在线备份示例：

```bash
sqlite3 /var/lib/liquidation-predictor/liquidation.db ".backup '/var/backups/liquidation-$(date +%F).db'"
```

Telegram 预警截图在 CoinGlass 快照可用时叠加多单清算 Top 3 蓝色水平线和空单清算 Top 3 橙色水平线（80% 透明度、1px），每条线的左右端以较小字体标注价格，图例使用“预警／激进／稳健／多单／空单”等中文说明，预警标记使用空心菱形；快照不可用时仍发送原始 K 线截图。

## 数据与风险说明

清算与行情模型读取 Binance USDⓈ-M 永续合约，期权观察页独立读取 Deribit 公开期权数据。Binance 全市场强平流对每个交易对每1000ms只推送最近一笔，因此覆盖率在 API 中按采样源标记。VAL/VAH 根据聚合成交名义额计算，POC只用于内部确定连续70%价值区域，不由API返回。

系统用于研究和可视化，不是投资建议。清算墙可能吸引价格，也可能成为加速穿越区；必须通过严格的时间序列样本外验证判断其是否提供增量信息。
