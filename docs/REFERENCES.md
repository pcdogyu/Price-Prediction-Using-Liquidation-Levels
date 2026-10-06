# References and provenance

No CoinGlass scraper, browser profile, cookies, database, backup file, or dashboard code was imported into this repository.

The exchange-collector behavior was reviewed against the authorized reference repository [`pcdogyu/MultipleExchangeLiquidationMap`, branch `golangv3`](https://github.com/pcdogyu/MultipleExchangeLiquidationMap/tree/golangv3), observed at commit `0aa982c8dd164b6df642ef014c85836ac5360e67`. The collectors in this repository were implemented afresh against the official exchange protocols.

Algorithmic ideas—not source code—were compared with:

- [`mindoozer/liquidation-map`](https://github.com/mindoozer/liquidation-map): OI-delta anchoring, leverage-specific decay and survivorship. No code was copied because no license was observed during review.
- [`aoki-h-jp/py-liquidation-map`](https://github.com/aoki-h-jp/py-liquidation-map): leverage-bucket visualization.
- [`flowdrivenml/market-data-stream-processor`](https://github.com/flowdrivenml/market-data-stream-processor): normalized multi-exchange event design.

Protocol sources:

- [Binance all-market liquidation stream](https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams/All-Market-Liquidation-Order-Streams)
- [Bybit all-liquidation stream](https://bybit-exchange.github.io/docs/v5/websocket/public/all-liquidation)
- [Bybit open-interest history](https://bybit-exchange.github.io/docs/v5/market/open-interest)
- [OKX API v5](https://www.okx.com/docs-v5/en/)
- [CoinGlass liquidation heatmap API description](https://github.com/coinglass-official/coinglass-api-docs/blob/main/rest/Futures/Liquidation/liquidation-heatmap.md)
