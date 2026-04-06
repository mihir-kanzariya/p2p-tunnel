.PHONY: build clean install

build:
	go build -o bin/p2p-tunnel ./cmd/client

clean:
	rm -rf bin/

install: build
	cp bin/p2p-tunnel /usr/local/bin/p2p-tunnel
