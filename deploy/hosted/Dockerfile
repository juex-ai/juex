FROM public.ecr.aws/docker/library/ubuntu@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3

ARG BUILD_DNS=""

RUN if [ -n "$BUILD_DNS" ]; then printf 'nameserver %s\n' "$BUILD_DNS" > /etc/resolv.conf; fi \
    && apt-get -o APT::Update::Error-Mode=any update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       ca-certificates curl git less python3 python3-venv python3-pip ripgrep build-essential xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Official Node release checksums. Execution pins the resulting image ID too.
RUN set -eu; \
    if [ -n "$BUILD_DNS" ]; then printf 'nameserver %s\n' "$BUILD_DNS" > /etc/resolv.conf; fi; \
    case "$(dpkg --print-architecture)" in \
      amd64) arch=x64; checksum=d804845d34eddc21dc1092b519d643ef40b1f58ec5dec5c22b1f4bd8fabde6c9 ;; \
      arm64) arch=arm64; checksum=524659219d6a207a7400f2bde15d19ba060ffbe0d32a8643319ad67e3bb64c78 ;; \
      *) exit 1 ;; \
    esac; \
    curl --fail --show-error --location "https://nodejs.org/dist/v24.16.0/node-v24.16.0-linux-${arch}.tar.xz" --output /tmp/node.tar.xz; \
    printf '%s  /tmp/node.tar.xz\n' "$checksum" | sha256sum --check -; \
    tar --extract --xz --file /tmp/node.tar.xz --directory /usr/local --strip-components=1; \
    rm /tmp/node.tar.xz

ENV HOME=/home/agent \
    NPM_CONFIG_PREFIX=/home/agent/.local \
    PYTHONUSERBASE=/home/agent/.local \
    PATH=/home/agent/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    LANG=C.UTF-8

WORKDIR /workspace
