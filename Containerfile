# GoSpeak Build Container
# Independent server, Linux and Windows builds; apt/cmake precede source copies.
FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS go-base

# Linux client dependencies (also used by development and release inspection).
FROM go-base AS builder-base
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
    && rm -rf /var/lib/apt/lists/*

# Aggregate release inspection needs Linux runtime libraries and Windows objdump.
FROM builder-base AS release-base
RUN apt-get update && apt-get install -y --no-install-recommends \
    binutils-mingw-w64-x86-64 \
    && rm -rf /var/lib/apt/lists/*

# Windows toolchain and native libraries, independent of Linux client packages.
FROM go-base AS win-deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    pkg-config \
    cmake \
    make \
    git \
    gcc-mingw-w64-x86-64 \
    g++-mingw-w64-x86-64 \
    && rm -rf /var/lib/apt/lists/*

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
    cmake -S /tmp/portaudio-src -B /tmp/portaudio-build \
        -DCMAKE_TOOLCHAIN_FILE=/tmp/mingw-toolchain.cmake \
        -DCMAKE_INSTALL_PREFIX=/win-deps \
        -DPA_USE_WASAPI=ON \
        -DPA_USE_WMME=ON \
        -DPA_USE_DS=OFF \
        -DPA_USE_ASIO=OFF \
        -DPA_USE_JACK=OFF \
        -DPA_BUILD_SHARED=OFF \
        -DBUILD_SHARED_LIBS=OFF && \
    cmake --build /tmp/portaudio-build -j$(nproc) && \
    cmake --install /tmp/portaudio-build && \
    # Opus
    git init /tmp/opus-src && \
    git -C /tmp/opus-src remote add origin https://github.com/xiph/opus.git && \
    git -C /tmp/opus-src fetch --depth 1 origin "$OPUS_COMMIT" && \
    git -C /tmp/opus-src checkout --detach FETCH_HEAD && \
    cmake -S /tmp/opus-src -B /tmp/opus-build \
        -DCMAKE_TOOLCHAIN_FILE=/tmp/mingw-toolchain.cmake \
        -DCMAKE_INSTALL_PREFIX=/win-deps \
        -DOPUS_STACK_PROTECTOR=OFF \
        -DOPUS_FORTIFY_SOURCE=OFF \
        -DBUILD_SHARED_LIBS=OFF && \
    cmake --build /tmp/opus-build -j$(nproc) && \
    cmake --install /tmp/opus-build && \
    # pkg-config files
    mkdir -p /win-deps/lib/pkgconfig && \
    printf 'prefix=/win-deps\nlibdir=${prefix}/lib\nincludedir=${prefix}/include\nName: portaudio-2.0\nVersion: 19.7\nDescription: PortAudio\nLibs: -L${libdir} -lportaudio -lwinmm -lole32 -lsetupapi\nCflags: -I${includedir}\n' > /win-deps/lib/pkgconfig/portaudio-2.0.pc && \
    printf 'prefix=/win-deps\nlibdir=${prefix}/lib\nincludedir=${prefix}/include\nName: opus\nVersion: 1.5.2\nDescription: Opus\nLibs: -L${libdir} -lopus\nCflags: -I${includedir}/opus\n' > /win-deps/lib/pkgconfig/opus.pc && \
    rm -rf /tmp/portaudio-src /tmp/portaudio-build /tmp/opus-src /tmp/opus-build

# Shared Go inputs; no native packages or release compilation.
FROM go-base AS go-source
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Release metadata cannot invalidate apt, CMake or module-download layers.
ARG VERSION_TAG
ARG VERSION_COMMIT=unknown
ARG VERSION_DATE=unknown
RUN VERSION_PKG="github.com/NicolasHaas/gospeak/pkg/version" && \
    echo "-X ${VERSION_PKG}.tag=${VERSION_TAG} -X ${VERSION_PKG}.commit=${VERSION_COMMIT} -X ${VERSION_PKG}.date=${VERSION_DATE}" > /tmp/version-ldflags

# Dedicated pure-Go server build, using the build platform's architecture.
FROM go-source AS server-builder
RUN mkdir -m 0700 -p /out/data
RUN CGO_ENABLED=0 go build -o /out/gospeak-server \
    -ldflags="-s -w $(cat /tmp/version-ldflags)" \
    ./cmd/server/

# Compose lint/test need native Linux dependencies and source, not release binaries.
FROM builder-base AS development
WORKDIR /build
COPY --from=go-source /go/pkg/mod /go/pkg/mod
COPY --from=go-source /build /build

FROM development AS builder-lin
COPY --from=go-source /tmp/version-ldflags /tmp/version-ldflags
COPY --from=server-builder /out/gospeak-server /out/
RUN CGO_ENABLED=1 go build -o /out/gospeak-client-lin \
    -tags nolibopusfile \
    -ldflags="-s -w $(cat /tmp/version-ldflags)" \
    ./cmd/client/

FROM win-deps AS builder-win
WORKDIR /build
COPY --from=go-source /go/pkg/mod /go/pkg/mod
COPY --from=go-source /build /build
COPY --from=go-source /tmp/version-ldflags /tmp/version-ldflags

# Windows server remains pure Go; only the client uses MinGW and native libraries.
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -o /out/gospeak-server-win.exe \
    -ldflags="-s -w $(cat /tmp/version-ldflags)" \
    ./cmd/server/

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

# Aggregate export retains all four binaries and release dependency-inspection tools.
FROM release-base AS builder
COPY --from=builder-lin /out/gospeak-server /out/
COPY --from=builder-lin /out/gospeak-client-lin /out/
COPY --from=builder-win /out/gospeak-server-win.exe /out/
COPY --from=builder-win /out/gospeak-client-win.exe /out/

# ============================================================
# Stage 4: Server runtime — the server has no C or OS package dependencies
# ============================================================
FROM scratch AS server

LABEL org.opencontainers.image.source="https://github.com/NicolasHaas/gospeak" \
      org.opencontainers.image.licenses="AGPL-3.0" \
      org.opencontainers.image.description="GoSpeak server: privacy-focused voice communication SFU"

COPY --from=server-builder /out/gospeak-server /gospeak-server
COPY --from=server-builder --chown=10001:10001 --chmod=0700 /out/data /data
USER 10001:10001

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
