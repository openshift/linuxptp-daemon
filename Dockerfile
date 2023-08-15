FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.20-openshift-4.14 AS builder
WORKDIR /go/src/github.com/openshift/linuxptp-daemon
COPY . .
RUN make clean && make

FROM registry.ci.openshift.org/ocp/4.14:base as buildgps

RUN yum -y install git python3-pip gcc ncurses-devel

RUN pip3 install scons \
	&& git clone https://gitlab.com/gpsd/gpsd.git

WORKDIR /gpsd

RUN scons -c \
	&& scons install
	#&& scons udev-install

FROM registry.ci.openshift.org/ocp/4.14:base

COPY ./extra/leap-seconds.list /usr/share/zoneinfo/leap-seconds.list

RUN yum -y update && yum -y update glibc && yum --setopt=skip_missing_names_on_install=False -y install linuxptp ethtool hwdata  && yum clean all

RUN yum -y install python3-pip

COPY --from=buildgps /usr/local/lib/python3.6/site-packages /usr/local/lib/python3.6/site-packages

#add gpsmon
COPY --from=buildgps /usr/local/bin/gpsmon /usr/local/bin/gpsmon

#add ubxtool
COPY --from=buildgps /usr/local/bin/ubxtool /usr/local/bin/ubxtool

#add gpspipe
COPY --from=buildgps /usr/local/bin/gpspipe /usr/local/bin/gpspipe

#add gpsd
COPY --from=buildgps /usr/local/sbin/gpsd /usr/local/sbin/gpsd

COPY --from=builder /go/src/github.com/openshift/linuxptp-daemon/bin/ptp /usr/local/bin/

ENV PYTHONPATH=/usr/local/lib/python3.6/site-packages

CMD ["/usr/local/bin/ptp"]
