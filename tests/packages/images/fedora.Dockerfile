# Disposable Fedora image for the ADR-026 S4 package tests. systemd runs as PID 1
# inside this container only; the host's systemd is never touched.
FROM fedora:41

RUN dnf install -y systemd systemd-udev \
    && dnf clean all

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
