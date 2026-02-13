# GoSpeak Build Container
# Multi-stage build: compile with all CGo deps, output lean binaries
# Optimized for layer caching — all apt/cmake steps run before source copy.

# ============================================================
# Stage 1: Builder base — install ALL system deps (cached layer)
# ============================================================
FROM golang:1.24-bookworm AS builder-base

# Install ALL system dependencies in one layer:
# - Linux audio/GL for native client
# - mingw-w64 for Windows cross-compilation
RUN apt-get update && apt-get install -y --no-install-recommends \
    # Audio
    portaudio19-dev \
    libopus-dev \
    pkg-config \
    # Fyne / OpenGL
    libgl1-mesa-dev \
    libx11-dev \
    libxcursor-dev \
    libxrandr-dev \
    libxinerama-dev \
    libxi-dev \
    libxxf86vm-dev \
    # Build tools
    gcc \
    libc6-dev \
    cmake \
    make \
    git \
    # Windows cross-compile
    gcc-mingw-w64-x86-64 \
    g++-mingw-w64-x86-64 \
    && rm -rf /var/lib/apt/lists/*

# ============================================================
# Stage 2: Cross-compile Windows deps (cached until cmake changes)
# ============================================================
FROM builder-base AS win-deps

# Create cmake toolchain file for mingw64
RUN printf '\
set(CMAKE_SYSTEM_NAME Windows)\n\
set(CMAKE_SYSTEM_PROCESSOR AMD64)\n\
set(CMAKE_C_COMPILER x86_64-w64-mingw32-gcc)\n\
set(CMAKE_CXX_COMPILER x86_64-w64-mingw32-g++)\n\
set(CMAKE_RC_COMPILER x86_64-w64-mingw32-windres)\n\
set(CMAKE_FIND_ROOT_PATH /usr/x86_64-w64-mingw32)\n\
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)\n\
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)\n\
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)\n\
' > /tmp/mingw-toolchain.cmake

# Build PortAudio + Opus for Windows (slow — but cached)
RUN mkdir -p /win-deps/include /win-deps/lib && \
    # PortAudio
    git clone --depth 1 https://github.com/PortAudio/portaudio.git /tmp/portaudio-src && \
    cd /tmp/portaudio-src && mkdir build && cd build && \
    cmake .. -DCMAKE_TOOLCHAIN_FILE=/tmp/mingw-toolchain.cmake \
        -DCMAKE_INSTALL_PREFIX=/win-deps \
        -DPA_USE_WASAPI=ON \
        -DPA_USE_WMME=ON \
        -DPA_USE_DS=OFF \
        -DPA_USE_ASIO=OFF \
        -DPA_USE_JACK=OFF \
        -DPA_BUILD_SHARED_LIBS=OFF \
        -DBUILD_SHARED_LIBS=OFF && \
    make -j$(nproc) && make install && \
    # Opus
    git clone --depth 1 --branch v1.5.2 https://github.com/xiph/opus.git /tmp/opus-src && \
    cd /tmp/opus-src && mkdir build && cd build && \
    cmake .. -DCMAKE_TOOLCHAIN_FILE=/tmp/mingw-toolchain.cmake \
        -DCMAKE_INSTALL_PREFIX=/win-deps \
        -DOPUS_STACK_PROTECTOR=OFF \
        -DOPUS_FORTIFY_SOURCE=OFF \
        -DBUILD_SHARED_LIBS=OFF && \
    make -j$(nproc) && make install && \
    # pkg-config files
    mkdir -p /win-deps/lib/pkgconfig && \
    printf 'prefix=/win-deps\nlibdir=${prefix}/lib\nincludedir=${prefix}/include\nName: portaudio-2.0\nVersion: 19.7\nDescription: PortAudio\nLibs: -L${libdir} -lportaudio -lwinmm -lole32 -lsetupapi\nCflags: -I${includedir}\n' > /win-deps/lib/pkgconfig/portaudio-2.0.pc && \
    printf 'prefix=/win-deps\nlibdir=${prefix}/lib\nincludedir=${prefix}/include\nName: opus\nVersion: 1.5.2\nDescription: Opus\nLibs: -L${libdir} -lopus\nCflags: -I${includedir}/opus\n' > /win-deps/lib/pkgconfig/opus.pc && \
    rm -rf /tmp/portaudio-src /tmp/opus-src

# ============================================================
# Stage 3: Go build — only this reruns on source changes
# ============================================================
FROM builder-base AS builder

WORKDIR /build

# Cache Go module download (only reruns when go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

# Copy Windows cross-compiled libs from cache stage
COPY --from=win-deps /win-deps /win-deps
COPY --from=win-deps /tmp/mingw-toolchain.cmake /tmp/mingw-toolchain.cmake

# Copy source code (this is the layer that changes most often)
COPY . .

# Compute version from git (tag > short-sha > dev)
RUN VERSION_PKG="github.com/NicolasHaas/gospeak/pkg/version" && \
    TAG=$(git describe --tags --exact-match 2>/dev/null || true) && \
    COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) && \
    DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    echo "-X ${VERSION_PKG}.tag=${TAG} -X ${VERSION_PKG}.commit=${COMMIT} -X ${VERSION_PKG}.date=${DATE}" > /tmp/version-ldflags

# Build server (Linux)
RUN CGO_ENABLED=1 go build -o /out/gospeak-server \
    -ldflags="-s -w $(cat /tmp/version-ldflags)" \
    ./cmd/server/

# Build server (Windows) — pure Go SQLite, no C deps needed
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -o /out/gospeak-server-win.exe \
    -ldflags="-s -w $(cat /tmp/version-ldflags)" \
    ./cmd/server/

# Build Linux client
RUN CGO_ENABLED=1 go build -o /out/gospeak-client-lin \
    -tags nolibopusfile \
    -ldflags="-s -w $(cat /tmp/version-ldflags)" \
    ./cmd/client/

# Build Windows client
RUN PKG_CONFIG_PATH=/win-deps/lib/pkgconfig \
    PKG_CONFIG_LIBDIR=/win-deps/lib/pkgconfig \
    CC=x86_64-w64-mingw32-gcc \
    CGO_ENABLED=1 \
    GOOS=windows \
    GOARCH=amd64 \
    go build -o /out/gospeak-client-win.exe \
    -tags nolibopusfile \
    -ldflags="-s -w -H windowsgui $(cat /tmp/version-ldflags)" \
    ./cmd/client/

# ============================================================
# Stage 3b: Windows-only export (for fast local dev)
# Usage: podman compose --profile build-win run builder-win
# ============================================================
FROM builder-base AS builder-win

COPY --from=builder /out/gospeak-server-win.exe /out/
COPY --from=builder /out/gospeak-client-win.exe /out/

# ============================================================
# Stage 3c: Linux-only export (for fast local dev)
# Usage: podman compose --profile build-lin run builder-lin
# ============================================================
FROM builder-base AS builder-lin

COPY --from=builder /out/gospeak-server /out/
COPY --from=builder /out/gospeak-client-lin /out/

# ============================================================
# Stage 4: Server runtime — minimal Debian with glibc for CGO SQLite
# ============================================================
FROM debian:bookworm-slim AS server

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/gospeak-server /gospeak-server

EXPOSE 9600/tcp 9601/udp 9602/tcp

VOLUME ["/data"]

ENTRYPOINT ["/gospeak-server", "-data", "/data", "-db", "/data/gospeak.db"]

# ============================================================
# Stage 5: Trivy security scan (optional — use with --target trivy-scan)
# Scans the server image for CVEs.
# Usage: docker build --target trivy-scan -f Containerfile .
# ============================================================
FROM aquasec/trivy:latest AS trivy-scan

COPY --from=server / /scan-root

RUN trivy rootfs --severity HIGH,CRITICAL --no-progress --exit-code 1 /scan-root
