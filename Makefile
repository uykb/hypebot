.PHONY: build run clean deploy

# Default build
build:
	docker build -t hypefollow-go .

# Run with env file
run:
	docker run --env-file .env hypefollow-go

# Run with docker-compose
deploy:
	docker-compose up -d --build

# Stop
stop:
	docker-compose down

# View logs
logs:
	docker-compose logs -f

# Clean build artifacts
clean:
	docker rmi hypefollow-go:latest || true
	docker system prune -f

# Development - run locally (requires Go 1.21+)
dev:
	go run ./cmd/main.go
