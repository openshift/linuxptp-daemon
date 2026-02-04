FROM golang:1.24.0 AS builder
WORKDIR /go/src/github.com/k8snetworkplumbingwg/linuxptp-daemon

COPY go.mod go.sum ./
COPY vendor vendor
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY addons addons
COPY cmd cmd
COPY deploy deploy
COPY hack hack
COPY Makefile Makefile
COPY pkg pkg
COPY .git .git

RUN --mount=type=cache,target=/root/.cache/go-build make

FROM quay.io/centos/centos:stream9

RUN yum -y update && yum -y update glibc && yum --setopt=skip_missing_names_on_install=False -y \
  install \
  pciutils \
  linuxptp \
  ethtool \
  hwdata \
  synce4l \
  iproute \
  procps-ng \
  chrony \
  gpsd-minimal \
  gpsd-minimal-clients \
  && yum clean all

# Create symlinks for executables to match references
RUN ln -s /usr/bin/gpspipe /usr/local/bin/gpspipe
RUN ln -s /usr/sbin/gpsd /usr/local/sbin/gpsd
RUN ln -s /usr/bin/ubxtool /usr/local/bin/ubxtool


COPY --from=builder /go/src/github.com/k8snetworkplumbingwg/linuxptp-daemon/bin/ptp /usr/local/bin/

CMD ["/usr/local/bin/ptp"]
