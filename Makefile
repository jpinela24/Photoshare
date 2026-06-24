PHOTO_DIR ?= /photos
PORT      ?= 8080

.PHONY: build run docker build-windows build-windows-arm64 clean

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
	# Embed the exe icon + version metadata (Explorer, taskbar, Add/Remove
	# Programs). The .syso is named *_windows_amd64 so only Windows builds
	# link it; regenerated here so it always matches the current icon/version.
	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -icon=windows/icon.ico -o=resource_windows_amd64.syso windows/versioninfo.json
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-H windowsgui -s -w" -o photoshare.exe .

# Native Windows-on-ARM (Windows 11 ARM) build. goversioninfo's "-arm -64"
# emits an ARM64 resource; the *_windows_arm64.syso suffix scopes it to ARM
# builds only.
build-windows-arm64:
	cd client && npm install && npm run build
	go mod tidy
	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -arm -64 -icon=windows/icon.ico -o=resource_windows_arm64.syso windows/versioninfo.json
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-H windowsgui -s -w" -o photoshare.exe .

clean:
	rm -rf photoshare photoshare.exe client/dist client/node_modules
