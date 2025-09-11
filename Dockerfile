FROM golang:1.23.5 AS builder
WORKDIR /go/src/github.com/k8snetworkplumbingwg/linuxptp-daemon
COPY . .
RUN make clean && make

FROM quay.io/centos/centos:stream9

RUN yum -y update && yum -y update glibc && yum --setopt=skip_missing_names_on_install=False -y install linuxptp ethtool hwdata synce4l && yum clean all


RUN yum install -y chrony
RUN yum install -y gpsd-minimal
RUN yum install -y gpsd-minimal-clients

# Create symlinks for executables to match references
RUN ln -s /usr/bin/gpspipe /usr/local/bin/gpspipe
RUN ln -s /usr/sbin/gpsd /usr/local/sbin/gpsd
RUN ln -s /usr/bin/ubxtool /usr/local/bin/ubxtool


COPY --from=builder /go/src/github.com/k8snetworkplumbingwg/linuxptp-daemon/bin/ptp /usr/local/bin/

CMD ["/usr/local/bin/ptp"]