# Official Ubuntu 24.04 base; update the digest deliberately with CI validation.
FROM docker.io/library/ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac

# Tool image only. No project binary, credentials, test fixtures or running init.
# Apt package revisions follow Ubuntu security updates, not a frozen archive.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        bash ca-certificates coreutils dpkg findutils grep sed tar xz-utils \
        systemd systemd-sysv \
    && rm -rf /var/lib/apt/lists/*

ENV LC_ALL=C
CMD ["bash"]
