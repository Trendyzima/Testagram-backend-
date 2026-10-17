FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/testagram-api ./cmd/api

FROM alpine:3.21
RUN addgroup -S app && adduser -S -G app app && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/testagram-api /app/testagram-api
USER app
ENV LISTEN_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/app/testagram-api"]
