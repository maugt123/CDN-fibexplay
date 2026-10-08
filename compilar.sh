#!/bin/sh
# Compila fibex-cdn. Necesita Go 1.26+ (o usar compilar.ps1, que lo hace en Docker).
#   VERSION=1.0.0 ./compilar.sh
set -eu
VERSION="${VERSION:-dev}"
cd "$(dirname "$0")"

echo "== pruebas"
go vet ./...
go test -count=1 ./...

rm -rf dist && mkdir -p dist
for destino in linux/amd64 linux/arm64 windows/amd64; do
	os="${destino%/*}"
	arq="${destino#*/}"
	salida="dist/fibex-cdn-${os}-${arq}"
	[ "$os" = windows ] && salida="${salida}.exe"
	echo "== $salida"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arq" go build -trimpath \
		-ldflags "-s -w -X fibex-cdn.Version=${VERSION}" \
		-o "$salida" ./cmd/fibex-cdn
done
cp fibex-cdn.env.ejemplo dist/
(cd dist && sha256sum fibex-cdn-* > SHA256SUMS)
echo "listo: dist/"
