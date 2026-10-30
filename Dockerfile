# The mishmar binary for Linux, built with cb-mpc (C++, cgo) from the
# mishmar-io fork of cb-mpc-go at the ref in cbmpc.ref. The image runs the
# engine's signer service by default; the same binary is the agent's signer
# and the CLI.
#
#   docker build -t mishmar .
#   docker run -v mishmar-keys:/var/lib/mishmar -p 127.0.0.1:8091:8091 \
#     -e MISHMAR_SIGNER_KEY=<64 hex: openssl rand -hex 32, from the secret manager> mishmar \
#     signer serve --listen 0.0.0.0:8091 --store /var/lib/mishmar/signer-keys.json --engine-key <base64>
#
# Plain HTTP is for a private network only: in front of the engine over a
# network, give it --tls-cert, --tls-key and --client-ca.

FROM golang:1.26-bookworm AS build
ARG CBMPC_REF
ENV GOTOOLCHAIN=auto CXXFLAGS=-DDY_NO_LOG CBMPC_OPENSSL_ROOT=/usr/local/opt/openssl@3.6.1
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential ca-certificates cmake curl git perl pkg-config rsync \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY cbmpc.ref ./
RUN ref="${CBMPC_REF:-$(tr -d '[:space:]' < cbmpc.ref)}" \
    && git clone --filter=blob:none https://github.com/mishmar-io/cb-mpc-go.git /cb-mpc-go \
    && git -C /cb-mpc-go checkout --quiet "$ref" \
    && git -C /cb-mpc-go submodule update --init --recursive \
    && bash /cb-mpc-go/scripts/openssl/build-static-openssl-linux.sh \
    && bash /cb-mpc-go/scripts/build_cpp.sh Release
COPY go.mod go.sum ./
RUN sed -i 's#=> ../cb-mpc-go#=> /cb-mpc-go#' go.mod && go mod download
COPY . .
RUN sed -i 's#=> ../cb-mpc-go#=> /cb-mpc-go#' go.mod \
    && CBMPC_DIR=/cb-mpc-go bash /cb-mpc-go/scripts/go_with_cpp.sh go build -trimpath -ldflags "-s -w" -o /out/mishmar ./cmd/mishmar

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home /var/lib/mishmar --create-home mishmar
COPY --from=build /out/mishmar /usr/local/bin/mishmar
USER mishmar
VOLUME /var/lib/mishmar
EXPOSE 8091
ENTRYPOINT ["mishmar"]
CMD ["signer", "serve", "--listen", "0.0.0.0:8091", "--store", "/var/lib/mishmar/signer-keys.json"]
