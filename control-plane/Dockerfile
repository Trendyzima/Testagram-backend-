FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY internal ./internal
COPY control-plane ./control-plane
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/control-plane ./control-plane

FROM alpine:3.22
RUN adduser -D -H -u 10001 app
USER app
COPY --from=build /out/control-plane /usr/local/bin/control-plane
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/control-plane"]
