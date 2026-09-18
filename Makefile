.PHONY: build test race lint run clean

build:
	go build -trimpath -ldflags="-s -w" -o bin/loadgun ./cmd/loadgun

test:
	go test ./...

race:
	go test -race -cover ./...

lint:
	gofmt -l . && go vet ./...

run: build
	./bin/loadgun -n 200 -c 20 http://localhost:8080/

clean:
	rm -rf bin
