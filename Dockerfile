# マルチステージビルド
FROM golang:alpine AS builder

WORKDIR /app

ENV GOTOOLCHAIN=auto

# CA証明書等の取得
RUN apk --no-cache add ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO無効で静的バイナリをビルド（ARM64 / AMD64 両対応）
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o resend-server ./cmd/server

# 実行用軽量イメージ
FROM alpine:3.21

WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata
ENV TZ=Asia/Tokyo

# ビルド成果物および静的ファイル・テンプレートの配置
COPY --from=builder /app/resend-server /app/resend-server
COPY --from=builder /app/web /app/web

# データ永続化ディレクトリ用マウントポイント
RUN mkdir -p /app/data

EXPOSE 8080

CMD ["/app/resend-server"]
