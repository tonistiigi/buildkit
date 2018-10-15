ARG RUNC_VERSION=00dc70017d222b178a002ed30e9321b12647af2d
ARG CONTAINERD_VERSION=v1.2.0-rc.1
# containerd v1.0 for integration tests
ARG CONTAINERD10_VERSION=v1.0.3
# available targets: buildkitd, buildkitd.oci_only, buildkitd.containerd_only
ARG BUILDKIT_TARGET=buildkitd
ARG REGISTRY_VERSION=v2.7.0-rc.0
ARG ROOTLESSKIT_VERSION=4f7ae4607d626f0a22fb495056d55b17cce8c01b

# The `buildkitd` stage and the `buildctl` stage are placed here
# so that they can be built quickly with legacy DAG-unaware `docker build --target=...`

FROM golang:1.11-alpine AS gobuild-base
RUN apk add --no-cache g++ linux-headers
RUN apk add --no-cache git libseccomp-dev make

FROM gobuild-base AS buildkit-base
WORKDIR /go/src/github.com/moby/buildkit
COPY . .
RUN mkdir .tmp; \
  PKG=github.com/moby/buildkit VERSION=$(git describe --match 'v[0-9]*' --dirty='.m' --always) REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi); \
  echo "-X ${PKG}/version.Version=${VERSION} -X ${PKG}/version.Revision=${REVISION} -X ${PKG}/version.Package=${PKG}" | tee .tmp/ldflags

FROM buildkit-base AS buildctl
ENV CGO_ENABLED=0
RUN go build -ldflags "$(cat .tmp/ldflags) -d" -o /usr/bin/buildctl ./cmd/buildctl

FROM buildkit-base AS buildctl-darwin
ENV CGO_ENABLED=0
ENV GOOS=darwin
RUN go build -ldflags "$(cat .tmp/ldflags)" -o /out/buildctl-darwin ./cmd/buildctl
# reset GOOS for legacy builder
ENV GOOS=linux

FROM buildkit-base AS buildkitd
ENV CGO_ENABLED=1
RUN go build -installsuffix netgo -ldflags "$(cat .tmp/ldflags) -w -extldflags -static" -tags 'seccomp netgo cgo static_build' -o /usr/bin/buildkitd ./cmd/buildkitd

# test dependencies begin here
FROM gobuild-base AS runc
ARG RUNC_VERSION
ENV CGO_ENABLED=1
RUN git clone https://github.com/opencontainers/runc.git "$GOPATH/src/github.com/opencontainers/runc" \
	&& cd "$GOPATH/src/github.com/opencontainers/runc" \
	&& git checkout -q "$RUNC_VERSION" \
	&& go build -installsuffix netgo -ldflags '-w -extldflags -static' -tags 'seccomp netgo cgo static_build' -o /usr/bin/runc ./

FROM gobuild-base AS containerd-base
RUN apk add --no-cache btrfs-progs-dev
RUN git clone https://github.com/containerd/containerd.git /go/src/github.com/containerd/containerd
WORKDIR /go/src/github.com/containerd/containerd

FROM containerd-base as containerd
ARG CONTAINERD_VERSION
RUN git checkout -q "$CONTAINERD_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim \
  && make bin/ctr

# containerd v1.0 for integration tests
FROM containerd-base as containerd10
ARG CONTAINERD10_VERSION
RUN git checkout -q "$CONTAINERD10_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim

FROM buildkit-base AS buildkitd.oci_only
ENV CGO_ENABLED=1
# mitigate https://github.com/moby/moby/pull/35456
WORKDIR /go/src/github.com/moby/buildkit
RUN go build -installsuffix netgo -ldflags "$(cat .tmp/ldflags) -w -extldflags -static" -tags 'no_containerd_worker seccomp netgo cgo static_build' -o /usr/bin/buildkitd.oci_only ./cmd/buildkitd

FROM buildkit-base AS buildkitd.containerd_only
ENV CGO_ENABLED=0
RUN go build -ldflags "$(cat .tmp/ldflags) -d"  -o /usr/bin/buildkitd.containerd_only -tags no_oci_worker ./cmd/buildkitd

