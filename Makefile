# Load environment variables from .env file
ifneq (,$(wildcard .env))
    include .env
    export
endif

ifneq ($(filter migrate% migration,$(MAKECMDGOALS)),)
    ifeq ($(DB_ADDR),)
        $(error DB_ADDR environment variable is not set. Please add DB_ADDR to your .env file)
    endif
endif

MIGRATIONS_PATH = ./internal/migrations

export PATH := $(PATH):/usr/local/go/bin:$(HOME)/go/bin

SWAG := go run github.com/swaggo/swag/cmd/swag@v1.16.2

.PHONY: swagger
swagger:
	@$(SWAG) init -g cmd/app/main.go -o docs --parseDependency --parseInternal --outputTypes go

.PHONY: migrate-create
migration:
	@migrate create -seq -ext sql -dir $(MIGRATIONS_PATH) $(filter-out $@, $(MAKECMDGOALS))

.PHONY: migrate-up
migrate-up:
	@migrate -path=$(MIGRATIONS_PATH) -database=$(DB_ADDR) up

.PHONY: migrate-down
migrate-down:
	@if [ "$(STAGE)" != "dev" ]; then \
		echo "Error: migrate-down is allowed only in development (STAGE=dev)."; \
		exit 1; \
	fi
	@migrate -path=$(MIGRATIONS_PATH) -database=$(DB_ADDR) down $(filter-out $@, $(MAKECMDGOALS))

.PHONY: migrate-force
migrate-force:
	@if [ "$(STAGE)" != "dev" ]; then \
		echo "Error: migrate-force is allowed only in development (STAGE=dev)."; \
		exit 1; \
	fi
	@migrate -path=$(MIGRATIONS_PATH) -database=$(DB_ADDR) force $(filter-out $@, $(MAKECMDGOALS))