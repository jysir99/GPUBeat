BINARY = gpuview
DIST   = dist
BUILD  = $(DIST)/$(BINARY)

.PHONY: build clean run

build:
	@mkdir -p $(DIST)
	go build -ldflags="-s -w" -o $(BUILD)

clean:
	rm -rf $(DIST)

run: build
	@cd $(DIST) && ./$(BINARY)
