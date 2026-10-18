FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/payrelay ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
WORKDIR /app
COPY --from=build /out/payrelay /app/payrelay
EXPOSE 8080
ENV PAYRELAY_ADDR=:8080 PAYRELAY_DB_PATH=/data/payrelay.db
VOLUME ["/data"]
ENTRYPOINT ["/app/payrelay"]
