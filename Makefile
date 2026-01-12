.PHONY: test bench lint clean

test:
	go test -v -race -cover ./...

bench:
	go test -bench=BenchmarkRingBuffer_Write -benchmem -count=3 ./...

lint:
	golangci-lint run ./...

clean:
	go clean -testcache
