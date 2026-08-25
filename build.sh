#!/usr/bin/env bash
set -euo pipefail

# u1s12api-go 构建脚本
# 用法: ./build.sh [target]
#   target: local (默认) | linux | all

TARGET="${1:-local}"
VERSION=$(date +%Y%m%d%H%M)

build_web() {
    echo ":: 构建前端..."
    cd web
    npm install --silent 2>/dev/null
    npm run build
    cd ..
}

build_go() {
    local os=$1 arch=$2 suffix=$3
    echo ":: 构建 $os/$arch..."
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build \
        -ldflags="-s -w -X main.version=$VERSION" \
        -o "u1s12api${suffix}" .
}

case "$TARGET" in
    local)
        build_web
        build_go "$(go env GOOS)" "$(go env GOARCH)" ""
        echo "✓ 构建完成: ./u1s12api"
        ;;
    linux)
        build_web
        build_go linux amd64 "-linux-amd64"
        echo "✓ 构建完成: ./u1s12api-linux-amd64"
        ;;
    all)
        build_web
        build_go darwin arm64 "-darwin-arm64"
        build_go linux amd64 "-linux-amd64"
        build_go linux arm64 "-linux-arm64"
        echo "✓ 全部构建完成"
        ;;
    *)
        echo "用法: $0 [local|linux|all]"
        exit 1
        ;;
esac
