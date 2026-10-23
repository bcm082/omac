install:
	go build -o $(HOME)/.local/bin/omac .

test:
	go test ./...
