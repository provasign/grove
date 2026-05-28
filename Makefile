.PHONY: build test lint install run bootstrap clean

build:
	go build -o bin/grove ./cmd/grove

test:
	go test ./...

lint:
	go vet ./...

install:
	go install ./cmd/grove

bootstrap:
	bash ../prism/scripts/bootstrap-prism-grove.sh

run:
	go run ./cmd/grove

clean:
	rm -rf bin