FROM tonistiigi/registry:$REGISTRY_VERSION AS registry

FROM gobuild-base AS rootlesskit-base
RUN git clone https://github.com/rootless-containers/rootlesskit.git /go/src/github.com/rootless-containers/rootlesskit
WORKDIR /go/src/github.com/rootless-containers/rootlesskit

FROM rootlesskit-base as rootlesskit
ARG ROOTLESSKIT_VERSION
# mitigate https://github.com/moby/moby/pull/35456
ENV GOOS=linux
RUN git checkout -q "$ROOTLESSKIT_VERSION" \
&& go build -o /rootlesskit ./cmd/rootlesskit

FROM scratch AS buildkit-binaries
COPY --from=runc /usr/bin/runc /usr/bin/buildkit-runc
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
COPY --from=buildkitd /usr/bin/buildkitd /usr/bin

FROM buildkit-base AS integration-tests
ENV BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR="1000:1000"
RUN apk add --no-cache shadow shadow-uidmap sudo \
  && useradd --create-home --home-dir /home/user --uid 1000 -s /bin/sh user \
  && echo "XDG_RUNTIME_DIR=/run/user/1000; export XDG_RUNTIME_DIR" >> /home/user/.profile \
  && mkdir -m 0700 -p /run/user/1000 \
  && chown -R user /run/user/1000 /home/user
ENV BUILDKIT_INTEGRATION_CONTAINERD_EXTRA="containerd-1.0=/opt/containerd-1.0/bin"
COPY --from=runc /usr/bin/runc /usr/bin/buildkit-runc
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/
COPY --from=containerd10 /go/src/github.com/containerd/containerd/bin/containerd* /opt/containerd-1.0/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
COPY --from=buildkitd /usr/bin/buildkitd /usr/bin
COPY --from=registry /bin/registry /usr/bin
COPY --from=rootlesskit /rootlesskit /usr/bin/

FROM buildkit-base AS cross-windows
ENV GOOS=windows

FROM cross-windows AS buildctl.exe
RUN go build -ldflags "$(cat .tmp/ldflags)" -o /out/buildctl.exe ./cmd/buildctl

FROM cross-windows AS buildkitd.exe
ENV CGO_ENABLED=0
RUN go build -ldflags "$(cat .tmp/ldflags)" -o /out/buildkitd.exe ./cmd/buildkitd

FROM alpine AS buildkit-export
RUN apk add --no-cache git
VOLUME /var/lib/buildkit

# Copy together all binaries for oci+containerd mode
FROM buildkit-export AS buildkit-buildkitd
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildkitd /usr/bin/buildkitd /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd"]

# Copy together all binaries needed for oci worker mode
FROM buildkit-export AS buildkit-buildkitd.oci_only
COPY --from=buildkitd.oci_only /usr/bin/buildkitd.oci_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.oci_only"]

# Copy together all binaries for containerd worker mode
FROM buildkit-export AS buildkit-buildkitd.containerd_only
COPY --from=buildkitd.containerd_only /usr/bin/buildkitd.containerd_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.containerd_only"]

FROM alpine AS containerd-runtime
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

# Rootless mode.
# Still requires `--privileged`.
FROM buildkit-buildkitd AS rootless
RUN apk add --no-cache shadow shadow-uidmap \
  && useradd --create-home --home-dir /home/user --uid 1000 user \
  && mkdir -p /run/user/1000 /home/user/.local/tmp /home/user/.local/share/buildkit \
  && chown -R user /run/user/1000 /home/user \
  && rm /bin/su && ln -s /bin/busybox /bin/su
COPY --from=rootlesskit /rootlesskit /usr/bin/
USER user
ENV HOME /home/user
ENV USER user
ENV XDG_RUNTIME_DIR=/run/user/1000
ENV TMPDIR=/home/user/.local/tmp
VOLUME /home/user/.local/share/buildkit
ENTRYPOINT ["rootlesskit", "buildkitd"]

FROM buildkit-${BUILDKIT_TARGET}


