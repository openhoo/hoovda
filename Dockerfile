FROM docker.io/library/ubuntu:24.04@sha256:1e0a86e57d247923571b75e0aaf48a1449cf8c543d51fb3e07a4a7d7bfa79316 AS runtime-deps

ARG DEBIAN_FRONTEND=noninteractive
ARG ATSPI_COMMIT=ea37f283d06e556dcffad036623107dc20f419c5
ARG ATSPI_SHA256=8002fcc676d7182e4a7667a467dc2d8e4b63f805f5ea9f673e4488f5b9f728c6
ARG ESPEAK_COMMIT=4870adfa25b1a32b4361592f1be8a40337c58d6c
ARG ESPEAK_SHA256=cd83f84c4e495f281ac14e919aecf2834306ec1ea1b498de8ef6000f3a0f90de
ARG LIBLOUIS_COMMIT=07c61e58cfb8814f6842c7212063f829288638c1
ARG LIBLOUIS_SHA256=865c9dd08fbe57c5de5290b7bad443c438f62cc781cecb8fe488e867ec382f18

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      autoconf \
      automake \
      autopoint \
      build-essential \
      ca-certificates \
      cmake \
      curl \
      dbus-daemon \
      gettext \
      libdbus-1-dev \
      libglib2.0-dev \
      libtool \
      libx11-dev \
      libxi-dev \
      libxml2-dev \
      libxres-dev \
      libxtst-dev \
      meson \
      ninja-build \
      pkg-config \
      python3 \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /build
RUN curl -fsSL --retry 3 -o atspi.tar.gz \
      "https://gitlab.gnome.org/GNOME/at-spi2-core/-/archive/${ATSPI_COMMIT}/at-spi2-core-${ATSPI_COMMIT}.tar.gz" \
 && echo "${ATSPI_SHA256}  atspi.tar.gz" | sha256sum --check --strict \
 && mkdir atspi \
 && tar -xzf atspi.tar.gz -C atspi --strip-components=1 \
 && meson setup atspi-build atspi \
      --buildtype=release \
      --prefix=/opt/hoovda-runtime \
      --libdir=lib/x86_64-linux-gnu \
      -Ddbus_glib=disabled \
      -Ddbus_daemon=/usr/bin/dbus-daemon \
      -Ddefault_bus=dbus-daemon \
      -Ddocs=false \
      -Dgtk2_atk_adaptor=false \
      -Dintrospection=disabled \
      -Duse_systemd=false \
      -Dx11=enabled \
 && meson compile -C atspi-build \
 && meson install -C atspi-build

RUN curl -fsSL --retry 3 -o espeak.tar.gz \
      "https://github.com/espeak-ng/espeak-ng/archive/${ESPEAK_COMMIT}.tar.gz" \
 && echo "${ESPEAK_SHA256}  espeak.tar.gz" | sha256sum --check --strict \
 && mkdir espeak \
 && tar -xzf espeak.tar.gz -C espeak --strip-components=1 \
 && cd espeak \
 && ./autogen.sh \
 && ./configure \
      --prefix=/opt/hoovda-runtime \
      --disable-rpath \
      --disable-shared \
      --enable-static \
      --without-async \
      --without-mbrola \
      --without-pcaudiolib \
      --without-sonic \
      --without-speechplayer \
 && make -j"$(nproc)" src/espeak-ng src/speak-ng \
 && make \
 && make install

RUN curl -fsSL --retry 3 -o liblouis.tar.gz \
      "https://github.com/liblouis/liblouis/archive/${LIBLOUIS_COMMIT}.tar.gz" \
 && echo "${LIBLOUIS_SHA256}  liblouis.tar.gz" | sha256sum --check --strict \
 && mkdir liblouis \
 && tar -xzf liblouis.tar.gz -C liblouis --strip-components=1 \
 && cd liblouis \
 && ./autogen.sh \
 && ./configure \
      --prefix=/opt/hoovda-runtime \
      --disable-shared \
      --enable-static \
      --without-yaml \
 && make -j"$(nproc)" \
 && make install

