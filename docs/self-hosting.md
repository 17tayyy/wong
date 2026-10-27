# Self-hosting the wong server

The server keeps every file of the brain, with its history, in one SQLite file. Each machine
syncs with it through `wong pull` and `wong push`, which the Claude Code plugin runs when a
session starts and ends.

## Install

Build a static binary and copy it to a Linux host or container:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o wong ./cmd/wong
```

On the host, as root:

```sh
install -m 0755 wong /usr/local/bin/wong
useradd --system --home-dir /var/lib/wong --create-home --shell /usr/sbin/nologin wong
chmod 700 /var/lib/wong
install -m 0644 deploy/wong.service /etc/systemd/system/wong.service
systemctl daemon-reload
systemctl enable --now wong
curl http://localhost:8787/api/health
```

A small container is enough: one core, 512 MB of memory and a few GB of disk. On Proxmox, an
unprivileged Debian LXC works:

```sh
pct create <id> local:vztmpl/debian-13-standard_<version>_amd64.tar.zst \
  --hostname wong --cores 1 --memory 512 --swap 512 --rootfs local-lvm:4 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1 --onboot 1
```

## Connect a machine

Each machine has its own token. Create it on the server and pipe it straight into
`wong remote` on the machine, so it is never printed:

```sh
ssh <server> 'runuser -u wong -- wong serve --db /var/lib/wong/wong.db --new-token <machine>' \
  | wong remote http://<server>:8787
```

Then run `wong push` on the machine that has the brain, and `wong pull` on the others.

## Keep it private

The API uses bearer tokens over plain HTTP and is not meant to face the internet. Reach it
over a private network: your LAN, or a VPN such as Tailscale (a subnet router is enough).

## Back up

`/var/lib/wong/wong.db` holds the brain and its history: back up the host or container, or
copy the database with `sqlite3 /var/lib/wong/wong.db ".backup /path/to/copy.db"`.
