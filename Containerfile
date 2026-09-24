# GoSpeak Build Container
# Multi-stage build: compile with all CGo deps, output lean binaries
# Optimized for layer caching — all apt/cmake steps run before source copy.

# ============================================================
# Stage 1: Builder base — install ALL system deps (cached layer)
# ============================================================
FROM golang:1.24.4-bookworm@sha256:10f549dc8489597aa7ed2b62008199bb96717f52a8e8434ea035d5b44368f8a6 AS builder-base

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

ARG PORTAUDIO_COMMIT=147dd722548358763a8b649b3e4b41dfffbcfbb6
ARG OPUS_COMMIT=ddbe48383984d56acd9e1ab6a090c54ca6b735a6

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
    git init /tmp/portaudio-src && \
    git -C /tmp/portaudio-src remote add origin https://github.com/PortAudio/portaudio.git && \
    git -C /tmp/portaudio-src fetch --depth 1 origin "$PORTAUDIO_COMMIT" && \
    git -C /tmp/portaudio-src checkout --detach FETCH_HEAD && \
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
    git init /tmp/opus-src && \
    git -C /tmp/opus-src remote add origin https://github.com/xiph/opus.git && \
    git -C /tmp/opus-src fetch --depth 1 origin "$OPUS_COMMIT" && \
    git -C /tmp/opus-src checkout --detach FETCH_HEAD && \
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

ARG VERSION_TAG
ARG VERSION_COMMIT=unknown
ARG VERSION_DATE=unknown

WORKDIR /build

# Cache Go module download (only reruns when go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

# Copy Windows cross-compiled libs from cache stage
COPY --from=win-deps /win-deps /win-deps
COPY --from=win-deps /tmp/mingw-toolchain.cmake /tmp/mingw-toolchain.cmake

# Copy source code (this is the layer that changes most often)
COPY . .

# Release workflows pass immutable source metadata. Local builds keep explicit
# unknown defaults instead of depending on whether the build context contains .git.
RUN VERSION_PKG="github.com/NicolasHaas/gospeak/pkg/version" && \
    echo "-X ${VERSION_PKG}.tag=${VERSION_TAG} -X ${VERSION_PKG}.commit=${VERSION_COMMIT} -X ${VERSION_PKG}.date=${VERSION_DATE}" > /tmp/version-ldflags

# Build the pure-Go server as a static binary.
RUN CGO_ENABLED=0 go build -o /out/gospeak-server \
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
# Stage 4: Server runtime — the server has no C or OS package dependencies
# ============================================================
FROM scratch AS server

COPY --from=builder /out/gospeak-server /gospeak-server

EXPOSE 9600/tcp 9601/udp 9603/tcp

VOLUME ["/data"]

ENTRYPOINT ["/gospeak-server", "-data", "/data", "-db", "/data/gospeak.db"]

# ============================================================
# Stage 5: Trivy security scan (optional — use with --target trivy-scan)
# Scans the server image for CVEs.
# Usage: docker build --target trivy-scan -f Containerfile .
# ============================================================
FROM aquasec/trivy:0.67.2@sha256:e2b22eac59c02003d8749f5b8d9bd073b62e30fefaef5b7c8371204e0a4b0c08 AS trivy-scan

COPY --from=server / /scan-root

RUN trivy rootfs --severity HIGH,CRITICAL --no-progress --exit-code 1 /scan-root
