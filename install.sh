#!/bin/sh
# Installs wong and sets this machine up: curl -fsSL https://raw.githubusercontent.com/17tayyy/wong/main/install.sh | sh -s -- [server-url]
set -eu

repo=17tayyy/wong
server=${1:-}

fail() {
	echo "wong: $*" >&2
	exit 1
}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "no build for $(uname -s) here; on Windows, download wong-windows-amd64.exe from https://github.com/$repo/releases" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "no build for $(uname -m)" ;;
esac

dir=
for candidate in "$HOME/.local/bin" /usr/local/bin /opt/homebrew/bin; do
	case ":$PATH:" in
	*":$candidate:"*) if [ -w "$candidate" ]; then dir=$candidate && break; fi ;;
	esac
done
if [ -z "$dir" ]; then
	dir=$HOME/.local/bin
	case ${SHELL:-} in
	*/zsh) rc=$HOME/.zshrc ;;
	*/bash) rc=$HOME/.bashrc ;;
	*) rc=$HOME/.profile ;;
	esac
	echo 'export PATH="$HOME/.local/bin:$PATH"' >>"$rc"
	echo "wong: added $dir to PATH in $rc; open a new terminal before starting Claude Code"
fi
mkdir -p "$dir"

file=wong-$os-$arch
base=https://github.com/$repo/releases/latest/download
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
echo "wong: downloading $file"
curl -fsSL "$base/$file" -o "$tmp" || fail "could not download $base/$file"
want=$(curl -fsSL "$base/checksums.txt" | awk -v f="$file" '$2 == f { print $1 }')
have=$( (sha256sum "$tmp" 2>/dev/null || shasum -a 256 "$tmp") | awk '{ print $1 }')
[ -n "$want" ] && [ "$want" = "$have" ] || fail "the checksum of $file does not match the release's; nothing installed"
chmod 755 "$tmp"
mv "$tmp" "$dir/wong"
echo "wong: installed $("$dir/wong" --version) in $dir"

if [ -n "${WONG_TOKEN:-}" ]; then
	printf '%s\n' "$WONG_TOKEN" | "$dir/wong" setup $server
elif [ -n "$server" ]; then
	"$dir/wong" setup "$server" </dev/tty
else
	"$dir/wong" setup
fi
