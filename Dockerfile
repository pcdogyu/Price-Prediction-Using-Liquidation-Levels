FROM golang:1.21-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/liquidation-predictor ./cmd/server
RUN mkdir -p /out/data /out/models

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --chown=65532:65532 --from=build /out/liquidation-predictor /app/liquidation-predictor
COPY --chown=65532:65532 --from=build /out/data /app/data
COPY --chown=65532:65532 --from=build /out/models /app/models
VOLUME ["/app/data", "/app/models"]
EXPOSE 9090
ENTRYPOINT ["/app/liquidation-predictor"]
