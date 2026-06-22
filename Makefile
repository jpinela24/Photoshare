PHOTO_DIR ?= /photos
PORT      ?= 8080

.PHONY: build run docker build-windows clean

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

# Build the frontend + a native Windows .exe (tray + WebView2 window, no console).
# Run on any platform — cross-compiles via GOOS=windows; CGO_ENABLED=0 keeps it
# free of a C toolchain dependency.
build-windows:
	cd client && npm install && npm run build
	go mod tidy
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-H windowsgui -s -w" -o photoshare.exe .

clean:
	rm -rf photoshare photoshare.exe client/dist client/node_modules
