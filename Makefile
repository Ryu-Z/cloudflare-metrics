.PHONY: fmt lint build run up down logs restart ps test-alert

APP=cloudflare-analytics-metrics-exporter

fmt:
	gofmt -w *.go

lint:
	go test ./...

build:
	go build -o $(APP) .

run:
	go run . -config config.yaml

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

restart:
	docker compose down
	docker compose up -d --build

ps:
	docker compose ps

test-alert:
	CF_API_TOKEN=invalid-token-for-lark-verification go run . -config config.verify-failure.yaml
