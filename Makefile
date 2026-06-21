PHOTO_DIR ?= /photos
PORT      ?= 8080

.PHONY: build run docker clean

# Build the frontend + a native Linux binary
build:
	cd client && npm install && npm run build
	go mod tidy
	go build -o photoshare .

# Run locally for testing
run: build
	./photoshare -dir "$(PHOTO_DIR)" -http-only -port $(PORT)

# Build the Docker image
docker:
	docker build -t photoshare:latest .

clean:
	rm -rf photoshare client/dist client/node_modules
