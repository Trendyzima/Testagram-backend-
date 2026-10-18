FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/testagram-admin ./admin-console

FROM alpine:3.24
RUN addgroup -S app && adduser -S -G app app && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/testagram-admin /app/testagram-admin
USER app
ENV ADMIN_ADDR=:8090
EXPOSE 8090
ENTRYPOINT ["/app/testagram-admin"]
