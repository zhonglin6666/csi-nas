FROM centos:7.4.1708

RUN yum install -y e4fsprogs

COPY nsenter /
COPY bin/csi-nas-plugin /bin/csi-nas-plugin
RUN chmod +x /bin/csi-nas-plugin
RUN chmod 755 /nsenter

ENTRYPOINT ["/bin/csi-nas-plugin"]
