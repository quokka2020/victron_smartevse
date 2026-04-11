#!/bin/bash -ex

go test ./...
go vet ./...

export GOARCH=arm
export GOARM=7
export GOOS=linux


[ ! -d build ] && mkdir build
[ -f build/victron_smartevse ] && rm build/victron_smartevse
LDFLAGS="-X victron_smartevse/global.Version=$(date "+%Y-%m-%dT%H:%M:%S") -X victron_smartevse/global.BuildTime=$(date "+%Y-%m-%dT%H:%M:%S")"
go build -ldflags "$LDFLAGS" -gcflags="all=-N -l" -o build/victron_smartevse app/main.go

