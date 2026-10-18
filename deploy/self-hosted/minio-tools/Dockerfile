# MinIO withdrew its public server images from Docker Hub and Quay.
# Build a small shell-capable init image around the published mc binary mirror.
FROM ghcr.io/golithus/mc:RELEASE.2025-08-13T08-35-41Z AS mc

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=mc /usr/bin/mc /usr/local/bin/mc
ENTRYPOINT ["/bin/sh", "-ec"]
