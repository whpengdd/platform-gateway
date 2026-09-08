FROM golang:1.23-alpine AS builder
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG GOPROXY=https://proxy.golang.org,direct
ENV HTTP_PROXY=$HTTP_PROXY HTTPS_PROXY=$HTTPS_PROXY GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /platform-gateway ./cmd/gateway

# alpine 而不是 distroless：63 等公网机经常拉不到 gcr.io。
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 65532 -g nonroot nonroot
COPY --from=builder /platform-gateway /platform-gateway
USER nonroot:nonroot
EXPOSE 8091
ENTRYPOINT ["/platform-gateway"]
