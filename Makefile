VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBUILD := go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build run test vet android dist clean

# Сборка под текущую систему (в том числе прямо в Termux)
build:
	$(GOBUILD) -o assistant .

run: build
	./assistant

test:
	go test ./...

vet:
	go vet ./...

# Готовый бинарник для телефона (Android 64-bit / Termux), собирается на ПК
android:
	GOOS=android GOARCH=arm64 CGO_ENABLED=0 $(GOBUILD) -o dist/assistant-android-arm64 .

# Набор бинарников под популярные платформы
dist: android
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -o dist/assistant-linux-amd64 .
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 $(GOBUILD) -o dist/assistant-linux-arm64 .
	GOOS=linux   GOARCH=arm GOARM=7 CGO_ENABLED=0 $(GOBUILD) -o dist/assistant-linux-armv7 .
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -o dist/assistant-windows-amd64.exe .
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 $(GOBUILD) -o dist/assistant-macos-arm64 .

clean:
	rm -rf assistant assistant.exe dist
