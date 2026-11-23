#!/usr/bin/env bash
# Regenerate every generated file the fork touches, with the tool versions the upstream
# generate.sh scripts pin. Run it after each upstream merge instead of merging generated files by hand.
# Tools install into a temporary directory, so the global toolchain stays untouched.
set -euo pipefail

root=$(cd "$(dirname "$(realpath "$0")")/../.." && pwd)
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
export GOBIN=$bin PATH=$bin:$PATH

go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.26.0
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1

# Only the proto files the fork edits are regenerated, so upstream-owned generated files stay byte for byte.
# management and signal use protoc-gen-go v1.26.0 and grpc plugin v1.1
(cd "$root/shared/management/proto" && protoc -I ./ ./management.proto --go_out=../ --go-grpc_out=../)
(cd "$root/shared/signal/proto" && protoc -I ./ ./signalexchange.proto --go_out=../ --go-grpc_out=../)

# the daemon API uses newer plugins plus the grpc gateway
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.26.3
(cd "$root/client/proto" && protoc -I ./ ./daemon.proto \
  --go_out=../ --go-grpc_out=../ \
  --grpc-gateway_out=../ --grpc-gateway_opt=generate_unbound_methods=true \
  --experimental_allow_proto3_optional)

# REST API types
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.1
(cd "$root/shared/management/http/api" && oapi-codegen --config cfg.yaml openapi.yml)

echo "regenerated; review with: git diff --stat"
