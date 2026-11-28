ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.24

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/metronome ./cmd/metronome

FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS gh
ARG GH_VERSION=2.106.0
ARG TARGETARCH
RUN apk add --no-cache curl \
 && curl -fsSLO https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz \
 && curl -fsSLO https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_checksums.txt \
 && grep " gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz$" gh_${GH_VERSION}_checksums.txt > gh.sha256 \
 && sha256sum -c gh.sha256 \
 && tar -xzf gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz \
 && mkdir -p /out \
 && install -m 0755 gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh /out/gh

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tini \
 && addgroup -S -g 65532 metronome \
 && adduser -S -D -H -u 65532 -G metronome -h /home/metronome metronome \
 && install -d -o 65532 -g 65532 /data /home/metronome
COPY --from=build /out/metronome /usr/local/bin/metronome
COPY --from=gh /out/gh /usr/local/bin/gh
LABEL org.opencontainers.image.source=https://github.com/jystringfellow/metronome \
      org.opencontainers.image.licenses=Apache-2.0
ENV HOME=/home/metronome GH_NO_UPDATE_NOTIFIER=1 GH_PROMPT_DISABLED=1
WORKDIR /data
USER 65532:65532
EXPOSE 7340
ENTRYPOINT ["/sbin/tini", "--", "metronome"]
CMD ["serve", "--addr", "0.0.0.0:7340", "--db", "/data/metronome.db"]
