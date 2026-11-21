# Linux development image. Every make target that runs Go runs in here, as
# the calling user, with caches on the lan-tracker-cache volume (/cache).
FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea

# golangci-lint comes from its release tarball, checked against the SHA-256
# sums published with that release (bump the version and both sums together).
ARG GOLANGCI_LINT_VERSION=2.14.0
ARG GOLANGCI_LINT_SHA256_AMD64=ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab
ARG GOLANGCI_LINT_SHA256_ARM64=ee7ec5f3453d15ddf106fae5a4d6c71737712348a979d1fe9cd52ec7ea299bae
RUN apt-get update \
    && apt-get install -y --no-install-recommends file \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && case "$arch" in \
         amd64) sum="${GOLANGCI_LINT_SHA256_AMD64}" ;; \
         arm64) sum="${GOLANGCI_LINT_SHA256_ARM64}" ;; \
         *) echo "no golangci-lint checksum for $arch" >&2; exit 1 ;; \
       esac \
    && name="golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}" \
    && curl -sSfL -o /tmp/gl.tar.gz "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/${name}.tar.gz" \
    && echo "${sum}  /tmp/gl.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/gl.tar.gz -C /tmp "${name}/golangci-lint" \
    && install -m 0755 "/tmp/${name}/golangci-lint" /usr/local/bin/golangci-lint \
    && rm -rf /tmp/gl.tar.gz "/tmp/${name}"

# Version metadata is stamped with -ldflags, so VCS stamping is off (the
# mounted checkout may be owned by another uid).
ENV HOME=/tmp \
    GOCACHE=/cache/go-build \
    GOMODCACHE=/cache/mod \
    GOLANGCI_LINT_CACHE=/cache/golangci-lint \
    GOFLAGS=-buildvcs=false \
    CGO_ENABLED=0
WORKDIR /src
