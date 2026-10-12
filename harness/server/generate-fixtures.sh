#!/bin/sh
set -eu

root=${1:-/srv}

mkdir -p \
	"$root/admin" \
	"$root/api" \
	"$root/cgi-bin" \
	"$root/dashboard" \
	"$root/images"

printf '%s\n' '<!doctype html><title>h3fuzz fixture</title><h1>root</h1>' >"$root/index.html"
printf '%s\n' '<!doctype html><title>admin</title><h1>admin</h1>' >"$root/admin/index.html"
printf '%s\n' '<!doctype html><title>api</title><h1>api</h1>' >"$root/api/index.html"
printf '%s\n' '#!/bin/sh' 'printf "Content-Type: text/plain\\r\\n\\r\\ncgi fixture\\n"' >"$root/cgi-bin/index.sh"
printf '%s\n' '<!doctype html><title>cgi-bin</title><h1>cgi-bin</h1>' >"$root/cgi-bin/index.html"
printf '%s\n' '<!doctype html><title>dashboard</title><h1>dashboard</h1>' >"$root/dashboard/index.html"
printf '%s\n' '<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><path d="M0 0h1v1H0z"/></svg>' >"$root/images/pixel.svg"
printf '%s\n' '<!doctype html><title>images</title><h1>images</h1>' >"$root/images/index.html"
printf '%s\n' '<?php echo "admin fixture"; ?>' >"$root/admin.php"
printf '%s\n' '<?php echo "login fixture"; ?>' >"$root/login.php"
printf '%s\n' 'login fixture' >"$root/login"
printf '%s\n' 'healthy fixture' >"$root/health"
printf '%s\n' 'runtime fallback fixture' >"$root/runtime-fallback.txt"
printf 'x' >"$root/one-byte.txt"
: >"$root/empty.txt"

# Deterministic and highly compressible in the image layer, but presented as
# an uncompressed 1 MiB representation by Caddy.
dd if=/dev/zero of="$root/known-large.bin" bs=1048576 count=1 2>/dev/null
