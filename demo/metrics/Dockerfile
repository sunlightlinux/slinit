# slinit as PID 1, serving Prometheus metrics.
#
# busybox because the demo is about slinit, not about the image: a
# shell and sleep are all the services below need.
FROM busybox:1.37-musl
COPY bin/ /sbin/
COPY services/ /etc/slinit.d/
EXPOSE 9100
ENTRYPOINT ["/sbin/slinit", "-o", "--services-dir", "/etc/slinit.d", \
            "--metrics-listen", "0.0.0.0:9100", "boot"]
