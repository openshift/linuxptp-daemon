FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.20-openshift-4.15 AS builder
WORKDIR /go/src/github.com/openshift/linuxptp-daemon
COPY . .
RUN make clean && make

FROM registry.ci.openshift.org/ocp/4.15:base-rhel9

RUN yum -y update && \
    yum -y update glibc &&  \
    yum --setopt=skip_missing_names_on_install=False -y install ethtool hwdata && \
    yum clean all

RUN yum install -y gpsd-minimal
RUN yum install -y gpsd-minimal-clients

# Test RPM by Miroslav
COPY ./linuxptp-3.1.1-6.el9_2.5.test1.x86_64.rpm .
RUN yum install -y ./linuxptp-3.1.1-6.el9_2.5.test1.x86_64.rpm

# Create symlinks for executables to match references
RUN ln -s /usr/bin/gpspipe /usr/local/bin/gpspipe
RUN ln -s /usr/sbin/gpsd /usr/local/sbin/gpsd
RUN ln -s /usr/bin/ubxtool /usr/local/bin/ubxtool

COPY --from=builder /go/src/github.com/openshift/linuxptp-daemon/bin/ptp /usr/local/bin/
COPY ./extra/leap-seconds.list /usr/share/zoneinfo/leap-seconds.list

CMD ["/usr/local/bin/ptp"]
