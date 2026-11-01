FROM alpine:3.22
RUN apk add --no-cache docker docker-cli containerd btrfs-progs e2fsprogs qemu-x86_64 openssh
COPY nanoc-guest.sh /opt/nanoc-guest.sh
RUN chmod +x /opt/nanoc-guest.sh
