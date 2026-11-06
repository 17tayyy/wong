#!/bin/sh
# Installs wong and sets this machine up: curl -fsSL https://wong.bytay.dev/install.sh | sh
set -eu

repo=17tayyy/wong
server=${1:-}

fancy= gray= green= yellow= red= badge= reset=
if [ -t 1 ] && [ "${TERM:-}" != dumb ]; then
	fancy=1
	if [ -z "${NO_COLOR:-}" ]; then
		gray=$(printf '\033[90m') green=$(printf '\033[32m') yellow=$(printf '\033[33m') red=$(printf '\033[31m') reset=$(printf '\033[0m')
		badge=$(printf '\033[1;97;48;5;62m')
		case "${COLORTERM:-}${WT_SESSION:+truecolor}" in
		*truecolor* | *24bit*) badge=$(printf '\033[1;38;2;255;255;255;48;2;109;91;208m') ;;
		esac
	fi
fi

# say prints a step of the frame with a line of detail under it, or plain lines when the output is not a terminal.
say() {
	if [ -n "$fancy" ]; then
		printf '\r\033[K%s%s%s  %s\n%s│  %s%s\n' "$1" "$2" "$reset" "$3" "$gray" "$4" "$reset"
	else
		echo "wong: $3. $4."
	fi
}

fail() {
	if [ -n "$fancy" ]; then
		printf '\r\033[K%s│%s\n%s■%s  %s\n%s└%s\n' "$gray" "$reset" "$red" "$reset" "$*" "$gray" "$reset" >&2
	else
		echo "wong: $*" >&2
	fi
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

if [ -n "$fancy" ]; then
	printf '%s┌%s  %s wong %s  %sinstaller%s\n%s│%s\n' "$gray" "$reset" "$badge" "$reset" "$gray" "$reset" "$gray" "$reset"
fi

dir=
for candidate in "$HOME/.local/bin" /usr/local/bin /opt/homebrew/bin; do
	case ":$PATH:" in
	*":$candidate:"*) if [ -w "$candidate" ]; then dir=$candidate && break; fi ;;
	esac
done
rc=
if [ -z "$dir" ]; then
	dir=$HOME/.local/bin
	case ${SHELL:-} in
	*/zsh) rc=$HOME/.zshrc ;;
	*/bash) rc=$HOME/.bashrc ;;
	*) rc=$HOME/.profile ;;
	esac
	echo 'export PATH="$HOME/.local/bin:$PATH"' >>"$rc"
fi
mkdir -p "$dir"

file=wong-$os-$arch
base=https://github.com/$repo/releases/latest/download
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
if [ -n "$fancy" ]; then
	printf '%s◒%s  Downloading %s' "$green" "$reset" "$file"
else
	echo "wong: downloading $file"
fi
curl -fsSL "$base/$file" -o "$tmp" || fail "could not download $base/$file"
want=$(curl -fsSL "$base/checksums.txt" | awk -v f="$file" '$2 == f { print $1 }')
have=$( (sha256sum "$tmp" 2>/dev/null || shasum -a 256 "$tmp") | awk '{ print $1 }')
[ -n "$want" ] && [ "$want" = "$have" ] || fail "the checksum of $file does not match the release's; nothing installed"
chmod 755 "$tmp"
mv "$tmp" "$dir/wong"
version=$("$dir/wong" --version | awk '{ print $NF }')
shown=$(printf '%s' "$dir" | sed "s#^$HOME#~#")
say "$green" "◇" "Installed wong $version in $shown" "Checked against the release's checksums"
if [ -n "$rc" ]; then
	say "$yellow" "▲" "Added $shown to your PATH in $(printf '%s' "$rc" | sed "s#^$HOME#~#")" "Open a new terminal before starting your agents"
fi

export WONG_INSTALLER=$fancy
if [ -n "${WONG_TOKEN:-}" ]; then
	[ -n "$server" ] || fail "WONG_TOKEN is set but no server address was given: pass it after sh -s --"
	printf '%s\n' "$WONG_TOKEN" | "$dir/wong" setup "$server"
elif (: </dev/tty) 2>/dev/null; then
	"$dir/wong" setup ${server:+"$server"} </dev/tty
else
	"$dir/wong" setup ${server:+"$server"}
fi
