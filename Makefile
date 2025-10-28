.PHONY: tidy run migrate

tidy:
	go mod tidy

# run the service locally with your local environment variables
run:
	go run ./cmd/server

# apply database migrations
migrate:
	psql "$(DATABASE_URL)" -f migrations/001_init.sql