RUN test "$(PKG_CONFIG_PATH=/opt/hoovda-runtime/lib/x86_64-linux-gnu/pkgconfig pkg-config --modversion atspi-2)" = "2.60.1" \
 && test "$(/opt/hoovda-runtime/bin/espeak-ng --version 2>&1 | awk 'NR == 1 { print $4 }')" = "1.52.0" \
 && test "$(/opt/hoovda-runtime/bin/lou_translate --version 2>&1 | awk 'NR == 1 { print $3 }')" = "3.38.0" \
 && install -d /opt/hoovda-sources /opt/hoovda-third-party-licenses/at-spi2-core \
      /opt/hoovda-third-party-licenses/espeak-ng /opt/hoovda-third-party-licenses/liblouis \
 && cp atspi.tar.gz espeak.tar.gz liblouis.tar.gz /opt/hoovda-sources/ \
 && cp atspi/COPYING /opt/hoovda-third-party-licenses/at-spi2-core/ \
 && cp espeak/COPYING* /opt/hoovda-third-party-licenses/espeak-ng/ \
 && cp liblouis/COPYING liblouis/COPYING.LESSER /opt/hoovda-third-party-licenses/liblouis/

FROM docker.io/library/golang:1.27-bookworm@sha256:ba5ef6614ca131b80a635fc6a7b715d9ee8a7f333debdbb81afb68259c7d48d4 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY tools ./tools

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go test ./... \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
      -ldflags="-s -w -X github.com/openhoo/hoovda/internal/buildinfo.Version=${VERSION} -X github.com/openhoo/hoovda/internal/buildinfo.Commit=${COMMIT} -X github.com/openhoo/hoovda/internal/buildinfo.BuildDate=${BUILD_DATE}" \
      -o /out/hoovda ./cmd/hoovda

FROM docker.io/library/ubuntu:24.04@sha256:1e0a86e57d247923571b75e0aaf48a1449cf8c543d51fb3e07a4a7d7bfa79316

ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      at-spi2-core \
      ca-certificates \
      dbus-x11 \
      espeak-ng \
      ffmpeg \
      liblouis-bin \
      libxres1 \
      xdotool \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 1001 hoovda \
 && useradd --uid 1001 --gid 1001 --create-home --shell /usr/sbin/nologin hoovda \
 && install -d -o hoovda -g hoovda -m 0700 /tmp/hoovda

COPY --from=builder /out/hoovda /usr/local/bin/hoovda
COPY --from=runtime-deps /opt/hoovda-runtime /opt/hoovda-runtime
COPY --from=runtime-deps /opt/hoovda-sources /usr/src/hoovda-third-party
COPY --from=runtime-deps /opt/hoovda-third-party-licenses /usr/share/doc/hoovda/third-party
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.md /usr/share/doc/hoovda/

ENV PATH=/opt/hoovda-runtime/bin:$PATH \
    LD_LIBRARY_PATH=/opt/hoovda-runtime/lib/x86_64-linux-gnu:/opt/hoovda-runtime/lib \
    XDG_DATA_DIRS=/opt/hoovda-runtime/share:/usr/local/share:/usr/share

LABEL org.opencontainers.image.title="HooVDA" \
      org.opencontainers.image.description="Clean-room Linux-native browser screenreader test engine" \
      org.opencontainers.image.source="https://github.com/openhoo/hoovda" \
      org.opencontainers.image.licenses="Apache-2.0 AND GPL-3.0-or-later AND LGPL-2.1-or-later" \
      dev.openhoo.hoovda.corresponding-source="/usr/src/hoovda-third-party" \
      dev.openhoo.hoovda.third-party-notices="/usr/share/doc/hoovda/THIRD_PARTY_NOTICES.md" \
      dev.openhoo.hoovda.platform="linux/amd64" \
      dev.openhoo.hoovda.profile="nvda-web-2026.1.1" \
      dev.openhoo.hoovda.release-gate="oracle-conformance-required"

USER hoovda
WORKDIR /tmp/hoovda
ENTRYPOINT ["/usr/local/bin/hoovda"]
CMD ["version"]
